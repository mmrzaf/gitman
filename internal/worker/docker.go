package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/config"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Docker runs steps as containers through the docker command-line
// client, the same client an operator uses to inspect them.
type Docker struct {
	Resources config.Resources
	// Binary is the docker client to run, "docker" to find it on PATH.
	Binary string
	// Socket is the Docker socket on the host, mounted into steps of a
	// pipeline that uses Docker itself.
	Socket string
	// Instance identifies the Gitman instance whose steps these are. Every
	// step container is labelled with it, and leftovers are looked for
	// only among containers carrying it, so two Gitman instances sharing
	// one Docker host never remove each other's steps.
	Instance string
}

// Container labels. runLabel carries the run a container belongs to, so
// leftovers of a crashed worker can be found and removed; instanceLabel
// carries Docker.Instance.
const (
	runLabel      = "gitman.run"
	instanceLabel = "gitman.instance"
)

// clientWaitDelay bounds how long a docker client that has been killed
// may keep its output pipes open through a child process it forked.
const clientWaitDelay = 10 * time.Second

// removeTimeout bounds a force-remove issued after the step's own context
// has already ended, so a docker daemon that never answers cannot leave
// that cleanup running forever.
const removeTimeout = 30 * time.Second

// Image inventory and build-cache pruning can walk a host's whole image
// store, so they get their own budgets rather than the ten seconds ordinary
// probes use. A cache prune is the one Docker call that may legitimately
// take minutes on a large host.
const (
	inventoryTimeout  = 20 * time.Second
	cachePruneTimeout = 10 * time.Minute
)

// command returns a docker client invocation bound to ctx: when ctx ends
// the client is killed, which is the only way to stop it while it is
// blocked on a daemon that has stopped answering.
func (d *Docker) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, d.Binary, args...)
	cmd.WaitDelay = clientWaitDelay
	return cmd
}

// containerSpec is one step's container.
type containerSpec struct {
	Resources config.Resources
	Name      string
	RunID     string
	Image     string
	Script    string
	// Source and Meta are host directories mounted at /workspace and
	// /gitman.
	Source string
	Meta   string
	// Env holds the step's ordinary variables, passed on the command
	// line. Secrets are never on a command line, which other users of
	// the host can read: they reach the container through the client's
	// own environment. A secret named like an ordinary variable wins.
	Env          map[string]string
	Secrets      map[string]string
	DockerSocket bool
	// Created persists the daemon ID before the container can start.
	Created func(context.Context, string) error
}

// args is the "docker run" command line for spec. MKNOD is dropped: the
// workspace is a host directory the worker reads as root afterwards, and
// a step must not be able to leave a device node there.
func (spec containerSpec) args(socket, instance string) []string {
	resources := spec.Resources.WithDefaults()
	args := []string{"create", "--name", spec.Name,
		"--label", runLabel + "=" + spec.RunID,
		"--label", instanceLabel + "=" + instance,
		"--cap-drop", "MKNOD",
		"--memory", fmt.Sprintf("%dm", resources.MemoryMiB), "--memory-swap", fmt.Sprintf("%dm", resources.MemoryMiB), "--cpus", fmt.Sprint(resources.CPUs), "--pids-limit", fmt.Sprint(resources.PIDs), "--pull", "never",
		"--workdir", containerSource,
		"--volume", spec.Source + ":" + containerSource,
		"--volume", spec.Meta + ":" + containerMeta,
	}
	if spec.DockerSocket {
		args = append(args, "--volume", socket+":/var/run/docker.sock")
	}
	for _, key := range sortedKeys(spec.Env) {
		if _, secret := spec.Secrets[key]; !secret {
			args = append(args, "--env", key+"="+spec.Env[key])
		}
	}
	for _, key := range sortedKeys(spec.Secrets) {
		args = append(args, "--env", key)
	}
	// "--" ends the options, so the image can never be read as one.
	return append(args, "--entrypoint", "/bin/sh", "--", spec.Image, "-ec", spec.Script)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Available reports whether the Docker daemon answers. A worker checks
// this before claiming a run, so a host whose daemon is down leaves runs
// queued for other workers instead of claiming and failing every one.
func (d *Docker) Available(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := d.command(ctx, "version", "--format", "{{.Server.Version}}").CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker version: %s", strings.TrimSpace(lastLines(string(out), 3)))
	}
	return nil
}

// ImageExists reports whether image is present on the Docker host.
// Gitman never pulls images; a missing image is a setup problem for the
// operator, not something a run fixes by downloading.
func (d *Docker) ImageExists(ctx context.Context, image string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := d.command(ctx, "image", "inspect", "--format", "{{.Id}}", "--", image).CombinedOutput()
	if err == nil {
		return true, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && strings.Contains(strings.ToLower(string(out)), "no such image") {
		return false, nil
	}
	return false, fmt.Errorf("docker image inspect %s: %s", image, strings.TrimSpace(lastLines(string(out), 3)))
}

// TerminationUnknown means the daemon has not confirmed that the container
// is gone. The caller must retain its workspace and pause new claims.
type TerminationUnknown struct {
	Name string
	Err  error
}

func (e *TerminationUnknown) Error() string {
	return "container termination is unconfirmed: " + e.Name + ": " + e.Err.Error()
}
func (e *TerminationUnknown) Unwrap() error { return e.Err }

var errContainerAbsent = errors.New("container is absent")

type containerState struct {
	Running  bool
	ExitCode int
	Status   string
	Error    string
}

func (d *Docker) state(ctx context.Context, name string) (*containerState, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := d.command(ctx, "inspect", "--format", "{{json .State}}", name).CombinedOutput()
	if err != nil {
		message := strings.ToLower(string(out))
		if ctx.Err() == nil && (strings.Contains(message, "no such container") || strings.Contains(message, "no such object")) {
			return nil, errContainerAbsent
		}
		return nil, fmt.Errorf("inspect container: %w: %s", err, lastLines(string(out), 3))
	}
	var state containerState
	if err := json.Unmarshal(out, &state); err != nil {
		return nil, fmt.Errorf("decode container state: %w", err)
	}
	return &state, nil
}

// Run creates a retained container, persists its ID, then starts and attaches.
// A client's exit status is never used as the script's exit status: the daemon's
// retained state is the execution receipt. The caller removes it after recording.
func (d *Docker) Run(ctx context.Context, spec containerSpec, out io.Writer) (int, error) {
	create, stopCreate := context.WithTimeout(ctx, 10*time.Second)
	spec.Resources = d.Resources
	cmd := d.command(create, spec.args(d.Socket, d.Instance)...)
	cmd.Env = os.Environ()
	for _, key := range sortedKeys(spec.Secrets) {
		cmd.Env = append(cmd.Env, key+"="+spec.Secrets[key])
	}
	receipt, err := cmd.CombinedOutput()
	stopCreate()
	cleanup := func(cause error) (int, error) {
		// The create reply can be lost after creation. The persisted deterministic
		// name lets recovery find it even when no container ID reached this client.
		if err := d.stopContainer(spec.Name); err != nil {
			return -1, &TerminationUnknown{Name: spec.Name, Err: errors.Join(cause, err)}
		}
		return -1, cause
	}
	if err != nil {
		return cleanup(fmt.Errorf("create docker container: %w", err))
	}
	containerID := strings.TrimSpace(string(receipt))
	if containerID == "" {
		return cleanup(errors.New("Docker returned an empty container ID"))
	}
	if spec.Created != nil {
		persist, stop := context.WithTimeout(ctx, 10*time.Second)
		err := spec.Created(persist, containerID)
		stop()
		if err != nil {
			return cleanup(fmt.Errorf("persist container ID: %w", err))
		}
	}
	cmd = d.command(ctx, "start", "--attach", containerID)
	cmd.Stdout, cmd.Stderr = out, out

	if err := cmd.Start(); err != nil {
		return cleanup(fmt.Errorf("start container: %w", err))
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err = <-waited:
	case <-ctx.Done():
		code, cleanupErr := cleanup(ctx.Err())
		// Wait only to reap the process; cleanup reports the cancellation.
		<-waited //nolint:errcheck // the wait error is the cancellation cleanup returned
		return code, cleanupErr
	}
	if ctx.Err() != nil {
		return cleanup(ctx.Err())
	}

	state, stateErr := d.state(ctx, containerID)
	if stateErr != nil {
		return cleanup(stateErr)
	}
	if state.Running || state.Status != "exited" {
		return cleanup(fmt.Errorf("container did not exit: %w", errors.Join(err, errors.New(state.Status))))
	}
	// Docker's State.Error represents failure to execute the image, not a
	// pipeline exit. Report it as infrastructure failure without retrying.
	if state.Error != "" {
		return cleanup(errors.New("Docker could not execute the container"))
	}
	return state.ExitCode, nil
}

func (d *Docker) stopContainer(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), removeTimeout)
	defer cancel()
	return d.removeContainer(ctx, name)
}

// runContainers lists this instance's step containers on the Docker
// host, running or not, by container ID, with the run each belongs to.
func (d *Docker) runContainers(ctx context.Context) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := d.command(ctx, "ps", "--all", "--no-trunc",
		"--filter", "label="+runLabel,
		"--filter", "label="+instanceLabel+"="+d.Instance,
		"--format", "{{.ID}} {{.Label \""+runLabel+"\"}}").Output()
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	containers := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if containerID, runID, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			containers[containerID] = runID
		}
	}
	return containers, nil
}

// removeContainer force-removes one container.
func (d *Docker) removeContainer(ctx context.Context, containerID string) error {
	ctx, cancel := context.WithTimeout(ctx, removeTimeout)
	defer cancel()
	out, err := d.command(ctx, "rm", "--force", containerID).CombinedOutput()
	if err != nil {
		// Not found is a confirmed absence. A daemon connection failure or lost
		// reply is not, and must never authorize deleting a mounted workspace.
		if ctx.Err() == nil && strings.Contains(strings.ToLower(string(out)), "no such container") {
			return nil
		}
		return fmt.Errorf("remove container %s: %w", containerID, err)
	}
	return nil
}

// Images returns the tags, digests and IDs currently available on this daemon.
func (d *Docker) Images(ctx context.Context) ([]string, error) {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	out, err := d.command(ctx, "image", "ls", "--no-trunc", "--digests", "--format", "{{.Repository}}:{{.Tag}} {{.Repository}}@{{.Digest}} {{.ID}}").Output()
	if err != nil {
		return nil, fmt.Errorf("list Docker images: %w", err)
	}
	seen := map[string]string{}
	for _, image := range strings.Fields(string(out)) {
		if !strings.Contains(image, "<none>") {
			image = ci.NormalizeImageReference(image)
			seen[image] = image
		}
	}
	return sortedKeys(seen), nil
}

// stopRetained confirms termination but preserves the daemon's exit receipt.
func (d *Docker) stopRetained(ctx context.Context, name string) (*containerState, error) {
	state, err := d.state(ctx, name)
	if errors.Is(err, errContainerAbsent) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if state.Running {
		stop, cancel := context.WithTimeout(ctx, removeTimeout)
		err := d.command(stop, "kill", name).Run()
		cancel()
		if err != nil {
			return nil, fmt.Errorf("kill retained container: %w", err)
		}
		state, err = d.state(ctx, name)
		if err != nil {
			return nil, err
		}
		if state.Running {
			return nil, errors.New("container is still running")
		}
	}
	return state, nil
}

// EngineID identifies a Docker host across worker process and container restarts.
func (d *Docker) EngineID(ctx context.Context) (string, error) {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	out, err := d.command(ctx, "info", "--format", "{{.ID}}").Output()
	if err != nil {
		return "", err
	}
	engine := strings.TrimSpace(string(out))
	if engine == "" {
		return "", errors.New("Docker engine has no identity")
	}
	return engine, nil
}

// imageListRow is one line of "docker image ls --format '{{json .}}'".
type imageListRow struct {
	Repository string `json:"Repository"`
	Tag        string `json:"Tag"`
	Digest     string `json:"Digest"`
	ID         string `json:"ID"`
	CreatedAt  string `json:"CreatedAt"`
	Size       string `json:"Size"`
}

type imageInspect struct {
	ID          string    `json:"Id"`
	RepoTags    []string  `json:"RepoTags"`
	RepoDigests []string  `json:"RepoDigests"`
	Created     time.Time `json:"Created"`
	Size        int64     `json:"Size"`
	Metadata    struct {
		LastTagTime time.Time `json:"LastTagTime"`
	} `json:"Metadata"`
}

// systemDFRow is one line of "docker system df --format '{{json .}}'". The
// client prints every column as a string, counts included, so only the columns
// the inventory reads are declared.
type systemDFRow struct {
	Type        string `json:"Type"`
	Size        string `json:"Size"`
	Reclaimable string `json:"Reclaimable"`
}

// ImageInventory lists every tagged and dangling image on the host, the
// containers that reference each image ID, and the build cache's size. The
// caller publishes it as the host's latest inventory; it is never used to
// decide a removal until the worker re-reads it at execution time.
func (d *Docker) ImageInventory(ctx context.Context) ([]ci.InventoryImage, int64, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, inventoryTimeout)
	defer cancel()

	out, err := d.command(ctx, "image", "ls", "--no-trunc", "--digests", "--format", "{{json .}}").Output()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("list Docker images: %w", err)
	}
	var rows []imageListRow
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row imageListRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, 0, 0, fmt.Errorf("decode Docker image list: %w", err)
		}
		rows = append(rows, row)
	}

	ids := make([]string, 0, len(rows))
	seenID := map[string]bool{}
	for _, row := range rows {
		if row.ID != "" && !seenID[row.ID] {
			seenID[row.ID] = true
			ids = append(ids, row.ID)
		}
	}
	inspected := map[string]imageInspect{}
	if len(ids) > 0 {
		args := append([]string{"image", "inspect", "--format", "{{json .}}"}, ids...)
		out, err := d.command(ctx, args...).Output()
		if err != nil {
			return nil, 0, 0, fmt.Errorf("inspect Docker images: %w", err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var image imageInspect
			if err := json.Unmarshal([]byte(line), &image); err != nil {
				return nil, 0, 0, fmt.Errorf("decode Docker image inspection: %w", err)
			}
			inspected[image.ID] = image
		}
	}

	inUse, err := d.containerImages(ctx)
	if err != nil {
		return nil, 0, 0, err
	}

	cacheSize, cacheReclaimable, err := d.BuildCache(ctx)
	if err != nil {
		return nil, 0, 0, err
	}

	images := make([]ci.InventoryImage, 0, len(rows))
	tagged := map[string]bool{}
	seenRef := map[string]bool{}
	for _, row := range rows {
		if row.ID == "" {
			continue
		}
		if row.Repository == "" || row.Repository == "<none>" || row.Tag == "" || row.Tag == "<none>" {
			continue
		}
		ref := row.Repository + ":" + row.Tag
		if seenRef[ref] {
			continue
		}
		seenRef[ref] = true
		tagged[row.ID] = true
		images = append(images, inventoryImage(row, inspected[row.ID], inUse))
	}
	for _, row := range rows {
		if row.ID == "" || tagged[row.ID] || seenRef[row.ID] {
			continue
		}
		seenRef[row.ID] = true
		image := inspected[row.ID]
		created := image.Created
		size := image.Size
		if created.IsZero() {
			created, _ = time.Parse(time.RFC3339Nano, row.CreatedAt)
		}
		if size == 0 {
			size, _ = parseDockerSize(row.Size)
		}
		images = append(images, ci.InventoryImage{
			ID: normalizeImageID(row.ID), Size: size, Created: created,
			Digests: normalizeDigests(image.RepoDigests), InUse: inUse[normalizeImageID(row.ID)],
		})
	}
	sort.Slice(images, func(i, j int) bool {
		if images[i].NormalizedRepository != images[j].NormalizedRepository {
			return images[i].NormalizedRepository < images[j].NormalizedRepository
		}
		if images[i].Tag != images[j].Tag {
			return images[i].Tag < images[j].Tag
		}
		return images[i].ID < images[j].ID
	})
	return images, cacheSize, cacheReclaimable, nil
}

func inventoryImage(row imageListRow, inspected imageInspect, inUse map[string]bool) ci.InventoryImage {
	created := inspected.Created
	size := inspected.Size
	if created.IsZero() {
		created, _ = time.Parse(time.RFC3339Nano, row.CreatedAt)
	}
	if size == 0 {
		size, _ = parseDockerSize(row.Size)
	}
	lastTagTime := inspected.Metadata.LastTagTime
	if lastTagTime.Year() <= 1 {
		lastTagTime = time.Time{}
	}
	id := normalizeImageID(row.ID)
	return ci.InventoryImage{
		Repository: row.Repository, NormalizedRepository: ci.NormalizeImageRepository(row.Repository),
		Tag: row.Tag, ID: id, Digests: normalizeDigests(inspected.RepoDigests),
		Size: size, Created: created, LastTagTime: lastTagTime, InUse: inUse[id],
	}
}

func normalizeImageID(id string) string {
	if len(id) == 64 && strings.Trim(id, "0123456789abcdef") == "" {
		return "sha256:" + id
	}
	return id
}

func normalizeDigests(digests []string) []string {
	if len(digests) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(digests))
	for _, digest := range digests {
		normalized = append(normalized, ci.NormalizeImageReference(digest))
	}
	return normalized
}

// containerImages returns the image ID of every container, running or stopped.
func (d *Docker) containerImages(ctx context.Context) (map[string]bool, error) {
	out, err := d.command(ctx, "ps", "-aq", "--no-trunc").Output()
	if err != nil {
		return nil, fmt.Errorf("list Docker containers: %w", err)
	}
	ids := strings.Fields(string(out))
	inUse := map[string]bool{}
	for start := 0; start < len(ids); start += 256 {
		end := min(start+256, len(ids))
		args := append([]string{"container", "inspect", "--format", "{{.Image}}"}, ids[start:end]...)
		out, err := d.command(ctx, args...).Output()
		if err != nil {
			return nil, fmt.Errorf("inspect Docker containers: %w", err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if image := normalizeImageID(strings.TrimSpace(line)); image != "" {
				inUse[image] = true
			}
		}
	}
	return inUse, nil
}

// BuildCache returns the build cache's current size and reclaimable size.
func (d *Docker) BuildCache(ctx context.Context) (int64, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, inventoryTimeout)
	defer cancel()
	out, err := d.command(ctx, "system", "df", "--format", "{{json .}}").Output()
	if err != nil {
		return 0, 0, fmt.Errorf("read Docker disk usage: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row systemDFRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return 0, 0, fmt.Errorf("decode Docker disk usage: %w", err)
		}
		if !strings.EqualFold(strings.TrimSpace(row.Type), "Build Cache") {
			continue
		}
		size, err := parseDockerSize(row.Size)
		if err != nil {
			return 0, 0, fmt.Errorf("parse build cache size %q: %w", row.Size, err)
		}
		reclaimable, err := parseDockerSize(row.Reclaimable)
		if err != nil {
			return 0, 0, fmt.Errorf("parse build cache reclaimable size %q: %w", row.Reclaimable, err)
		}
		return size, reclaimable, nil
	}
	return 0, 0, nil
}

// PruneBuildCache enforces the storage cap and, when olderThanHours is
// positive, the age limit. It passes --all because without it Docker removes
// only dangling cache and leaves the layer cache of build steps, which is
// nearly all of it, so the cap would never be met. --all still removes only
// unused records: cache a running build holds is kept. A zero age omits the age
// filter, for the second pass that makes the cap effective.
func (d *Docker) PruneBuildCache(ctx context.Context, keepGiB, olderThanHours int) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, cachePruneTimeout)
	defer cancel()
	args := []string{"builder", "prune", "--force", "--all", "--keep-storage", fmt.Sprintf("%dGiB", keepGiB)}
	if olderThanHours > 0 {
		args = append(args, "--filter", fmt.Sprintf("until=%dh", olderThanHours))
	}
	out, err := d.command(ctx, args...).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("prune Docker build cache: %s", strings.TrimSpace(lastLines(string(out), 3)))
	}
	return parseReclaimed(string(out)), nil
}

// RemoveImage removes one tag, or one dangling image by ID, without --force.
// An already absent image is success: a retried operation must be idempotent.
func (d *Docker) RemoveImage(ctx context.Context, ref string) error {
	ctx, cancel := context.WithTimeout(ctx, removeTimeout)
	defer cancel()
	out, err := d.command(ctx, "image", "rm", "--", ref).CombinedOutput()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	message := strings.ToLower(string(out))
	if strings.Contains(message, "no such image") {
		return nil
	}
	return fmt.Errorf("docker image rm %s: %s", ref, strings.TrimSpace(lastLines(string(out), 3)))
}

var dockerSizePattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*([A-Za-z]+)$`)

// parseDockerSize turns the sizes Docker prints (1.2GB, 512MiB, 0B) into
// bytes. Decimal units are powers of 1000, as Docker prints them.
func parseDockerSize(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0" {
		return 0, nil
	}
	if i := strings.Index(raw, " ("); i >= 0 {
		raw = raw[:i]
	}
	match := dockerSizePattern.FindStringSubmatch(raw)
	if match == nil {
		return 0, fmt.Errorf("unrecognised size %q", raw)
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return 0, err
	}
	var multiplier float64
	switch strings.ToLower(match[2]) {
	case "b":
		multiplier = 1
	case "kb":
		multiplier = 1e3
	case "mb":
		multiplier = 1e6
	case "gb":
		multiplier = 1e9
	case "tb":
		multiplier = 1e12
	case "pb":
		multiplier = 1e15
	case "kib":
		multiplier = 1 << 10
	case "mib":
		multiplier = 1 << 20
	case "gib":
		multiplier = 1 << 30
	case "tib":
		multiplier = 1 << 40
	case "pib":
		multiplier = 1 << 50
	default:
		return 0, fmt.Errorf("unrecognised size unit %q", match[2])
	}
	return int64(value * multiplier), nil
}

// parseReclaimed reads the total a docker prune reports. BuildKit prints
// "Total reclaimed space:", older output "Total:".
func parseReclaimed(output string) int64 {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		for _, prefix := range []string{"Total reclaimed space:", "Total:"} {
			if strings.HasPrefix(line, prefix) {
				size, err := parseDockerSize(strings.TrimSpace(strings.TrimPrefix(line, prefix)))
				if err == nil {
					return size
				}
			}
		}
	}
	return 0
}
