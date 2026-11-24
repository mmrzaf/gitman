package web

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/mmrzaf/gitman/internal/ci"
)

// attentionItem is one line of what needs an operator, with where to act.
type attentionItem struct {
	Icon string
	Text string
	URL  string
}

// imageGroup is one class of a host's images, as a collapsible table.
type imageGroup struct {
	Kind   string // required, built or other
	Title  string
	Images []ci.ClassifiedImage
}

// runnerHost is one Docker host: its worker processes, build cache and images.
type runnerHost struct {
	ci.ImageHost

	Procs []ci.WorkerStatus
	Ready int
	// Stopped counts workers that shut down cleanly; they are history, not
	// something to act on.
	Stopped int
	Groups  []imageGroup
	// CacheCap and CacheUsed are bytes for the cache meter; CacheCap is zero
	// when enforcement is off.
	CacheCap  int64
	CacheUsed int64
}

// workersPage is the single administration page for execution: workers,
// the Docker hosts they run on, the images those hosts hold, and why runs wait.
type workersPage struct {
	Hosts     []runnerHost
	Unbound   []ci.WorkerStatus
	Attention []attentionItem
	Plan      *ci.ImageHost
	Ops       []ci.ImageOperation

	Workers, ReadyWorkers int
	Queued, Blocked       int
	Missing               int
	CacheUsed, CacheCap   int64
}

func (a *App) workersView(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	workers, err := a.ci.Workers(ctx)
	if err != nil {
		return err
	}
	hosts, err := a.ci.ImageHosts(ctx)
	if err != nil {
		return err
	}
	queued, err := a.ci.QueuedCount(ctx)
	if err != nil {
		return err
	}
	blocks, err := a.ci.QueuedBlockGroups(ctx)
	if err != nil {
		return err
	}
	ops, err := a.ci.ImageOperations(ctx, 5)
	if err != nil {
		return err
	}

	page := workersPage{Queued: queued, Ops: ops}
	byEngine := map[string][]ci.WorkerStatus{}
	stoppedByEngine := map[string]int{}
	for _, worker := range workers {
		if worker.Stopped != nil {
			stoppedByEngine[worker.EngineID]++
			continue
		}
		page.Workers++
		if worker.Ready {
			page.ReadyWorkers++
		}
		byEngine[worker.EngineID] = append(byEngine[worker.EngineID], worker)
	}

	planEngine := r.URL.Query().Get("plan")
	for _, host := range hosts {
		view := runnerHost{ImageHost: host, Procs: byEngine[host.EngineID], Stopped: stoppedByEngine[host.EngineID]}
		delete(byEngine, host.EngineID)
		for _, worker := range view.Procs {
			if worker.Ready {
				view.Ready++
			}
		}
		groups := map[string]*imageGroup{
			"required": {Kind: "required", Title: "Required by pipelines"},
			"built":    {Kind: "built", Title: "Built by pipelines"},
			"other":    {Kind: "other", Title: "Other images"},
		}
		for _, image := range host.Images {
			switch {
			case image.IsRequired():
				groups["required"].Images = append(groups["required"].Images, image)
			case image.IsBuilt():
				groups["built"].Images = append(groups["built"].Images, image)
			case image.IsOther():
				groups["other"].Images = append(groups["other"].Images, image)
			}
		}
		for _, kind := range []string{"required", "built", "other"} {
			view.Groups = append(view.Groups, *groups[kind])
		}
		if host.HasInventory && !host.Cache.Disabled {
			view.CacheUsed = host.Cache.Size
			view.CacheCap = host.Cache.Policy.CacheSizeBytes()
			page.CacheUsed += view.CacheUsed
			page.CacheCap += view.CacheCap
		}
		page.Missing += len(host.Missing)
		page.Hosts = append(page.Hosts, view)
		if planEngine != "" && host.EngineID == planEngine {
			plan := host
			page.Plan = &plan
		}
	}
	for _, rest := range byEngine {
		page.Unbound = append(page.Unbound, rest...)
	}
	sort.Slice(page.Unbound, func(i, j int) bool { return page.Unbound[i].Hostname < page.Unbound[j].Hostname })

	page.Attention = a.runnerAttention(&page, blocks)
	a.render(w, r, http.StatusOK, "workers", "Workers", page)
	return nil
}

// runnerAttention lists, most urgent first, what keeps runs from starting or
// a host from staying healthy.
func (a *App) runnerAttention(page *workersPage, blocks []ci.BlockGroup) []attentionItem {
	var items []attentionItem
	sort.Slice(blocks, func(i, j int) bool { return blocks[i].Count > blocks[j].Count })
	for _, group := range blocks {
		page.Blocked += group.Count
		items = append(items, attentionItem{Icon: "queued", Text: fmt.Sprintf("%d queued run%s: %s", group.Count, plural(group.Count), group.Blocker.Message()), URL: fmt.Sprintf("/%s/runs/%d", group.RepoName, group.Number)})
	}
	if page.Workers > 0 && page.ReadyWorkers == 0 && page.Blocked == 0 {
		items = append(items, attentionItem{Icon: "warning", Text: "No worker is ready."})
	}
	for _, host := range page.Hosts {
		anchor := "#host-" + host.EngineID
		// A stale error must not show while enforcement is off.
		if !host.Cache.Disabled {
			switch {
			case host.Cache.Error != "":
				items = append(items, attentionItem{Icon: "warning", Text: host.Hostname + ": build cache enforcement failed: " + host.Cache.Error, URL: anchor})
			case host.Cache.SharedFloor():
				items = append(items, attentionItem{Icon: "warning", Text: host.Hostname + ": build cache is above its cap but only " + formatBytes(host.Cache.Reclaimable) + " is reclaimable.", URL: anchor})
			case host.Cache.AboveCap:
				items = append(items, attentionItem{Icon: "warning", Text: host.Hostname + ": build cache is above its cap.", URL: anchor})
			}
		}
		if host.FactsIncomplete {
			items = append(items, attentionItem{Icon: "warning", Text: host.Hostname + ": image facts are incomplete, so image removal is disabled.", URL: anchor})
		}
		if host.HasInventory && host.Stale {
			items = append(items, attentionItem{Icon: "warning", Text: host.Hostname + ": image inventory is stale; no worker has refreshed it.", URL: anchor})
		}
	}
	return items
}
