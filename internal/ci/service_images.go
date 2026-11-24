package ci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mmrzaf/gitman/internal/activity"
	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/id"
	"github.com/mmrzaf/gitman/internal/postgres"
)

// ImageFacts resolves the pipelines that can still need images and the latest
// deployments that can map to built tags. It is one snapshot: callers classify
// and plan from it without re-reading checkouts or the database.
func (s *Service) ImageFacts(ctx context.Context) (*ImageFacts, error) {
	pipelines, incomplete, err := selectRequiredPipelines(ctx, s.db.Q)
	if err != nil {
		return nil, err
	}
	facts := NewImageFacts()
	facts.Incomplete = incomplete
	repoIDs := map[string]bool{}
	for _, fact := range pipelines {
		cfg, err := Parse(fact.Pipeline)
		if err != nil {
			// Stored bytes were valid when the run was created; a row that
			// is not readable now may still require images. Treat the
			// snapshot as incomplete so nothing is pruned.
			facts.Incomplete = true
			continue
		}
		source := ImageSource{Repo: fact.RepoName, Ref: fact.Source}
		for _, ref := range append([]string{cfg.Image}, cfg.Requires...) {
			normalized := NormalizeImageReference(ref)
			required := facts.Required[normalized]
			if required.Ref == "" {
				required.Ref = ref
			}
			required.Sources = appendImageSource(required.Sources, source)
			facts.Required[normalized] = required
		}
		for repository, policy := range cfg.Images {
			built := facts.Built[repository]
			if policy.Keep > built.Keep {
				built.Keep = policy.Keep
			}
			built.Sources = appendImageSource(built.Sources, source)
			built.repoIDs = appendUnique(built.repoIDs, fact.RepoID)
			facts.Built[repository] = built
			repoIDs[fact.RepoID] = true
		}
	}
	if len(repoIDs) > 0 {
		ids := make([]string, 0, len(repoIDs))
		for repoID := range repoIDs {
			ids = append(ids, repoID)
		}
		deployments, err := selectLiveDeploymentsForRepos(ctx, s.db.Q, ids)
		if err != nil {
			return nil, err
		}
		versions := map[string]map[string]bool{}
		for _, deployment := range deployments {
			if versions[deployment.RepoID] == nil {
				versions[deployment.RepoID] = map[string]bool{}
			}
			versions[deployment.RepoID][deployment.Version] = true
		}
		for repository, built := range facts.Built {
			mapped := map[string]bool{}
			for _, repoID := range built.repoIDs {
				for version := range versions[repoID] {
					mapped[version] = true
				}
			}
			built.Deployments = sortedKeys(mapped)
			built.repoIDs = nil
			facts.Built[repository] = built
		}
	}
	return facts, nil
}

// PublishInventory replaces a host's image inventory in place, with the policy
// the worker is actually enforcing and the time the database received it.
func (s *Service) PublishInventory(ctx context.Context, inventory HostInventory) error {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	return upsertImageHost(ctx, s.db.Q, inventory)
}

// ImageHosts returns every known Docker host, its classified images, the plan
// for pruning its built images, missing required images on its ready workers,
// and its recent image operations.
func (s *Service) ImageHosts(ctx context.Context) ([]ImageHost, error) {
	facts, err := s.ImageFacts(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := selectImageHosts(ctx, s.db.Q)
	if err != nil {
		return nil, err
	}
	workers, err := s.Workers(ctx)
	if err != nil {
		return nil, err
	}
	operations, err := s.ImageOperations(ctx, 200)
	if err != nil {
		return nil, err
	}

	byEngine := map[string]*ImageHost{}
	for _, row := range rows {
		var images []InventoryImage
		if err := json.Unmarshal(row.Images, &images); err != nil {
			return nil, fmt.Errorf("decode image inventory for %s: %w", row.EngineID, err)
		}
		classified := Classify(images, facts)
		plan := PlanPrune(classified)
		applyPlan(classified, plan)
		host := &ImageHost{
			EngineID: row.EngineID, Hostname: row.Hostname, HasInventory: true, Stale: row.Stale,
			CollectedAt: row.CollectedAt, Automation: row.Automation, FactsIncomplete: facts.Incomplete, Images: classified, Plan: plan,
			Cache: ImageCacheState{
				Size: row.CacheSize, Reclaimable: row.CacheReclaimable,
				Policy:   ImageCachePolicy{KeepGiB: row.KeepGiB, OlderThanHours: row.OlderHours},
				Disabled: row.CacheDisabled, AttemptedAt: row.AttemptedAt, PrunedAt: row.PrunedAt, Reclaimed: row.Reclaimed, Error: row.CacheError,
				AboveCap: !row.CacheDisabled && row.KeepGiB > 0 && row.CacheSize > int64(row.KeepGiB)<<30,
			},
		}
		byEngine[row.EngineID] = host
	}
	for _, worker := range workers {
		if worker.EngineID == "" {
			continue
		}
		host := byEngine[worker.EngineID]
		if host == nil {
			host = &ImageHost{EngineID: worker.EngineID, Hostname: worker.Hostname, FactsIncomplete: facts.Incomplete}
			byEngine[worker.EngineID] = host
		}
		if host.Hostname == "" {
			host.Hostname = worker.Hostname
		}
		host.Workers = append(host.Workers, HostWorker{
			ID: worker.ID, Hostname: worker.Hostname, Ready: worker.Ready, images: worker.Images,
		})
	}
	for engineID, host := range byEngine {
		host.Missing = missingImages(facts, host.Workers)
		for _, operation := range operations {
			if operation.EngineID == engineID && len(host.Operations) < 20 {
				host.Operations = append(host.Operations, operation)
			}
		}
	}
	hosts := make([]ImageHost, 0, len(byEngine))
	for _, host := range byEngine {
		hosts = append(hosts, *host)
	}
	sort.Slice(hosts, func(i, j int) bool {
		if hosts[i].Hostname != hosts[j].Hostname {
			return hosts[i].Hostname < hosts[j].Hostname
		}
		return hosts[i].EngineID < hosts[j].EngineID
	})
	return hosts, nil
}

// ImageHost returns one Docker host by engine ID.
func (s *Service) ImageHost(ctx context.Context, engineID string) (*ImageHost, error) {
	hosts, err := s.ImageHosts(ctx)
	if err != nil {
		return nil, err
	}
	for i := range hosts {
		if hosts[i].EngineID == engineID {
			return &hosts[i], nil
		}
	}
	return nil, apperr.New(apperr.KindNotFound, "There is no image inventory for that Docker host.")
}

// QueueImagePrune plans a built-image prune from the current inventory and
// queues it for the host's workers. An empty plan returns no operation.
func (s *Service) QueueImagePrune(ctx context.Context, engineID, actorID string) (string, PrunePlan, error) {
	return s.queueImagePrune(ctx, engineID, actorID, nil)
}

// QueueImagePruneTargets queues only the targets the operator reviewed. The
// fresh plan is intersected with the requested ref+ID pairs, so an image that
// became prunable after the page rendered is never removed unseen.
func (s *Service) QueueImagePruneTargets(ctx context.Context, engineID, actorID string, requested []ImageTarget) (string, PrunePlan, error) {
	return s.queueImagePrune(ctx, engineID, actorID, requested)
}

func (s *Service) queueImagePrune(ctx context.Context, engineID, actorID string, requested []ImageTarget) (string, PrunePlan, error) {
	host, err := s.freshImageHost(ctx, engineID)
	if err != nil {
		return "", PrunePlan{}, err
	}
	if host.FactsIncomplete {
		return "", PrunePlan{}, apperr.New(apperr.KindConflict, "Image facts are incomplete; no built image is pruned. Try again after every stored pipeline can be read.")
	}
	plan := host.Plan
	if requested != nil {
		wanted := make(map[string]bool, len(requested))
		for _, target := range requested {
			wanted[NormalizeImageReference(target.Ref)+"\x00"+target.ID] = true
		}
		plan.Remove = nil
		plan.ReclaimableBytes = 0
		for _, image := range host.Plan.Remove {
			if !wanted[ImageKey(image)] {
				continue
			}
			plan.Remove = append(plan.Remove, image)
			plan.ReclaimableBytes += image.Size
		}
	}
	if len(plan.Remove) == 0 {
		return "", plan, nil
	}
	targets := make([]ImageTarget, 0, len(plan.Remove))
	for _, image := range plan.Remove {
		targets = append(targets, ImageTarget{Ref: image.Ref(), ID: image.ID})
	}
	id, err := s.queueImageOperation(ctx, engineID, actorID, ImageOperationPrune, imageOperationPayload{Targets: targets})
	return id, plan, err
}

// QueueImageRemove queues one explicitly named image. Required and in-use
// images are refused here, before anything is written.
func (s *Service) QueueImageRemove(ctx context.Context, engineID, ref, actorID string) (string, error) {
	host, err := s.freshImageHost(ctx, engineID)
	if err != nil {
		return "", err
	}
	if host.FactsIncomplete {
		return "", apperr.New(apperr.KindConflict, "Image facts are incomplete; no image is removed. Try again after every stored pipeline can be read.")
	}
	normalized := NormalizeImageReference(ref)
	var found *ClassifiedImage
	for i := range host.Images {
		image := &host.Images[i]
		if image.Ref() == ref || image.NormalizedRef() == normalized || image.ID == ref {
			found = image
			break
		}
	}
	if found == nil {
		return "", apperr.New(apperr.KindNotFound, "There is no image "+ref+" in that host's inventory.")
	}
	ok, reason := PlanRemoveOne(*found)
	if !ok {
		return "", apperr.New(apperr.KindConflict, "Refusing to remove "+found.Ref()+": "+reason+".")
	}
	return s.queueImageOperation(ctx, engineID, actorID, ImageOperationRemove,
		imageOperationPayload{Targets: []ImageTarget{{Ref: found.Ref(), ID: found.ID}}})
}

// QueueImageCachePrune queues a cache prune. A nil policy uses the policy the
// worker last reported; a non-nil one is a manual override.
func (s *Service) QueueImageCachePrune(ctx context.Context, engineID, actorID string, policy *ImageCachePolicy) (string, error) {
	host, err := s.freshImageHost(ctx, engineID)
	if err != nil {
		return "", err
	}
	applied := host.Cache.Policy
	if applied.KeepGiB == 0 {
		defaults := config.DefaultImageCache()
		applied = ImageCachePolicy{KeepGiB: defaults.KeepGiB, OlderThanHours: defaults.OlderThanHours()}
	}
	if policy != nil {
		applied = *policy
	}
	if applied.KeepGiB < 1 || applied.KeepGiB > config.MaxImageCacheKeepGiB {
		return "", apperr.New(apperr.KindInvalid, fmt.Sprintf("keep storage must be between 1 and %d GiB.", config.MaxImageCacheKeepGiB))
	}
	if applied.OlderThanHours < 1 || applied.OlderThanHours > config.MaxImageCacheMaxAgeHours {
		return "", apperr.New(apperr.KindInvalid, fmt.Sprintf("the maximum age must be between 1 and %d hours.", config.MaxImageCacheMaxAgeHours))
	}
	return s.queueImageOperation(ctx, engineID, actorID, ImageOperationCache, imageOperationPayload{Cache: applied})
}

func (s *Service) freshImageHost(ctx context.Context, engineID string) (*ImageHost, error) {
	host, err := s.ImageHost(ctx, engineID)
	if err != nil {
		return nil, err
	}
	if !host.HasInventory || host.Stale {
		return nil, apperr.New(apperr.KindConflict,
			fmt.Sprintf("The image inventory for %s is older than %s; try again after the worker refreshes it.", host.Hostname, ImageInventoryStaleAfter))
	}
	return host, nil
}

func (s *Service) queueImageOperation(ctx context.Context, engineID, actorID string, kind ImageOperationKind, payload imageOperationPayload) (string, error) {
	ctx, release, err := s.db.AdmitMutation(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	id := id.New()
	err = s.db.Tx(ctx, func(tx postgres.Tx) error {
		return insertImageOperation(ctx, tx, id, engineID, actorID, kind, payload)
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// ClaimImageOperation leases one operation for a Docker host, or returns nil
// when none is pending.
func (s *Service) ClaimImageOperation(ctx context.Context, engineID, workerID string) (*ImageOperation, error) {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	var operation *ImageOperation
	err := s.db.Tx(ctx, func(tx postgres.Tx) error {
		var err error
		operation, err = claimImageOperation(ctx, tx, engineID, workerID)
		return err
	})
	if errors.Is(err, postgres.ErrNotFound) {
		return nil, nil
	}
	return operation, err
}

// ImageOperations returns recent image operations, newest first.
func (s *Service) ImageOperations(ctx context.Context, limit int) ([]ImageOperation, error) {
	return selectImageOperations(ctx, s.db.Q, limit)
}

// ImageOperation returns one operation by ID.
func (s *Service) ImageOperation(ctx context.Context, id string) (*ImageOperation, error) {
	return selectImageOperation(ctx, s.db.Q, id)
}

// ReleaseImageOperation returns an unstarted operation to the queue, for a
// worker that could not reach Docker and should retry later.
func (s *Service) ReleaseImageOperation(ctx context.Context, id, reason string) error {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	return releaseImageOperation(ctx, s.db.Q, id, reason)
}

// FinishImageOperation records results and audits them in one transaction. A
// retry that lost its first commit is harmless: only the first update wins.
func (s *Service) FinishImageOperation(ctx context.Context, operation *ImageOperation, result ImageOperationResult) error {
	ctx, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer stop()
	summary := result.Error
	if summary == "" && operationFailed(result) {
		summary = "One or more images could not be removed."
	}
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		finished, err := finishImageOperation(ctx, tx, operation.ID, result, summary)
		if err != nil {
			return err
		}
		if !finished {
			return nil
		}
		failed := operationFailed(result) || result.Error != ""
		return activity.RecordAdmin(ctx, tx, "", operation.ActorID, imageOperationAction(operation.Kind, failed),
			imageOperationDetail(operation, result))
	})
}

// RecordCachePrune stores the last cache-prune attempt and returns the error
// it replaced, so an unchanged recurring failure is not audited every pass.
func (s *Service) RecordCachePrune(ctx context.Context, hostID string, reclaimed int64, cacheError string, success bool) (string, error) {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	var previous string
	err := s.db.Tx(ctx, func(tx postgres.Tx) error {
		var err error
		previous, err = recordCachePrune(ctx, tx, hostID, reclaimed, cacheError, success)
		return err
	})
	return previous, err
}

// RecordImageAudit writes an image management event for an automatic action.
func (s *Service) RecordImageAudit(ctx context.Context, actorID, action, detail string) error {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		return activity.RecordAdmin(ctx, tx, "", actorID, action, detail)
	})
}

func appendImageSource(sources []ImageSource, source ImageSource) []ImageSource {
	for _, existing := range sources {
		if existing == source {
			return sources
		}
	}
	return append(sources, source)
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func missingImages(facts *ImageFacts, workers []HostWorker) []MissingImage {
	missing := map[string]*MissingImage{}
	for _, worker := range workers {
		if !worker.Ready {
			continue
		}
		advertised := make(map[string]bool, len(worker.images))
		for _, image := range worker.images {
			advertised[NormalizeImageReference(image)] = true
		}
		for normalized, required := range facts.Required {
			if advertised[normalized] {
				continue
			}
			entry := missing[normalized]
			if entry == nil {
				entry = &MissingImage{Ref: required.Ref, Sources: required.Sources}
				missing[normalized] = entry
			}
			entry.Workers = appendUnique(entry.Workers, worker.ID)
		}
	}
	result := make([]MissingImage, 0, len(missing))
	for _, entry := range missing {
		sort.Strings(entry.Workers)
		result = append(result, *entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Ref < result[j].Ref })
	return result
}

func operationFailed(result ImageOperationResult) bool {
	for _, image := range result.Results {
		if image.Status == "failed" {
			return true
		}
	}
	return false
}

func imageOperationAction(kind ImageOperationKind, failed bool) string {
	switch {
	case kind == ImageOperationRemove && failed:
		return activity.ImageRemoveFailed
	case kind == ImageOperationRemove:
		return activity.ImageRemoved
	case kind == ImageOperationPrune && failed:
		return activity.ImagesPruneFailed
	case kind == ImageOperationPrune:
		return activity.ImagesPruned
	case failed:
		return activity.ImageCachePruneFailed
	default:
		return activity.ImageCachePruned
	}
}

func imageOperationDetail(operation *ImageOperation, result ImageOperationResult) string {
	host := operation.EngineID
	if operation.Kind == ImageOperationCache {
		policy := operation.Cache
		return fmt.Sprintf("on %s: cache policy %d GiB, %d hours; reclaimed %d bytes",
			host, policy.KeepGiB, policy.OlderThanHours, result.ReclaimedBytes)
	}
	parts := make([]string, 0, len(result.Results))
	for i, image := range result.Results {
		if i == 20 {
			parts = append(parts, fmt.Sprintf("and %d more", len(result.Results)-i))
			break
		}
		part := image.Ref + " " + image.Status
		if image.Reason != "" && image.Status != "removed" {
			part += " (" + image.Reason + ")"
		}
		parts = append(parts, part)
	}
	return fmt.Sprintf("on %s: %s; reclaimed %d bytes", host, strings.Join(parts, ", "), result.ReclaimedBytes)
}
