package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/config"
)

func adminImagesList(ctx context.Context, env *adminEnv, args []string) error {
	fs := flag.NewFlagSet("images list", flag.ContinueOnError)
	worker := fs.String("worker", "", "show only this worker's Docker host")
	class := fs.String("class", "", "required, built or other")
	asJSON := fs.Bool("json", false, "")
	if _, err := parseArgs(fs, args, 0, "gitman admin images list [--worker ID] [--class required|built|other] [--json]"); err != nil {
		return err
	}
	if *class != "" && *class != string(ci.ImageRequired) && *class != string(ci.ImageBuilt) && *class != string(ci.ImageOther) {
		return fmt.Errorf("--class must be required, built or other")
	}
	hosts, err := imageHostsFor(ctx, env, *worker)
	if err != nil {
		return err
	}
	if *class != "" {
		filtered := make([]ci.ImageHost, len(hosts))
		for i, host := range hosts {
			filtered[i] = host
			filtered[i].Images = nil
			for _, image := range host.Images {
				if image.Class == ci.ImageClass(*class) {
					filtered[i].Images = append(filtered[i].Images, image)
				}
			}
		}
		hosts = filtered
	}
	if *asJSON {
		return json.NewEncoder(env.out).Encode(hosts)
	}
	if len(hosts) == 0 {
		fmt.Fprintln(env.out, "No Docker hosts have published an image inventory.")
		return nil
	}
	for _, host := range hosts {
		fmt.Fprintf(env.out, "HOST %s (%s)%s\n", host.Hostname, host.EngineID, imageHostState(host))
		if !host.HasInventory {
			fmt.Fprintln(env.out, "  No inventory published yet.")
			continue
		}
		printImageTable(env, host.Images)
		fmt.Fprintf(env.out, "%d images", len(host.Images))
		if len(host.Plan.Remove) > 0 {
			fmt.Fprintf(env.out, ", %d prunable, estimated %s reclaimable", len(host.Plan.Remove), formatSize(host.Plan.ReclaimableBytes))
		} else {
			fmt.Fprint(env.out, ", none prunable")
		}
		fmt.Fprintf(env.out, "; build cache %s (%s reclaimable)", formatSize(host.Cache.Size), formatSize(host.Cache.Reclaimable))
		if !host.Cache.Disabled {
			fmt.Fprintf(env.out, "; policy %d GiB, %d hours", host.Cache.Policy.KeepGiB, host.Cache.Policy.OlderThanHours)
		}
		if host.Cache.PrunedAt != nil {
			fmt.Fprintf(env.out, "; last prune %s", host.Cache.PrunedAt.Format(time.RFC3339))
		}
		if host.Cache.Reclaimed > 0 {
			fmt.Fprintf(env.out, "; reclaimed %s", formatSize(host.Cache.Reclaimed))
		}
		if host.Cache.Error != "" {
			fmt.Fprintf(env.out, "; error: %s", host.Cache.Error)
		}
		if host.FactsIncomplete {
			fmt.Fprint(env.out, "; image facts incomplete")
		}
		fmt.Fprintln(env.out)
		fmt.Fprintln(env.out)
	}
	return nil
}

func printImageTable(env *adminEnv, images []ci.ClassifiedImage) {
	tw := tabwriter.NewWriter(env.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "  IMAGE\tCLASS\tSIZE\tAGE\tIN USE\tDEPLOYED\tPRUNABLE\tREASON")
	for _, image := range images {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			image.Ref(), image.Class, formatSize(image.Size), imageAge(image.Created),
			yesNo(image.InUse), yesNo(image.Deployed), imagePrunable(image), imageDetail(image))
	}
	_ = tw.Flush()
}

func imagePrunable(image ci.ClassifiedImage) string {
	if image.Prunable {
		return "yes"
	}
	return "no"
}

func imageDetail(image ci.ClassifiedImage) string {
	if image.Refusal != "" {
		return image.Refusal
	}
	return image.Reason
}

func adminImagesMissing(ctx context.Context, env *adminEnv, args []string) error {
	if _, err := parseArgs(flag.NewFlagSet("images missing", flag.ContinueOnError), args, 0, "gitman admin images missing"); err != nil {
		return err
	}
	hosts, err := env.ci.ImageHosts(ctx)
	if err != nil {
		return err
	}
	type row struct {
		worker, image, sources string
	}
	var rows []row
	incomplete := false
	for _, host := range hosts {
		if host.FactsIncomplete {
			incomplete = true
		}
		for _, missing := range host.Missing {
			for _, workerID := range missing.Workers {
				rows = append(rows, row{worker: host.Hostname + " (" + workerID + ")", image: missing.Ref, sources: imageSources(missing.Sources)})
			}
		}
	}
	if len(rows) == 0 {
		if incomplete {
			fmt.Fprintln(env.out, "Image facts are incomplete; the missing-image list cannot be trusted.")
			return fmt.Errorf("image facts are incomplete")
		}
		fmt.Fprintln(env.out, "No required images are missing from ready workers.")
		return nil
	}
	tw := tabwriter.NewWriter(env.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "WORKER\tIMAGE\tREQUIRED BY")
	for _, row := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", row.worker, row.image, row.sources)
	}
	_ = tw.Flush()
	if incomplete {
		fmt.Fprintln(env.out, "Image facts are incomplete; this list may miss required images.")
	}
	return fmt.Errorf("required images are missing")
}

func imageSources(sources []ci.ImageSource) string {
	labels := make([]string, len(sources))
	for i, source := range sources {
		labels[i] = source.String()
	}
	return strings.Join(labels, ", ")
}

func adminImagesPrune(ctx context.Context, env *adminEnv, args []string) error {
	fs := flag.NewFlagSet("images prune", flag.ContinueOnError)
	worker := fs.String("worker", "", "prune only this worker's Docker host")
	confirm := fs.Bool("confirm", false, "queue the planned work")
	cache := fs.Bool("cache", false, "prune the build cache instead of built images")
	keep := fs.Int("keep-storage", 0, "cache cap in GiB, with --cache")
	older := fs.Int("older-than", 0, "oldest cache entry to keep, in hours, with --cache")
	if _, err := parseArgs(fs, args, 0, "gitman admin images prune [--worker ID] [--confirm] [--cache] [--keep-storage GiB] [--older-than HOURS]"); err != nil {
		return err
	}
	if !*cache && (*keep != 0 || *older != 0) {
		return fmt.Errorf("--keep-storage and --older-than are only valid with --cache")
	}
	if *keep < 0 || *older < 0 {
		return fmt.Errorf("--keep-storage and --older-than must be positive")
	}
	hosts, err := imageHostsFor(ctx, env, *worker)
	if err != nil {
		return err
	}
	if len(hosts) == 0 {
		return fmt.Errorf("no Docker hosts have published an image inventory")
	}
	if *cache {
		return pruneCache(ctx, env, hosts, *confirm, *keep, *older)
	}
	for _, host := range hosts {
		if !host.HasInventory {
			if *confirm {
				return fmt.Errorf("%s has no image inventory yet; try again after the worker refreshes it", host.Hostname)
			}
			fmt.Fprintf(env.out, "HOST %s: no inventory published yet.\n\n", host.Hostname)
			continue
		}
		if host.Stale {
			return fmt.Errorf("the image inventory for %s is stale; try again after the worker refreshes it", host.Hostname)
		}
		fmt.Fprintf(env.out, "HOST %s (%s)\n", host.Hostname, host.EngineID)
		if len(host.Plan.Remove) == 0 {
			fmt.Fprintln(env.out, "  No built images are prunable.")
		} else {
			fmt.Fprintf(env.out, "  Would remove %d image(s), estimated %s reclaimable:\n", len(host.Plan.Remove), formatSize(host.Plan.ReclaimableBytes))
			for _, image := range host.Plan.Remove {
				fmt.Fprintf(env.out, "    %s (%s)\n", image.Ref(), formatSize(image.Size))
			}
		}
		for _, image := range host.Plan.Refuse {
			fmt.Fprintf(env.out, "  Refused %s: %s\n", image.Ref(), image.Refusal)
		}
		fmt.Fprintln(env.out)
		if !*confirm {
			continue
		}
		id, plan, err := env.ci.QueueImagePrune(ctx, host.EngineID, "")
		if err != nil {
			return err
		}
		if id == "" {
			fmt.Fprintf(env.out, "Nothing to queue on %s.\n", host.Hostname)
			continue
		}
		fmt.Fprintf(env.out, "Queued pruning %d image(s) on %s as operation %s.\n", len(plan.Remove), host.Hostname, id)
		printOperationResult(ctx, env, id)
	}
	return nil
}

func pruneCache(ctx context.Context, env *adminEnv, hosts []ci.ImageHost, confirm bool, keep, older int) error {
	for _, host := range hosts {
		if !host.HasInventory {
			if confirm {
				return fmt.Errorf("%s has no image inventory yet; try again after the worker refreshes it", host.Hostname)
			}
			fmt.Fprintf(env.out, "HOST %s: no inventory published yet.\n\n", host.Hostname)
			continue
		}
		if host.Stale {
			return fmt.Errorf("the image inventory for %s is stale; try again after the worker refreshes it", host.Hostname)
		}
		policy := host.Cache.Policy
		if policy.KeepGiB == 0 {
			defaults := config.DefaultImageCache()
			policy = ci.ImageCachePolicy{KeepGiB: defaults.KeepGiB, OlderThanHours: defaults.OlderThanHours()}
		}
		if keep > 0 {
			policy.KeepGiB = keep
		}
		if older > 0 {
			policy.OlderThanHours = older
		}
		if policy.KeepGiB < 1 || policy.KeepGiB > config.MaxImageCacheKeepGiB {
			return fmt.Errorf("--keep-storage must be between 1 and %d", config.MaxImageCacheKeepGiB)
		}
		if policy.OlderThanHours < 1 || policy.OlderThanHours > config.MaxImageCacheMaxAgeHours {
			return fmt.Errorf("--older-than must be between 1 and %d", config.MaxImageCacheMaxAgeHours)
		}
		fmt.Fprintf(env.out, "HOST %s (%s)\n  Cache %s (%s reclaimable); policy %d GiB, %d hours.\n",
			host.Hostname, host.EngineID, formatSize(host.Cache.Size), formatSize(host.Cache.Reclaimable), policy.KeepGiB, policy.OlderThanHours)
		if host.Cache.Error != "" {
			fmt.Fprintf(env.out, "  Last error: %s\n", host.Cache.Error)
		}
		fmt.Fprintln(env.out)
		if !confirm {
			continue
		}
		id, err := env.ci.QueueImageCachePrune(ctx, host.EngineID, "", &policy)
		if err != nil {
			return err
		}
		fmt.Fprintf(env.out, "Queued a cache prune on %s as operation %s.\n", host.Hostname, id)
		printOperationResult(ctx, env, id)
	}
	return nil
}

func adminImagesRemove(ctx context.Context, env *adminEnv, args []string) error {
	fs := flag.NewFlagSet("images remove", flag.ContinueOnError)
	confirm := fs.Bool("confirm", false, "queue the removal")
	pos, err := parseArgs(fs, args, 2, "gitman admin images remove [--confirm] <worker-id> <image>")
	if err != nil {
		return err
	}
	host, err := imageHostForWorker(ctx, env, pos[0])
	if err != nil {
		return err
	}
	if !host.HasInventory || host.Stale {
		return fmt.Errorf("the image inventory for %s is stale; try again after the worker refreshes it", host.Hostname)
	}
	if host.FactsIncomplete {
		return fmt.Errorf("image facts are incomplete; no image is removed until every stored pipeline can be read")
	}
	var found *ci.ClassifiedImage
	for i := range host.Images {
		image := &host.Images[i]
		if image.Ref() == pos[1] || image.NormalizedRef() == ci.NormalizeImageReference(pos[1]) || image.ID == pos[1] {
			found = image
			break
		}
	}
	if found == nil {
		return fmt.Errorf("there is no image %q in that host's inventory", pos[1])
	}
	if ok, reason := ci.PlanRemoveOne(*found); !ok {
		return fmt.Errorf("refusing to remove %s: %s", found.Ref(), reason)
	}
	if !*confirm {
		fmt.Fprintf(env.out, "Would remove %s (%s).\n", found.Ref(), formatSize(found.Size))
		return nil
	}
	id, err := env.ci.QueueImageRemove(ctx, host.EngineID, pos[1], "")
	if err != nil {
		return err
	}
	fmt.Fprintf(env.out, "Queued removal of %s as operation %s.\n", found.Ref(), id)
	printOperationResult(ctx, env, id)
	return nil
}

func imageHostsFor(ctx context.Context, env *adminEnv, workerID string) ([]ci.ImageHost, error) {
	if workerID == "" {
		return env.ci.ImageHosts(ctx)
	}
	host, err := imageHostForWorker(ctx, env, workerID)
	if err != nil {
		return nil, err
	}
	return []ci.ImageHost{*host}, nil
}

func imageHostForWorker(ctx context.Context, env *adminEnv, workerID string) (*ci.ImageHost, error) {
	workers, err := env.ci.Workers(ctx)
	if err != nil {
		return nil, err
	}
	engine := ""
	for _, worker := range workers {
		if worker.ID == workerID {
			engine = worker.EngineID
			break
		}
	}
	if engine == "" {
		return nil, fmt.Errorf("worker %q has not bound to a Docker host yet", workerID)
	}
	hosts, err := env.ci.ImageHosts(ctx)
	if err != nil {
		return nil, err
	}
	for i := range hosts {
		if hosts[i].EngineID == engine {
			return &hosts[i], nil
		}
	}
	return nil, fmt.Errorf("worker %q has no image inventory yet", workerID)
}

func printOperationResult(ctx context.Context, env *adminEnv, id string) {
	if env.imageOperationWait <= 0 {
		return
	}
	operation, err := waitForImageOperation(ctx, env, id, env.imageOperationWait)
	if err != nil {
		fmt.Fprintf(env.out, "Operation %s is still pending: %v\n", id, err)
		return
	}
	printImageOperation(env, operation)
}

func waitForImageOperation(ctx context.Context, env *adminEnv, id string, timeout time.Duration) (*ci.ImageOperation, error) {
	deadline := time.Now().Add(timeout)
	for {
		operation, err := env.ci.ImageOperation(ctx, id)
		if err != nil {
			return nil, err
		}
		if !operation.Pending() {
			return operation, nil
		}
		if time.Now().After(deadline) {
			return nil, errors.New("no worker claimed it before the wait ended")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func printImageOperation(env *adminEnv, operation *ci.ImageOperation) {
	fmt.Fprintf(env.out, "Operation %s: %s, reclaimed %s.\n", operation.ID, operation.Kind, formatSize(operation.ReclaimedBytes))
	for _, result := range operation.Results {
		line := fmt.Sprintf("  %s: %s", result.Ref, result.Status)
		if result.Reason != "" {
			line += " (" + result.Reason + ")"
		}
		fmt.Fprintln(env.out, line)
	}
	if operation.Error != "" {
		fmt.Fprintf(env.out, "  Error: %s\n", operation.Error)
	}
}

func imageHostState(host ci.ImageHost) string {
	switch {
	case !host.HasInventory:
		return " — no inventory yet"
	case host.Stale:
		return " — stale inventory"
	default:
		return ""
	}
}

func imageAge(created time.Time) string {
	if created.IsZero() {
		return "-"
	}
	age := time.Since(created)
	switch {
	case age < time.Minute:
		return "now"
	case age < time.Hour:
		return fmt.Sprintf("%dm", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh", int(age.Hours()))
	default:
		return fmt.Sprintf("%dd", int(age.Hours()/24))
	}
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
