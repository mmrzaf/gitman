package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mmrzaf/gitman/internal/ci"
)

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

func (d *Docker) imageListRows(ctx context.Context) ([]imageListRow, error) {
	out, err := d.command(ctx, "image", "ls", "--no-trunc", "--digests", "--format", "{{json .}}").Output()
	if err != nil {
		return nil, fmt.Errorf("list Docker images: %w", err)
	}
	var rows []imageListRow
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row imageListRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("decode Docker image list: %w", err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func imageListIDs(rows []imageListRow) []string {
	ids := make([]string, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		if row.ID != "" && !seen[row.ID] {
			seen[row.ID] = true
			ids = append(ids, row.ID)
		}
	}
	return ids
}

func (d *Docker) inspectImageIDs(ctx context.Context, ids []string) (map[string]imageInspect, error) {
	inspected := map[string]imageInspect{}
	if len(ids) == 0 {
		return inspected, nil
	}
	args := append([]string{"image", "inspect", "--format", "{{json .}}"}, ids...)
	out, err := d.command(ctx, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("inspect Docker images: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var image imageInspect
		if err := json.Unmarshal([]byte(line), &image); err != nil {
			return nil, fmt.Errorf("decode Docker image inspection: %w", err)
		}
		inspected[image.ID] = image
	}
	return inspected, nil
}

// ImageInventory lists every tagged and dangling image on the host, the
// containers that reference each image ID, and the build cache's size. The
// caller publishes it as the host's latest inventory; it is never used to
// decide a removal until the worker re-reads it at execution time.
func (d *Docker) ImageInventory(ctx context.Context) ([]ci.InventoryImage, int64, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, inventoryTimeout)
	defer cancel()

	rows, err := d.imageListRows(ctx)
	if err != nil {
		return nil, 0, 0, err
	}
	inspected, err := d.inspectImageIDs(ctx, imageListIDs(rows))
	if err != nil {
		// An image can disappear between `image ls` and `image inspect`.
		// Re-list once and inspect the new snapshot rather than leaving the
		// host's inventory stale for every caller.
		rows, err = d.imageListRows(ctx)
		if err != nil {
			return nil, 0, 0, err
		}
		inspected, err = d.inspectImageIDs(ctx, imageListIDs(rows))
		if err != nil {
			return nil, 0, 0, err
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

func (d *Docker) containerIDs(ctx context.Context) ([]string, error) {
	out, err := d.command(ctx, "ps", "-aq", "--no-trunc").Output()
	if err != nil {
		return nil, fmt.Errorf("list Docker containers: %w", err)
	}
	return strings.Fields(string(out)), nil
}

func (d *Docker) inspectContainerImages(ctx context.Context, ids []string) (map[string]bool, error) {
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

// containerImages returns the image ID of every container, running or stopped.
func (d *Docker) containerImages(ctx context.Context) (map[string]bool, error) {
	ids, err := d.containerIDs(ctx)
	if err != nil {
		return nil, err
	}
	inUse, err := d.inspectContainerImages(ctx, ids)
	if err != nil {
		// A container can finish between `ps` and `inspect`; retry once
		// against a fresh list so transient churn does not freeze inventory.
		ids, err = d.containerIDs(ctx)
		if err != nil {
			return nil, err
		}
		inUse, err = d.inspectContainerImages(ctx, ids)
		if err != nil {
			return nil, err
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
