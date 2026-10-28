package ci

import (
	"strings"
	"testing"
	"time"
)

func inventoryImage(repository, tag, id string, created time.Time) InventoryImage {
	return InventoryImage{
		Repository: repository, NormalizedRepository: NormalizeImageRepository(repository),
		Tag: tag, ID: id, Size: 10, Created: created,
	}
}

func TestIncompleteImageFactsRefuseBuiltImages(t *testing.T) {
	facts := NewImageFacts()
	facts.Incomplete = true
	facts.Built[NormalizeImageRepository("app")] = BuiltPolicy{Keep: 1}
	classified := Classify([]InventoryImage{inventoryImage("app", "old", "sha256:old", time.Now())}, facts)
	if len(classified) != 1 || classified[0].Class != ImageBuilt || classified[0].Refusal == "" {
		t.Fatalf("classified = %+v, want a refused built image", classified)
	}
	plan := PlanPrune(classified)
	if len(plan.Remove) != 0 || len(plan.Refuse) != 1 {
		t.Fatalf("plan = %+v, want no removal", plan)
	}
}

func TestNormalizeImageRepository(t *testing.T) {
	for ref, want := range map[string]string{
		"alpine":                                   "docker.io/library/alpine",
		"alpine:3.20":                              "docker.io/library/alpine",
		"docker.io/alpine:latest":                  "docker.io/library/alpine",
		"index.docker.io/library/alpine:3.20":      "docker.io/library/alpine",
		"team/app:v1":                              "docker.io/team/app",
		"localhost:5000/app:1":                     "localhost:5000/app",
		"registry.example.com/team/app:1.4.2":      "registry.example.com/team/app",
		"alpine@sha256:" + strings.Repeat("a", 64): "docker.io/library/alpine",
	} {
		if got := NormalizeImageRepository(ref); got != want {
			t.Errorf("NormalizeImageRepository(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestClassifyPrecedenceAndKeep(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	facts := NewImageFacts()
	facts.Required[NormalizeImageReference("runtime:1.0")] = RequiredImage{
		Ref: "runtime:1.0", Sources: []ImageSource{{Repo: "web", Ref: "branch develop"}},
	}
	facts.Built[NormalizeImageRepository("app")] = BuiltPolicy{
		Keep: 2, Sources: []ImageSource{{Repo: "app", Ref: "tag v1"}},
	}
	images := []InventoryImage{
		inventoryImage("app", "v1", "sha256:1", now.Add(-3*time.Hour)),
		inventoryImage("app", "v2", "sha256:2", now.Add(-2*time.Hour)),
		inventoryImage("app", "v3", "sha256:3", now.Add(-time.Hour)),
		inventoryImage("runtime", "1.0", "sha256:c", now.Add(-time.Hour)),
		inventoryImage("other", "1", "sha256:o", now.Add(-time.Hour)),
		{ID: "sha256:d", Size: 5}, // dangling
	}
	classified := Classify(images, facts)
	if len(classified) != len(images) {
		t.Fatalf("classified %d images, want %d", len(classified), len(images))
	}
	want := []ImageClass{ImageBuilt, ImageBuilt, ImageBuilt, ImageRequired, ImageOther, ImageOther}
	for i, c := range classified {
		if c.Class != want[i] {
			t.Errorf("%s class = %s, want %s (reason %q)", c.Ref(), c.Class, want[i], c.Reason)
		}
	}
	if classified[2].Keep != 2 || !classified[2].WithinKeep || !classified[1].WithinKeep {
		t.Errorf("newest two app tags must be within keep: %+v", classified)
	}
	if classified[0].WithinKeep {
		t.Error("the oldest app tag must be outside keep")
	}
	if classified[1].Reason != "built by app tag v1" {
		t.Errorf("built reason = %q", classified[1].Reason)
	}
	if classified[3].Reason != "required by web branch develop" {
		t.Errorf("required reason = %q", classified[3].Reason)
	}
}

func TestClassifyRequiredMatchesImplicitLatestAndDigests(t *testing.T) {
	facts := NewImageFacts()
	facts.Required[NormalizeImageReference("alpine")] = RequiredImage{Ref: "alpine"}
	facts.Required["docker.io/library/debian@sha256:"+strings.Repeat("a", 64)] = RequiredImage{Ref: "debian@sha256:" + strings.Repeat("a", 64)}
	images := []InventoryImage{
		inventoryImage("alpine", "latest", "sha256:1", time.Now()),
		{Repository: "debian", NormalizedRepository: "docker.io/library/debian", Tag: "12", ID: "sha256:2", Digests: []string{"docker.io/library/debian@sha256:" + strings.Repeat("a", 64)}},
	}
	classified := Classify(images, facts)
	for i := range classified {
		if classified[i].Class != ImageRequired {
			t.Errorf("%s class = %s, want required", classified[i].Ref(), classified[i].Class)
		}
	}
}

func TestClassifyDeployedVersionAndFallback(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	facts := NewImageFacts()
	facts.Built[NormalizeImageRepository("app")] = BuiltPolicy{
		Keep: 1, Deployments: []string{"v2"},
	}
	images := []InventoryImage{
		inventoryImage("app", "v1", "sha256:1", now.Add(-2*time.Hour)),
		inventoryImage("app", "v2", "sha256:2", now.Add(-time.Hour)),
	}
	classified := Classify(images, facts)
	if !classified[1].Deployed || classified[0].Deployed {
		t.Errorf("deployed = %+v, want only v2", classified)
	}
	plan := PlanPrune(classified)
	if len(plan.Remove) != 1 || plan.Remove[0].Tag != "v1" {
		t.Fatalf("plan remove = %+v, want v1 only", plan.Remove)
	}
	if reason := plan.Refuse[0].Refusal; reason != "currently deployed" {
		t.Errorf("v2 refusal = %q", reason)
	}

	// A recorded version with no live tag cannot identify the live image;
	// the newest tag must be protected.
	facts.Built[NormalizeImageRepository("app")] = BuiltPolicy{Keep: 1, Deployments: []string{"v9"}}
	classified = Classify(images, facts)
	if !classified[1].Deployed || !classified[1].DeployedFallback {
		t.Fatalf("fallback deployment = %+v, want newest tag protected", classified[1])
	}
	if !classified[0].Deployed && classified[0].WithinKeep {
		t.Fatal("test fixture must put v1 outside keep")
	}
}

func TestClassifyKeepTiesAreDeterministic(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	facts := NewImageFacts()
	facts.Built[NormalizeImageRepository("app")] = BuiltPolicy{Keep: 1}
	images := []InventoryImage{
		inventoryImage("app", "alpha", "sha256:a", at),
		inventoryImage("app", "beta", "sha256:b", at),
	}
	classified := Classify(images, facts)
	if classified[0].WithinKeep == classified[1].WithinKeep {
		t.Fatalf("exactly one of the tied tags must be within keep: %+v", classified)
	}
	if !classified[1].WithinKeep {
		t.Errorf("the tag ordering must be deterministic; got %+v", classified)
	}
}

func TestPlanPruneRefusalsAndOrdering(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	facts := NewImageFacts()
	facts.Required[NormalizeImageReference("runtime:1.0")] = RequiredImage{Ref: "runtime:1.0", Sources: []ImageSource{{Repo: "web"}}}
	facts.Built[NormalizeImageRepository("app")] = BuiltPolicy{Keep: 1}
	images := []InventoryImage{
		inventoryImage("app", "new", "sha256:new", now),
		inventoryImage("app", "old", "sha256:old", now.Add(-time.Hour)),
		inventoryImage("runtime", "1.0", "sha256:req", now.Add(-time.Hour)),
		inventoryImage("other", "1", "sha256:other", now.Add(-time.Hour)),
	}
	inUse := inventoryImage("app", "used", "sha256:used", now.Add(-2*time.Hour))
	inUse.InUse = true
	images = append(images, inUse)
	classified := Classify(images, facts)
	plan := PlanPrune(classified)
	if len(plan.Remove) != 1 || plan.Remove[0].Ref() != "app:old" {
		t.Fatalf("remove = %+v, want app:old only", plan.Remove)
	}
	reasons := map[string]string{}
	for _, image := range plan.Refuse {
		reasons[image.Ref()] = image.Refusal
	}
	for ref, want := range map[string]string{
		"app:new":     "within the newest 1 tags",
		"app:used":    "in use by a container",
		"runtime:1.0": "required by web",
		"other:1":     "not declared in any pipeline's images",
	} {
		if reasons[ref] != want {
			t.Errorf("%s refusal = %q, want %q", ref, reasons[ref], want)
		}
	}
	if plan.ReclaimableBytes != 10 {
		t.Errorf("reclaimable = %d, want 10", plan.ReclaimableBytes)
	}
}

func TestPlanPruneRefusesDanglingImages(t *testing.T) {
	plan := PlanPrune(Classify([]InventoryImage{{ID: "sha256:d", Size: 7}}, NewImageFacts()))
	if len(plan.Remove) != 0 || len(plan.Refuse) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.Refuse[0].Refusal != "dangling image; remove it explicitly" {
		t.Errorf("refusal = %q", plan.Refuse[0].Refusal)
	}
	if ok, _ := PlanRemoveOne(plan.Refuse[0]); !ok {
		t.Error("an explicit removal must still be allowed for a dangling image")
	}
}

func TestPlanPruneEmpty(t *testing.T) {
	plan := PlanPrune(nil)
	if len(plan.Remove) != 0 || len(plan.Refuse) != 0 || plan.ReclaimableBytes != 0 {
		t.Fatalf("empty plan = %+v", plan)
	}
}

func TestPlanRemoveOneProtectsRequiredAndInUse(t *testing.T) {
	required := ClassifiedImage{Class: ImageRequired, Reason: "required by x"}
	if ok, reason := PlanRemoveOne(required); ok || reason == "" {
		t.Errorf("required removal = %v, %q", ok, reason)
	}
	inUse := ClassifiedImage{Class: ImageBuilt, InUse: true}
	if ok, reason := PlanRemoveOne(inUse); ok || reason != "in use by a container" {
		t.Errorf("in-use removal = %v, %q", ok, reason)
	}
	built := ClassifiedImage{Class: ImageBuilt}
	if ok, reason := PlanRemoveOne(built); !ok || reason != "" {
		t.Errorf("explicit built removal = %v, %q", ok, reason)
	}
}

func TestPlanPruneMultipleTagsOnOneImageID(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	facts := NewImageFacts()
	facts.Built[NormalizeImageRepository("app")] = BuiltPolicy{Keep: 1}
	images := []InventoryImage{
		inventoryImage("app", "new", "sha256:shared", at),
		inventoryImage("app", "old", "sha256:shared", at.Add(-time.Hour)),
	}
	plan := PlanPrune(Classify(images, facts))
	if len(plan.Remove) != 1 || plan.Remove[0].Tag != "old" {
		t.Fatalf("plan = %+v, want only the old tag", plan.Remove)
	}
	if plan.Remove[0].ID != "sha256:shared" {
		t.Errorf("removal ID = %q, want the shared image ID retained for revalidation", plan.Remove[0].ID)
	}
}
