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
	"sort"
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

// Run runs one step's container, streaming its combined output to out,
// and returns the step's exit code. When ctx ends first, the docker
// client is killed and the container is removed in the background —
// stopping only the client would leave the container running — and Run
// returns ctx's error.
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
		<-waited
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
	out, err := d.command(ctx, "ps", "--all",
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
