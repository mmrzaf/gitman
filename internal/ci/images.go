package ci

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ImageInventoryStaleAfter is how old a worker's published image inventory may
// be before the interface and the prune planner refuse to act on it. Workers
// publish at least once a minute, so five minutes leaves room for a missed
// pass or a brief database outage without acting on a stale picture.
const ImageInventoryStaleAfter = 5 * time.Minute

// ImageClass is what an image means to Gitman.
type ImageClass string

const (
	// ImageRequired is an image a pipeline runs in or requires.
	ImageRequired ImageClass = "required"
	// ImageBuilt is an image a pipeline declares it builds.
	ImageBuilt ImageClass = "built"
	// ImageOther is any other image on the host, including dangling ones.
	ImageOther ImageClass = "other"
)

// InventoryImage is one image on a worker host: one row per tag, or one row
// for an untagged (dangling) image.
type InventoryImage struct {
	// Repository is the name Docker reports, for display. NormalizedRepository
	// is what every comparison uses.
	Repository           string    `json:"repository"`
	NormalizedRepository string    `json:"normalized_repository"`
	Tag                  string    `json:"tag,omitempty"`
	ID                   string    `json:"id"`
	Digests              []string  `json:"digests,omitempty"`
	Size                 int64     `json:"size"`
	Created              time.Time `json:"created"`
	LastTagTime          time.Time `json:"last_tag_time,omitempty"`
	InUse                bool      `json:"in_use"`
}

// Ref is the name an operator uses for the image: repo:tag, or the image ID
// when it is dangling.
func (i InventoryImage) Ref() string {
	if i.Tag == "" {
		return i.ID
	}
	return i.Repository + ":" + i.Tag
}

// NormalizedRef is Ref in the canonical form used for matching.
func (i InventoryImage) NormalizedRef() string {
	if i.Tag == "" {
		return i.ID
	}
	return i.NormalizedRepository + ":" + i.Tag
}

// Dangling reports whether the image has no tag.
func (i InventoryImage) Dangling() bool { return i.Tag == "" }

// ImageSource names the pipeline whose stored bytes contributed an image
// fact. Ref is a ref name or "run #N", for a person reading a reason.
type ImageSource struct {
	Repo string `json:"repo"`
	Ref  string `json:"ref"`
}

func (s ImageSource) String() string {
	if s.Ref == "" {
		return s.Repo
	}
	return s.Repo + " " + s.Ref
}

// RequiredImage is one image reference a pipeline requires, with every source
// that requires it.
type RequiredImage struct {
	// Ref is the spelling the first source used, for display.
	Ref     string        `json:"ref"`
	Sources []ImageSource `json:"sources"`
}

// BuiltPolicy is one repository's built-image retention facts, merged across
// every pipeline that declares it.
type BuiltPolicy struct {
	Keep        int           `json:"keep"`
	Sources     []ImageSource `json:"sources"`
	Deployments []string      `json:"deployments,omitempty"`

	// repoIDs are the repositories whose deployments can map to this image
	// repository. They are used only to collect Deployments, never shown.
	repoIDs []string
}

// ImageFacts is everything classification needs from PostgreSQL. It is built
// once for a page or a plan, so classifying many images does not repeat the
// same queries.
type ImageFacts struct {
	Required map[string]RequiredImage
	Built    map[string]BuiltPolicy
}

// NewImageFacts returns empty facts ready to be filled.
func NewImageFacts() *ImageFacts {
	return &ImageFacts{Required: map[string]RequiredImage{}, Built: map[string]BuiltPolicy{}}
}

// ClassifiedImage is one inventory image with the class Gitman gives it.
type ClassifiedImage struct {
	InventoryImage
	Class    ImageClass `json:"class"`
	Reason   string     `json:"reason"`
	Keep     int        `json:"keep,omitempty"`
	Deployed bool       `json:"deployed"`
	// DeployedFallback says the newest tag was protected because no live tag
	// matched a recorded deployment version, rather than because it matched
	// one exactly.
	DeployedFallback bool `json:"deployed_fallback,omitempty"`
	WithinKeep       bool `json:"within_keep"`
	Prunable         bool `json:"prunable"`
	// Refusal is why the planner will not remove it, or empty when it will.
	Refusal string `json:"refusal,omitempty"`
}

// IsRequired reports whether the image is required by a pipeline.
func (c ClassifiedImage) IsRequired() bool { return c.Class == ImageRequired }

// IsBuilt reports whether the image is built by a pipeline.
func (c ClassifiedImage) IsBuilt() bool { return c.Class == ImageBuilt }

// IsOther reports whether the image is neither required nor built.
func (c ClassifiedImage) IsOther() bool { return c.Class == ImageOther }

// Classify gives every image exactly one class, in the precedence required >
// built > other, and marks built images' keep/deployed state. It is pure: the
// caller supplies inventory and facts, so it can be tested exhaustively and
// used identically by the page, the CLI and a worker's execution-time check.
func Classify(images []InventoryImage, facts *ImageFacts) []ClassifiedImage {
	if facts == nil {
		facts = NewImageFacts()
	}
	out := make([]ClassifiedImage, len(images))
	builtByRepository := map[string][]int{}

	for i, image := range images {
		classified := ClassifiedImage{InventoryImage: image}
		switch {
		case image.Dangling():
			classified.Class = ImageOther
			classified.Reason = "dangling image"
		case requiredByReference(image, facts.Required):
			classified.Class = ImageRequired
			classified.Reason = "required by " + sourcesText(requiredSources(image, facts.Required))
		case requiredByDigest(image, facts.Required):
			classified.Class = ImageRequired
			classified.Reason = "required by " + sourcesText(requiredSources(image, facts.Required))
		default:
			built, ok := facts.Built[image.NormalizedRepository]
			if !ok {
				classified.Class = ImageOther
				classified.Reason = "not declared in any pipeline's images"
				break
			}
			classified.Class = ImageBuilt
			classified.Keep = built.Keep
			classified.Reason = "built by " + sourcesText(built.Sources)
			builtByRepository[image.NormalizedRepository] = append(builtByRepository[image.NormalizedRepository], i)
		}
		out[i] = classified
	}

	for repository, indices := range builtByRepository {
		// Newest first. Creation time is the retention order; the later
		// tie-breakers make a boundary deterministic when two builds share one.
		sort.SliceStable(indices, func(a, b int) bool {
			left, right := out[indices[a]], out[indices[b]]
			if !left.Created.Equal(right.Created) {
				return left.Created.After(right.Created)
			}
			if !left.LastTagTime.Equal(right.LastTagTime) {
				return left.LastTagTime.After(right.LastTagTime)
			}
			if left.Tag != right.Tag {
				return left.Tag > right.Tag
			}
			return left.ID > right.ID
		})
		built := facts.Built[repository]
		for rank, index := range indices {
			out[index].WithinKeep = rank < built.Keep
		}

		// A deployment version protects the tag it names. If no live tag
		// names a version, the live image cannot be identified; protect the
		// newest tag rather than risk removing what is still deployed.
		unmatched := false
		for _, version := range built.Deployments {
			matched := false
			for _, index := range indices {
				if out[index].NormalizedRef() == ImageTagForVersion(repository, version) {
					out[index].Deployed = true
					matched = true
				}
			}
			unmatched = unmatched || !matched
		}
		if unmatched {
			out[indices[0]].Deployed = true
			out[indices[0]].DeployedFallback = true
		}
	}
	return out
}

func requiredByReference(image InventoryImage, required map[string]RequiredImage) bool {
	_, ok := required[image.NormalizedRef()]
	return ok
}

func requiredByDigest(image InventoryImage, required map[string]RequiredImage) bool {
	for _, digest := range image.Digests {
		if _, ok := required[digest]; ok {
			return true
		}
	}
	return false
}

func requiredSources(image InventoryImage, required map[string]RequiredImage) []ImageSource {
	if req, ok := required[image.NormalizedRef()]; ok {
		return req.Sources
	}
	var sources []ImageSource
	for _, digest := range image.Digests {
		if req, ok := required[digest]; ok {
			sources = append(sources, req.Sources...)
		}
	}
	return sources
}

func sourcesText(sources []ImageSource) string {
	if len(sources) == 0 {
		return "a pipeline"
	}
	labels := make([]string, len(sources))
	for i, source := range sources {
		labels[i] = source.String()
	}
	return strings.Join(labels, ", ")
}

// PrunePlan is the exact result of planning a built-image prune.
type PrunePlan struct {
	// Remove holds the images to remove, in deterministic repository/tag
	// order. Each carries its ID so a worker can refuse a retagged image.
	Remove []ClassifiedImage `json:"remove"`
	// Refuse holds every image not removed, with the reason.
	Refuse []ClassifiedImage `json:"refuse"`
	// ReclaimableBytes sums the listed image sizes. Layers shared by several
	// images may be counted more than once; it is an estimate.
	ReclaimableBytes int64 `json:"reclaimable_bytes"`
}

// PlanPrune decides which built images a prune may remove. It never removes
// required images, images a container uses, the deployed version, or one of
// the newest keep tags. Images from other pipelines, and dangling images, are
// left for an explicit removal.
func PlanPrune(images []ClassifiedImage) PrunePlan {
	plan := PrunePlan{}
	for _, image := range images {
		image.Prunable = false
		switch {
		case image.Class == ImageRequired:
			image.Refusal = image.Reason
		case image.InUse:
			image.Refusal = "in use by a container"
		case image.Class == ImageOther && image.Dangling():
			image.Refusal = "dangling image; remove it explicitly"
		case image.Class == ImageOther:
			image.Refusal = image.Reason
		case image.Deployed && image.DeployedFallback:
			image.Refusal = "newest tag kept because the deployed version could not be matched to a tag"
		case image.Deployed:
			image.Refusal = "currently deployed"
		case image.WithinKeep:
			image.Refusal = fmt.Sprintf("within the newest %d tags", image.Keep)
		default:
			image.Prunable = true
			plan.Remove = append(plan.Remove, image)
			plan.ReclaimableBytes += image.Size
			continue
		}
		plan.Refuse = append(plan.Refuse, image)
	}
	sortClassified(plan.Remove)
	sortClassified(plan.Refuse)
	return plan
}

// PlanRemoveOne decides whether one explicitly named image may be removed.
// Only required and in-use images are protected here: a person naming one
// image is the explicit action that other images need, including dangling
// ones and a deployed version they have decided to replace.
func PlanRemoveOne(image ClassifiedImage) (bool, string) {
	switch {
	case image.Class == ImageRequired:
		return false, image.Reason
	case image.InUse:
		return false, "in use by a container"
	}
	return true, ""
}

func sortClassified(images []ClassifiedImage) {
	sort.SliceStable(images, func(i, j int) bool {
		left, right := images[i], images[j]
		if left.NormalizedRepository != right.NormalizedRepository {
			return left.NormalizedRepository < right.NormalizedRepository
		}
		if left.Tag != right.Tag {
			return left.Tag < right.Tag
		}
		return left.ID < right.ID
	})
}

// ImageKey identifies one inventory row for matching a plan to fresh
// inventory. The ID is part of it: the same tag may later point at a new image.
func ImageKey(image ClassifiedImage) string {
	return image.NormalizedRef() + "\x00" + image.ID
}

// applyPlan marks each classified image with the plan's decision, for tables
// that show whether a row is prunable and why not.
func applyPlan(images []ClassifiedImage, plan PrunePlan) {
	remove := make(map[string]bool, len(plan.Remove))
	for _, image := range plan.Remove {
		remove[ImageKey(image)] = true
	}
	refuse := make(map[string]string, len(plan.Refuse))
	for _, image := range plan.Refuse {
		refuse[ImageKey(image)] = image.Refusal
	}
	for i := range images {
		key := ImageKey(images[i])
		images[i].Prunable = remove[key]
		images[i].Refusal = refuse[key]
	}
}

// ImageCachePolicy is the build-cache policy a worker enforces, and the one a
// manual cache prune may override for a single run.
type ImageCachePolicy struct {
	KeepGiB        int `json:"keep_gib"`
	OlderThanHours int `json:"older_than_hours"`
}

// CacheSizeBytes is the policy's storage cap in bytes.
func (p ImageCachePolicy) CacheSizeBytes() int64 {
	return int64(p.KeepGiB) << 30
}

// HostInventory is what one worker publishes about its Docker host.
type HostInventory struct {
	EngineID  string
	WorkerID  string
	Hostname  string
	Images    []InventoryImage
	CacheSize int64
	// CacheReclaimable is how much of CacheSize Docker reports as reclaimable.
	CacheReclaimable int64
	Cache            ImageCachePolicy
	CacheDisabled    bool
	Automation       string
}

// HostWorker is one worker process sharing a Docker host.
type HostWorker struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	Ready    bool   `json:"ready"`

	// images is the normalized set the worker advertises for claim routing.
	// It is not shown; missing required images are computed from it.
	images []string
}

// MissingImage is a required image a ready worker does not advertise.
type MissingImage struct {
	Ref     string        `json:"ref"`
	Sources []ImageSource `json:"sources"`
	Workers []string      `json:"workers"`
}

// ImageCacheState is the last cache measurement and prune result for a host.
type ImageCacheState struct {
	Size        int64            `json:"size"`
	Reclaimable int64            `json:"reclaimable"`
	Policy      ImageCachePolicy `json:"policy"`
	Disabled    bool             `json:"disabled"`
	AboveCap    bool             `json:"above_cap"`
	PrunedAt    *time.Time       `json:"pruned_at,omitempty"`
	Reclaimed   int64            `json:"reclaimed_bytes"`
	Error       string           `json:"error,omitempty"`
}

// ImageHost is one Docker host's images and cache, as the page and CLI show
// them.
type ImageHost struct {
	EngineID     string            `json:"host_id"`
	Hostname     string            `json:"hostname"`
	HasInventory bool              `json:"has_inventory"`
	Stale        bool              `json:"stale"`
	CollectedAt  time.Time         `json:"collected_at,omitempty"`
	Automation   string            `json:"automation"`
	Workers      []HostWorker      `json:"workers"`
	Images       []ClassifiedImage `json:"images"`
	Plan         PrunePlan         `json:"plan"`
	Missing      []MissingImage    `json:"missing,omitempty"`
	Cache        ImageCacheState   `json:"cache"`
	Operations   []ImageOperation  `json:"operations,omitempty"`
}

// ImageOperationKind is a durable piece of worker-targeted image work.
type ImageOperationKind string

const (
	ImageOperationRemove ImageOperationKind = "image.remove"
	ImageOperationPrune  ImageOperationKind = "image.prune"
	ImageOperationCache  ImageOperationKind = "cache.prune"
)

// ImageTarget names one image a prune or removal planned. ID pins the tag to
// the image it had when planned, so a tag retagged in the meantime is left
// alone.
type ImageTarget struct {
	Ref string `json:"ref"`
	ID  string `json:"id"`
}

// ImageResult is what happened to one planned image.
type ImageResult struct {
	Ref    string `json:"ref"`
	Status string `json:"status"` // removed, skipped or failed
	Reason string `json:"reason,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
}

// ImageOperationResult is the per-image and cache outcome of an operation.
type ImageOperationResult struct {
	Results        []ImageResult `json:"results,omitempty"`
	ReclaimedBytes int64         `json:"reclaimed_bytes,omitempty"`
	Error          string        `json:"error,omitempty"`
}

// ImageOperation is one durable request to a worker host.
type ImageOperation struct {
	ID             string             `json:"id"`
	EngineID       string             `json:"host_id"`
	WorkerID       string             `json:"worker_id,omitempty"`
	Kind           ImageOperationKind `json:"kind"`
	ActorID        string             `json:"actor_id,omitempty"`
	Actor          string             `json:"actor,omitempty"`
	Targets        []ImageTarget      `json:"targets,omitempty"`
	Cache          ImageCachePolicy   `json:"cache,omitempty"`
	CreatedAt      time.Time          `json:"created_at"`
	ClaimedAt      *time.Time         `json:"claimed_at,omitempty"`
	CompletedAt    *time.Time         `json:"completed_at,omitempty"`
	Attempts       int                `json:"attempts"`
	Error          string             `json:"error,omitempty"`
	Results        []ImageResult      `json:"results,omitempty"`
	ReclaimedBytes int64              `json:"reclaimed_bytes,omitempty"`
}

// Pending reports whether the operation has not finished.
func (o ImageOperation) Pending() bool { return o.CompletedAt == nil }

// Failed reports whether at least one image or the cache operation failed.
func (o ImageOperation) Failed() bool {
	for _, result := range o.Results {
		if result.Status == "failed" {
			return true
		}
	}
	return o.Error != ""
}
