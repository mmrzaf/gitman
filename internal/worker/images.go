package worker

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/mmrzaf/gitman/internal/activity"
	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/config"
)

// images runs the worker's image duties: publishing its host's inventory,
// enforcing the build-cache policy, and executing operations an admin queued.
// It is separate from the run loop because a large inventory or prune must not
// delay a heartbeat, but every action that touches Docker takes the exclusive
// side of the host lock so it can never overlap a run.
func (w *Worker) images(ctx context.Context) {
	if w.docker == nil {
		return
	}
	w.publishInventory(ctx, true)
	w.enforceCache(ctx, "startup")
	w.runImageOperations(ctx)

	inventory := time.NewTicker(inventoryInterval)
	cache := time.NewTicker(cacheEnforceInterval)
	poll := time.NewTicker(imagePoll)
	defer inventory.Stop()
	defer cache.Stop()
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-inventory.C:
			w.publishInventory(ctx, false)
		case <-cache.C:
			w.enforceCache(ctx, "periodic")
		case <-poll.C:
			w.runImageOperations(ctx)
			if w.cfg.ImageAutomation == config.ImageAutomationLowDisk {
				w.lowDisk(ctx)
			}
		}
	}
}

// publishInventory replaces this host's inventory, at most once per
// inventoryMinGap unless force is set. A busy worker that finishes many short
// runs must not make Docker inspect every image after each one.
func (w *Worker) publishInventory(ctx context.Context, force bool) {
	if w.docker == nil {
		return
	}
	w.inventoryMu.Lock()
	defer w.inventoryMu.Unlock()
	now := time.Now()
	if !force && !w.lastInventory.IsZero() && now.Sub(w.lastInventory) < inventoryMinGap {
		return
	}
	engine, err := w.docker.EngineID(ctx)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Warn("could not identify the Docker host; image inventory not published", "error", err)
		}
		return
	}
	images, cacheSize, cacheReclaimable, err := w.docker.ImageInventory(ctx)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Warn("could not read the Docker image inventory", "error", err)
		}
		return
	}
	policy := ci.ImageCachePolicy{KeepGiB: w.cfg.ImageCache.KeepGiB, OlderThanHours: w.cfg.ImageCache.OlderThanHours()}
	if err := w.ci.PublishInventory(ctx, ci.HostInventory{
		EngineID: engine, WorkerID: w.id, Hostname: w.cfg.Hostname,
		Images: images, CacheSize: cacheSize, CacheReclaimable: cacheReclaimable,
		Cache: policy, CacheDisabled: w.cfg.ImageCache.Disabled, Automation: w.cfg.ImageAutomation,
	}); err != nil {
		if ctx.Err() == nil {
			w.log.Warn("could not publish the Docker image inventory", "error", err)
		}
		return
	}
	w.lastInventory = now
}

// runImageOperations leases and executes one host-targeted operation. It
// claims under the exclusive host lock, so two workers on one host cannot pick
// up the same work and no run can be executing while it applies.
func (w *Worker) runImageOperations(ctx context.Context) {
	if w.docker == nil {
		return
	}
	paused, err := w.ci.Maintenance(ctx)
	if err != nil || paused {
		return
	}
	release, locked, err := hostExecutionLock(w.cfg.WorkspaceRoot, true)
	if err != nil || !locked {
		return
	}
	defer release()
	engine, err := w.docker.EngineID(ctx)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Warn("could not identify the Docker host; image operations not claimed", "error", err)
		}
		return
	}
	operation, err := w.ci.ClaimImageOperation(ctx, engine, w.id)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Warn("could not claim an image operation", "error", err)
		}
		return
	}
	if operation == nil {
		return
	}
	w.executeImageOperation(ctx, operation)
}

func (w *Worker) executeImageOperation(ctx context.Context, operation *ci.ImageOperation) {
	result := ci.ImageOperationResult{}
	switch operation.Kind {
	case ci.ImageOperationCache:
		reclaimed, err := w.pruneCachePolicy(ctx, operation.Cache)
		result.ReclaimedBytes = reclaimed
		if err != nil {
			result.Error = err.Error()
		}
	case ci.ImageOperationRemove, ci.ImageOperationPrune:
		var err error
		result, err = w.applyImageTargets(ctx, operation.Targets, operation.Kind == ci.ImageOperationPrune)
		if err != nil {
			if ctx.Err() != nil {
				result.Error = "The worker shut down before the operation finished."
			} else {
				w.log.Warn("could not apply an image operation", "operation", operation.ID, "error", err)
				// Nothing was removed: return the lease so another worker
				// can retry, rather than making the admin queue it again.
				if releaseErr := w.ci.ReleaseImageOperation(context.WithoutCancel(ctx), operation.ID); releaseErr != nil {
					w.log.Warn("could not release an image operation", "operation", operation.ID, "error", releaseErr)
				}
				return
			}
		}
	default:
		result.Error = "Unknown image operation kind."
	}

	w.publishInventory(context.WithoutCancel(ctx), true)
	if operation.Kind == ci.ImageOperationCache {
		if _, err := w.ci.RecordCachePrune(context.WithoutCancel(ctx), operation.EngineID, result.ReclaimedBytes, result.Error); err != nil {
			w.log.Warn("could not record a cache prune result", "error", err)
		}
	}
	if err := w.ci.FinishImageOperation(ctx, operation, result); err != nil {
		w.log.Error("could not record an image operation's result", "operation", operation.ID, "error", err)
	}
}

// applyImageTargets removes the planned images after re-reading inventory,
// required images and deployments. The plan is advice; every target is checked
// again here, including the image ID, so a tag retagged since planning is left
// alone. A per-image refusal or failure is a result, not an operation error.
func (w *Worker) applyImageTargets(ctx context.Context, targets []ci.ImageTarget, prune bool) (ci.ImageOperationResult, error) {
	result := ci.ImageOperationResult{}
	facts, err := w.ci.ImageFacts(ctx)
	if err != nil {
		return result, fmt.Errorf("read image facts: %w", err)
	}
	images, _, _, err := w.docker.ImageInventory(ctx)
	if err != nil {
		return result, fmt.Errorf("read image inventory: %w", err)
	}
	classified := ci.Classify(images, facts)
	plan := ci.PlanPrune(classified)
	allowed := make(map[string]bool, len(plan.Remove))
	for _, image := range plan.Remove {
		allowed[ci.ImageKey(image)] = true
	}
	refused := make(map[string]string, len(plan.Refuse))
	for _, image := range plan.Refuse {
		refused[ci.ImageKey(image)] = image.Refusal
	}

	for _, target := range targets {
		if ctx.Err() != nil {
			result.Error = "The worker shut down before the operation finished."
			return result, ctx.Err()
		}
		image, found := findImage(classified, target)
		if !found {
			result.Results = append(result.Results, ci.ImageResult{Ref: target.Ref, Status: "removed", Reason: "already absent"})
			continue
		}
		if image.ID != target.ID {
			result.Results = append(result.Results, ci.ImageResult{Ref: target.Ref, Status: "skipped", Reason: "the tag now points to a different image"})
			continue
		}
		if prune {
			if !allowed[ci.ImageKey(image)] {
				reason := refused[ci.ImageKey(image)]
				if reason == "" {
					reason = "no longer eligible for pruning"
				}
				result.Results = append(result.Results, ci.ImageResult{Ref: target.Ref, Status: "skipped", Reason: reason})
				continue
			}
		} else if ok, reason := ci.PlanRemoveOne(image); !ok {
			result.Results = append(result.Results, ci.ImageResult{Ref: target.Ref, Status: "skipped", Reason: reason})
			continue
		}
		if err := w.docker.RemoveImage(ctx, target.Ref); err != nil {
			result.Results = append(result.Results, ci.ImageResult{Ref: target.Ref, Status: "failed", Reason: err.Error()})
			continue
		}
		result.Results = append(result.Results, ci.ImageResult{Ref: target.Ref, Status: "removed", Bytes: image.Size})
		result.ReclaimedBytes += image.Size
	}
	sort.Slice(result.Results, func(i, j int) bool {
		if result.Results[i].Ref != result.Results[j].Ref {
			return result.Results[i].Ref < result.Results[j].Ref
		}
		return result.Results[i].Status < result.Results[j].Status
	})
	return result, nil
}

func findImage(images []ci.ClassifiedImage, target ci.ImageTarget) (ci.ClassifiedImage, bool) {
	normalized := ci.NormalizeImageReference(target.Ref)
	for _, image := range images {
		if image.NormalizedRef() == normalized || image.ID == target.Ref {
			return image, true
		}
	}
	return ci.ClassifiedImage{}, false
}

// pruneCachePolicy enforces the age and cap together, then the cap alone if
// recent unused cache still keeps the host above it. Only unused cache is
// removed, so a build using the cache is safe.
func (w *Worker) pruneCachePolicy(ctx context.Context, policy ci.ImageCachePolicy) (int64, error) {
	if policy.KeepGiB < 1 || policy.OlderThanHours < 1 {
		return 0, errors.New("invalid build-cache policy")
	}
	reclaimed, err := w.docker.PruneBuildCache(ctx, policy.KeepGiB, policy.OlderThanHours)
	if err != nil {
		return reclaimed, err
	}
	size, _, err := w.docker.BuildCache(ctx)
	if err != nil {
		return reclaimed, err
	}
	if size <= policy.CacheSizeBytes() {
		return reclaimed, nil
	}
	more, err := w.docker.PruneBuildCache(ctx, policy.KeepGiB, 0)
	reclaimed += more
	if err != nil {
		return reclaimed, err
	}
	size, _, err = w.docker.BuildCache(ctx)
	if err != nil {
		return reclaimed, err
	}
	if size > policy.CacheSizeBytes() {
		return reclaimed, fmt.Errorf("build cache is still %d bytes; active cache may be in use", size)
	}
	return reclaimed, nil
}

// enforceCache applies the configured build-cache policy automatically. It is
// on unless the operator turned it off; a failure is recorded and retried, and
// never fails a run or blocks claims.
func (w *Worker) enforceCache(ctx context.Context, trigger string) {
	if w.docker == nil || w.cfg.ImageCache.Disabled {
		return
	}
	paused, err := w.ci.Maintenance(ctx)
	if err != nil || paused {
		return
	}
	release, locked, err := hostExecutionLock(w.cfg.WorkspaceRoot, true)
	if err != nil || !locked {
		return
	}
	defer release()
	engine, err := w.docker.EngineID(ctx)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Warn("could not identify the Docker host; build cache not enforced", "error", err)
		}
		return
	}
	policy := ci.ImageCachePolicy{KeepGiB: w.cfg.ImageCache.KeepGiB, OlderThanHours: w.cfg.ImageCache.OlderThanHours()}
	reclaimed, err := w.pruneCachePolicy(ctx, policy)
	cacheError := ""
	if err != nil {
		cacheError = err.Error()
		w.log.Warn("build cache enforcement failed", "trigger", trigger, "error", err)
	}
	previous, recordErr := w.ci.RecordCachePrune(context.WithoutCancel(ctx), engine, reclaimed, cacheError)
	if recordErr != nil {
		w.log.Warn("could not record build cache enforcement", "error", recordErr)
	}
	w.publishInventory(context.WithoutCancel(ctx), true)
	if reclaimed == 0 && (cacheError == "" || cacheError == previous) {
		return
	}
	detail := fmt.Sprintf("automation on %s (%s): cache policy %d GiB, %d hours; reclaimed %d bytes",
		w.cfg.Hostname, engine, policy.KeepGiB, policy.OlderThanHours, reclaimed)
	if cacheError != "" {
		detail += "; error: " + cacheError
	}
	if err := w.ci.RecordImageAudit(context.WithoutCancel(ctx), "", activity.ImageCachePruned, detail); err != nil {
		w.log.Warn("could not audit build cache enforcement", "error", err)
	}
}

// afterRun does the image duties that depend on a run: a fresh inventory when
// the pipeline may have changed images, cache enforcement after Docker was
// used, and the after-deploy image automation.
func (w *Worker) afterRun(ctx context.Context, claim *ci.Claim, outcome ci.Outcome) {
	if w.docker == nil {
		return
	}
	cfg, err := ci.Parse(claim.Pipeline)
	if err != nil {
		return
	}
	if len(cfg.Images) > 0 && (!cfg.Docker || w.cfg.ImageCache.Disabled) {
		w.publishInventory(ctx, false)
	}
	if cfg.Docker {
		w.enforceCache(ctx, "after run")
	}
	if w.cfg.ImageAutomation != config.ImageAutomationAfterDeploy || outcome.Status != ci.StatusPassed {
		return
	}
	deployed := false
	for _, step := range claim.Steps {
		if step.Type == ci.StepDeploy && !step.Skipped {
			deployed = true
			break
		}
	}
	if deployed {
		w.autoPruneBuilt(ctx, "after deploy")
	}
}

// autoPruneBuilt plans and applies a built-image prune from a fresh
// inventory. It is used by the after-deploy and low-disk automations. A stale
// published inventory is never consulted.
func (w *Worker) autoPruneBuilt(ctx context.Context, trigger string) {
	if w.docker == nil {
		return
	}
	paused, err := w.ci.Maintenance(ctx)
	if err != nil || paused {
		return
	}
	release, locked, err := hostExecutionLock(w.cfg.WorkspaceRoot, true)
	if err != nil || !locked {
		return
	}
	defer release()
	engine, err := w.docker.EngineID(ctx)
	if err != nil {
		return
	}
	facts, err := w.ci.ImageFacts(ctx)
	if err != nil {
		w.log.Warn("could not plan automatic image pruning", "trigger", trigger, "error", err)
		return
	}
	images, _, _, err := w.docker.ImageInventory(ctx)
	if err != nil {
		w.log.Warn("could not read images for automatic pruning", "trigger", trigger, "error", err)
		return
	}
	plan := ci.PlanPrune(ci.Classify(images, facts))
	if len(plan.Remove) == 0 {
		return
	}
	targets := make([]ci.ImageTarget, 0, len(plan.Remove))
	for _, image := range plan.Remove {
		targets = append(targets, ci.ImageTarget{Ref: image.Ref(), ID: image.ID})
	}
	result, err := w.applyImageTargets(ctx, targets, true)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Warn("automatic image pruning failed", "trigger", trigger, "error", err)
		}
		return
	}
	w.publishInventory(context.WithoutCancel(ctx), true)
	removed, failed := 0, 0
	for _, image := range result.Results {
		switch image.Status {
		case "removed":
			removed++
		case "failed":
			failed++
		}
	}
	detail := fmt.Sprintf("automation on %s (%s): %s; removed %d image(s), failed %d, reclaimed %d bytes",
		w.cfg.Hostname, engine, trigger, removed, failed, result.ReclaimedBytes)
	if err := w.ci.RecordImageAudit(context.WithoutCancel(ctx), "", activity.ImagesPruned, detail); err != nil {
		w.log.Warn("could not audit automatic image pruning", "error", err)
	}
}

// lowDisk runs the low-disk image automation when free space has fallen below
// the disk reserve plus its margin. It acts at most once per lowDiskInterval.
func (w *Worker) lowDisk(ctx context.Context) bool {
	if w.docker == nil {
		return false
	}
	available, err := availableBytes(w.cfg.WorkspaceRoot)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Warn("could not read free disk space for image automation", "error", err)
		}
		return false
	}
	threshold := uint64(w.cfg.Resources.WithDefaults().DiskReserveGiB)<<30 + lowDiskMargin
	if available >= threshold {
		return false
	}
	now := time.Now().UnixNano()
	last := w.lastLowDisk.Load()
	if last != 0 && now-last < int64(lowDiskInterval) {
		return false
	}
	if !w.lastLowDisk.CompareAndSwap(last, now) {
		return false
	}
	w.log.Info("free disk is below the reserve and margin; pruning built images", "available", available, "threshold", threshold)
	w.autoPruneBuilt(ctx, "low disk")
	return true
}
