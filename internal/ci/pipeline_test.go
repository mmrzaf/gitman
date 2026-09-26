package ci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/git"
)

func TestWhenShouldRun(t *testing.T) {
	cases := []struct {
		name       string
		when       When
		refKind    git.Kind
		targetName string
		want       bool
	}{
		{"always runs for a branch", When{Kind: WhenAlways}, git.KindBranch, "", true},
		{"always runs for a tag with a target", When{Kind: WhenAlways}, git.KindTag, "production", true},
		{"branch matches a branch ref", When{Kind: WhenBranch}, git.KindBranch, "", true},
		{"branch does not match a tag ref", When{Kind: WhenBranch}, git.KindTag, "production", false},
		{"tag matches a tag ref", When{Kind: WhenTag}, git.KindTag, "production", true},
		{"tag does not match a branch ref", When{Kind: WhenTag}, git.KindBranch, "", false},
		{"target matches when any target matched", When{Kind: WhenTarget}, git.KindBranch, "staging", true},
		{"target does not match when no target matched", When{Kind: WhenTarget}, git.KindBranch, "", false},
		{"named matches its own target", When{Kind: WhenNamed, Name: "production"}, git.KindTag, "production", true},
		{"named does not match a different target", When{Kind: WhenNamed, Name: "production"}, git.KindBranch, "staging", false},
		{"named does not match no target", When{Kind: WhenNamed, Name: "production"}, git.KindTag, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.when.ShouldRun(c.refKind, c.targetName); got != c.want {
				t.Errorf("ShouldRun(%v, %q) = %v, want %v", c.refKind, c.targetName, got, c.want)
			}
		})
	}
}

func TestResolveTargetExactBeatsWildcard(t *testing.T) {
	cfg := &Config{Targets: map[string]Target{
		"any":        {Kind: git.KindBranch, Pattern: "*"},
		"production": {Kind: git.KindBranch, Pattern: "main"},
	}}
	name, ok := cfg.ResolveTarget(git.KindBranch, "main")
	if !ok || name != "production" {
		t.Errorf("ResolveTarget = %q, %v, want \"production\", true", name, ok)
	}
}

func TestResolveTargetNoMatch(t *testing.T) {
	cfg := &Config{Targets: map[string]Target{
		"production": {Kind: git.KindTag, Pattern: "v*"},
	}}
	if _, ok := cfg.ResolveTarget(git.KindBranch, "develop"); ok {
		t.Error("expected no target to match")
	}
}

func TestResolveTargetKindIsolation(t *testing.T) {
	cfg := &Config{Targets: map[string]Target{
		"production": {Kind: git.KindTag, Pattern: "*"},
	}}
	if _, ok := cfg.ResolveTarget(git.KindBranch, "anything"); ok {
		t.Error("a tag target must not match a branch")
	}
}

func TestRunContextVersionFromTag(t *testing.T) {
	rc := RunContext{RefKind: git.KindTag, RefName: "v1.4.2", Commit: "3f2a91cb1de00000000000000000000000000000"}
	if got := rc.Version(); got != "v1.4.2" {
		t.Errorf("Version() = %q", got)
	}
}

func TestRunContextVersionSanitizesTag(t *testing.T) {
	rc := RunContext{RefKind: git.KindTag, RefName: "  weird tag!! ", Commit: "3f2a91cb1de00000000000000000000000000000"}
	got := rc.Version()
	if got == "" {
		t.Fatal("expected a non-empty sanitized version")
	}
	for _, r := range got {
		if r != '.' && r != '-' && r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			t.Fatalf("Version() = %q contains an unsafe character %q", got, r)
		}
	}
}

func TestRunContextVersionFallsBackToShortCommit(t *testing.T) {
	rc := RunContext{RefKind: git.KindBranch, RefName: "develop", Commit: "3f2a91cb1de00000000000000000000000000000"}
	if got := rc.Version(); got != "3f2a91cb1de0" {
		t.Errorf("Version() = %q, want first 12 characters of the commit", got)
	}
}

func TestRunContextVersionFallsBackWhenTagSanitizesToEmpty(t *testing.T) {
	rc := RunContext{RefKind: git.KindTag, RefName: "...", Commit: "3f2a91cb1de00000000000000000000000000000"}
	if got := rc.Version(); got != "3f2a91cb1de0" {
		t.Errorf("Version() = %q, want the commit fallback when the tag sanitizes to nothing", got)
	}
}

func TestRunContextVariables(t *testing.T) {
	rc := RunContext{
		Repo: "waiotech", RunNumber: 142, Commit: "3f2a91cb1de00000000000000000000000000000",
		RefKind: git.KindBranch, RefName: "develop", Target: "staging",
	}
	vars := rc.Variables()
	want := map[string]string{
		"GITMAN_REPO":     "waiotech",
		"GITMAN_RUN":      "142",
		"GITMAN_COMMIT":   "3f2a91cb1de00000000000000000000000000000",
		"GITMAN_SHORT":    "3f2a91c",
		"GITMAN_REF":      "develop",
		"GITMAN_REF_KIND": "branch",
		"GITMAN_VERSION":  "3f2a91cb1de0",
		"GITMAN_TARGET":   "staging",
	}
	for k, v := range want {
		if vars[k] != v {
			t.Errorf("Variables()[%q] = %q, want %q", k, vars[k], v)
		}
	}
	if _, ok := vars["GITMAN_SUMMARY"]; ok {
		t.Error("GITMAN_SUMMARY must not come from Variables(); it is a worker-assigned path")
	}
}

func TestResolvedEnvLayering(t *testing.T) {
	cfg := &Config{
		Env: map[string]string{"DEBIAN_MIRROR": "http://shared/debian", "SHARED_ONLY": "shared"},
		Targets: map[string]Target{
			"staging": {
				Kind: git.KindBranch, Pattern: "develop",
				Env: map[string]string{"DEBIAN_MIRROR": "http://staging/debian", "STAGING_ONLY": "staging"},
			},
		},
	}
	rc := RunContext{Repo: "x", RefKind: git.KindBranch, RefName: "develop", Target: "staging", Commit: "abc1234"}

	env := cfg.ResolvedEnv(rc)
	if env["GITMAN_TARGET"] != "staging" {
		t.Errorf("built-in GITMAN_TARGET = %q", env["GITMAN_TARGET"])
	}
	if env["DEBIAN_MIRROR"] != "http://staging/debian" {
		t.Errorf("expected the target's env to override the shared env, got %q", env["DEBIAN_MIRROR"])
	}
	if env["SHARED_ONLY"] != "shared" {
		t.Errorf("expected the shared-only key to survive, got %q", env["SHARED_ONLY"])
	}
	if env["STAGING_ONLY"] != "staging" {
		t.Errorf("expected the target-only key to survive, got %q", env["STAGING_ONLY"])
	}
}

func TestResolvedEnvNoTarget(t *testing.T) {
	cfg := &Config{Env: map[string]string{"SHARED_ONLY": "shared"}}
	rc := RunContext{Repo: "x", RefKind: git.KindBranch, RefName: "feature/x", Commit: "abc1234"}
	env := cfg.ResolvedEnv(rc)
	if env["GITMAN_TARGET"] != "" {
		t.Errorf("expected GITMAN_TARGET to be empty, got %q", env["GITMAN_TARGET"])
	}
	if env["SHARED_ONLY"] != "shared" {
		t.Errorf("expected the shared env to still apply, got %q", env["SHARED_ONLY"])
	}
}

func mustParse(t *testing.T, yaml string) *Config {
	t.Helper()
	cfg, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	return cfg
}

func wantParseError(t *testing.T, yaml string, contains string) {
	t.Helper()
	_, err := Parse([]byte(yaml))
	if err == nil {
		t.Fatalf("Parse: expected an error containing %q, got nil", contains)
	}
	if !strings.Contains(err.Error(), contains) {
		t.Fatalf("Parse error = %q, want it to contain %q", err.Error(), contains)
	}
}

const minimalValid = `
image: docker:29-cli
steps:
  - name: build
    run: echo hi
`

func TestParseMinimalValid(t *testing.T) {
	cfg := mustParse(t, minimalValid)
	if cfg.Image != "docker:29-cli" {
		t.Errorf("Image = %q", cfg.Image)
	}
	if cfg.Docker {
		t.Error("expected Docker to default to false")
	}
	if cfg.Timeout != 0 {
		t.Errorf("expected Timeout to default to zero, got %v", cfg.Timeout)
	}
	if len(cfg.Steps) != 1 || cfg.Steps[0].Name != "build" {
		t.Fatalf("Steps = %+v", cfg.Steps)
	}
	if cfg.Steps[0].When.Kind != WhenAlways {
		t.Errorf("expected default When to be WhenAlways, got %v", cfg.Steps[0].When)
	}
}

func TestParseEmptyFile(t *testing.T) {
	wantParseError(t, "", "empty")
}

func TestParseRejectsUnknownTopLevelKey(t *testing.T) {
	wantParseError(t, minimalValid+"\nunknown_key: 1\n", "unknown_key")
}

func TestParseRejectsUnknownStepKey(t *testing.T) {
	wantParseError(t, `
image: docker:29-cli
steps:
  - name: build
    run: echo hi
    bogus: 1
`, "bogus")
}

func TestParseRejectsMultipleDocuments(t *testing.T) {
	wantParseError(t, minimalValid+"\n---\n"+minimalValid, "exactly one YAML document")
}

func TestParseRejectsMissingImage(t *testing.T) {
	wantParseError(t, `
steps:
  - name: build
    run: echo hi
`, "image is required")
}

// TestParseRejectsWhatIsNotAnImageReference covers image and requires
// values the docker client would misread, above all one starting with
// "-", which it would take for an option.
func TestParseRejectsWhatIsNotAnImageReference(t *testing.T) {
	for _, image := range []string{"docker: 29 cli", "--privileged", "-v=/:/host", "-h", "alpine;rm", "../x"} {
		wantParseError(t, "image: \""+image+"\"\nsteps:\n  - name: build\n    run: echo hi\n", "is not an image reference")
		wantParseError(t, "image: alpine:3.20\nrequires: [\""+image+"\"]\nsteps:\n  - name: build\n    run: echo hi\n",
			"requires[0]")
	}
	for _, image := range []string{"alpine", "golang:1.25-alpine", "registry.example.com:5000/team/app:1.4.2",
		"alpine@sha256:" + strings.Repeat("a", 64), "my_org/app.v2-x"} {
		mustParse(t, "image: "+image+"\nrequires: [\""+image+"\"]\nsteps:\n  - name: build\n    run: echo hi\n")
	}
}

func TestParseValidTimeout(t *testing.T) {
	cfg := mustParse(t, `
image: docker:29-cli
timeout: 45m
steps:
  - name: build
    run: echo hi
`)
	if cfg.Timeout.String() != "45m0s" {
		t.Errorf("Timeout = %v", cfg.Timeout)
	}
}

func TestParseRejectsInvalidTimeout(t *testing.T) {
	wantParseError(t, `
image: docker:29-cli
timeout: not-a-duration
steps:
  - name: build
    run: echo hi
`, "timeout")
}

func TestParseRejectsNegativeTimeout(t *testing.T) {
	wantParseError(t, `
image: docker:29-cli
timeout: -5m
steps:
  - name: build
    run: echo hi
`, "timeout must be positive")
}

// TestParseRejectsAnUnboundedTimeout is a pipeline that asks for a
// timeout long enough to hold a worker indefinitely.
func TestParseRejectsAnUnboundedTimeout(t *testing.T) {
	wantParseError(t, `
image: docker:29-cli
timeout: 2562047h
steps:
  - name: build
    run: echo hi
`, "longer than the limit of 24h0m0s")
	mustParse(t, "image: docker:29-cli\ntimeout: 24h\nsteps:\n  - name: build\n    run: echo hi\n")
}

func TestParseRequires(t *testing.T) {
	cfg := mustParse(t, `
image: docker:29-cli
requires:
  - golang:1.26-bookworm
  - docker:29-cli
steps:
  - name: build
    run: echo hi
`)
	if len(cfg.Requires) != 2 {
		t.Fatalf("Requires = %v", cfg.Requires)
	}
}

func TestParseRejectsDuplicateRequires(t *testing.T) {
	wantParseError(t, `
image: docker:29-cli
requires:
  - golang:1.26-bookworm
  - golang:1.26-bookworm
steps:
  - name: build
    run: echo hi
`, "listed more than once")
}

func TestParseRejectsEmptyRequiresEntry(t *testing.T) {
	wantParseError(t, `
image: docker:29-cli
requires:
  - ""
steps:
  - name: build
    run: echo hi
`, "requires[0]")
}

func TestParseEnv(t *testing.T) {
	cfg := mustParse(t, `
image: docker:29-cli
env:
  DEBIAN_MIRROR: http://example/debian
steps:
  - name: build
    run: echo hi
`)
	if cfg.Env["DEBIAN_MIRROR"] != "http://example/debian" {
		t.Errorf("Env = %v", cfg.Env)
	}
}

func TestParseRejectsLowercaseEnvKey(t *testing.T) {
	wantParseError(t, `
image: docker:29-cli
env:
  debian_mirror: http://example/debian
steps:
  - name: build
    run: echo hi
`, "debian_mirror")
}

func TestParseRejectsReservedEnvPrefix(t *testing.T) {
	wantParseError(t, `
image: docker:29-cli
env:
  GITMAN_COMMIT: overridden
steps:
  - name: build
    run: echo hi
`, "reserved")
}

func TestParseRejectsControlCharacterInEnvValue(t *testing.T) {
	wantParseError(t, "image: docker:29-cli\nenv:\n  DEBIAN_MIRROR: \"http://example/debian\\r\"\nsteps:\n  - name: build\n    run: echo hi\n", "control character")
}

func TestParseTargets(t *testing.T) {
	cfg := mustParse(t, `
image: docker:29-cli
targets:
  staging:
    branch: develop
    env:
      DEPLOY_DIR: /srv/apps/x-stage
  production:
    tag: "*"
    env:
      DEPLOY_DIR: /srv/apps/x
steps:
  - name: build
    run: echo hi
  - name: deploy
    when: target
    run: echo deploy
`)
	if len(cfg.Targets) != 2 {
		t.Fatalf("Targets = %v", cfg.Targets)
	}
	staging := cfg.Targets["staging"]
	if staging.Kind != "branch" || staging.Pattern != "develop" {
		t.Errorf("staging target = %+v", staging)
	}
	if staging.Env["DEPLOY_DIR"] != "/srv/apps/x-stage" {
		t.Errorf("staging target env = %v", staging.Env)
	}
	production := cfg.Targets["production"]
	if production.Kind != "tag" || production.Pattern != "*" {
		t.Errorf("production target = %+v", production)
	}
	if cfg.Steps[1].When.Kind != WhenTarget {
		t.Errorf("deploy step When = %v", cfg.Steps[1].When)
	}
}

func TestParseRejectsTargetWithBothBranchAndTag(t *testing.T) {
	wantParseError(t, `
image: docker:29-cli
targets:
  production:
    branch: main
    tag: "v*"
steps:
  - name: build
    run: echo hi
`, "exactly one of branch or tag")
}

func TestParseRejectsTargetWithNeitherBranchNorTag(t *testing.T) {
	wantParseError(t, `
image: docker:29-cli
targets:
  production: {}
steps:
  - name: build
    run: echo hi
`, "exactly one of branch or tag")
}

func TestParseRejectsInvalidTargetName(t *testing.T) {
	wantParseError(t, `
image: docker:29-cli
targets:
  Production:
    tag: "*"
steps:
  - name: build
    run: echo hi
`, "Production")
}

func TestParseRejectsInvalidTargetPattern(t *testing.T) {
	wantParseError(t, `
image: docker:29-cli
targets:
  production:
    tag: "has space"
steps:
  - name: build
    run: echo hi
`, "targets.production")
}

func TestParseRejectsDuplicateTargetPattern(t *testing.T) {
	wantParseError(t, `
image: docker:29-cli
targets:
  production:
    tag: "v1.0"
  release:
    tag: "v1.0"
steps:
  - name: build
    run: echo hi
`, "matches the same tag pattern")
}

func TestParseRejectsNoSteps(t *testing.T) {
	wantParseError(t, `
image: docker:29-cli
steps: []
`, "at least one step is required")
}

func TestParseRejectsStepWithNoName(t *testing.T) {
	wantParseError(t, `
image: docker:29-cli
steps:
  - run: echo hi
`, "steps[0]: name is required")
}

func TestParseRejectsDuplicateStepName(t *testing.T) {
	wantParseError(t, `
image: docker:29-cli
steps:
  - name: build
    run: echo one
  - name: build
    run: echo two
`, "used by more than one step")
}

func TestParseRejectsStepWithNoRun(t *testing.T) {
	wantParseError(t, `
image: docker:29-cli
steps:
  - name: build
    run: ""
`, `"build": run is required`)
}

func TestParseRejectsStepWhenForUndeclaredTarget(t *testing.T) {
	wantParseError(t, `
image: docker:29-cli
steps:
  - name: deploy
    when: production
    run: echo hi
`, `not "always"`)
}

func TestParseStepWhenNamedTarget(t *testing.T) {
	cfg := mustParse(t, `
image: docker:29-cli
targets:
  production:
    tag: "*"
steps:
  - name: deploy
    when: production
    run: echo hi
`)
	if cfg.Steps[0].When.Kind != WhenNamed || cfg.Steps[0].When.Name != "production" {
		t.Errorf("When = %+v", cfg.Steps[0].When)
	}
}

func TestParseCollectsMultipleErrors(t *testing.T) {
	_, err := Parse([]byte(`
steps:
  - run: ""
`))
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "image is required") {
		t.Errorf("expected the image error, got: %s", msg)
	}
	if !strings.Contains(msg, "name is required") {
		t.Errorf("expected the step-name error, got: %s", msg)
	}
	if strings.Count(msg, "\n  - ") < 2 {
		t.Errorf("expected at least two bulleted errors, got: %s", msg)
	}
}

func TestParseRejectsOversizedFile(t *testing.T) {
	big := minimalValid + "#" + strings.Repeat("x", MaxFileBytes) + "\n"
	wantParseError(t, big, "the limit is")
}

func TestParseRejectsTooManySteps(t *testing.T) {
	var b strings.Builder
	b.WriteString("image: docker:29-cli\nsteps:\n")
	for i := 0; i <= MaxSteps; i++ {
		b.WriteString("  - name: step")
		b.WriteString(strings.Repeat("x", i+1))
		b.WriteString("\n    run: echo hi\n")
	}
	wantParseError(t, b.String(), "steps: 65 steps")
}

func loadFixture(t *testing.T, name string) *Config {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "pipelines", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	cfg, err := Parse(data)
	if err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	return cfg
}

func stepNames(cfg *Config) []string {
	names := make([]string, len(cfg.Steps))
	for i, s := range cfg.Steps {
		names[i] = s.Name
	}
	return names
}

// runningSteps resolves cfg's target for kind/ref and returns the names of
// the steps that would actually run, in order — the same decision the
// worker makes for a real push, without spawning anything.
func runningSteps(cfg *Config, kind git.Kind, ref string) []string {
	target, _ := cfg.ResolveTarget(kind, ref)
	var running []string
	for _, s := range cfg.Steps {
		if s.When.ShouldRun(kind, target) {
			running = append(running, s.Name)
		}
	}
	return running
}

func assertStringSlicesEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestGoldenSmsGateway(t *testing.T) {
	cfg := loadFixture(t, "sms-gateway.gitman.yml")

	assertStringSlicesEqual(t, stepNames(cfg), []string{"build image", "verify image", "deploy"})

	if len(cfg.Targets) != 1 {
		t.Fatalf("expected exactly one target, got %v", cfg.Targets)
	}
	production, ok := cfg.Targets["production"]
	if !ok || production.Kind != git.KindTag || production.Pattern != "*" {
		t.Fatalf("production target = %+v, ok=%v", production, ok)
	}

	// A branch push builds and verifies, but does not deploy: no target
	// matches a branch, and "deploy" only runs for the named "production"
	// target.
	assertStringSlicesEqual(t, runningSteps(cfg, git.KindBranch, "develop"), []string{"build image", "verify image"})

	// A tag push matches "production", so all three steps run, including
	// deploy.
	assertStringSlicesEqual(t, runningSteps(cfg, git.KindTag, "v1.0.0"), []string{"build image", "verify image", "deploy"})
}

func TestGoldenCerv(t *testing.T) {
	cfg := loadFixture(t, "cerv.gitman.yml")

	assertStringSlicesEqual(t, stepNames(cfg), []string{"verify version tag", "build image", "verify built image"})

	if len(cfg.Targets) != 0 {
		t.Fatalf("expected no targets, got %v", cfg.Targets)
	}

	// Every step is gated by when: tag, which checks the ref kind
	// directly and does not depend on any target matching (cerv declares
	// none). A branch push therefore runs nothing.
	if running := runningSteps(cfg, git.KindBranch, "develop"); len(running) != 0 {
		t.Fatalf("expected no steps to run for a branch push, got %v", running)
	}

	// A tag push runs every step.
	assertStringSlicesEqual(t, runningSteps(cfg, git.KindTag, "v1.0.0"), []string{"verify version tag", "build image", "verify built image"})
}

func TestGoldenWaiotech(t *testing.T) {
	cfg := loadFixture(t, "waiotech.gitman.yml")

	wantSteps := []string{"build server", "build dashboard", "build admin", "build website", "build deployer", "deploy"}
	assertStringSlicesEqual(t, stepNames(cfg), wantSteps)

	if len(cfg.Targets) != 2 {
		t.Fatalf("expected exactly two targets, got %v", cfg.Targets)
	}
	staging, ok := cfg.Targets["staging"]
	if !ok || staging.Kind != git.KindBranch || staging.Pattern != "develop" {
		t.Fatalf("staging target = %+v, ok=%v", staging, ok)
	}
	production, ok := cfg.Targets["production"]
	if !ok || production.Kind != git.KindTag || production.Pattern != "*" {
		t.Fatalf("production target = %+v, ok=%v", production, ok)
	}

	// A push to develop matches "staging": every step is gated by
	// when: target, so all six run.
	assertStringSlicesEqual(t, runningSteps(cfg, git.KindBranch, "develop"), wantSteps)

	// A push to a branch that matches no target runs nothing.
	if running := runningSteps(cfg, git.KindBranch, "feature/login-fix"); len(running) != 0 {
		t.Fatalf("expected no steps to run for an unmatched branch, got %v", running)
	}

	// A tag push matches "production": all six run again.
	assertStringSlicesEqual(t, runningSteps(cfg, git.KindTag, "v1.4.2"), wantSteps)

	// The deploy step's own logic picks DEPLOY_DIR from the resolved
	// target's env, not from a shell if-statement: confirm both targets
	// actually carry it.
	if staging.Env["DEPLOY_DIR"] != "/srv/apps/waiotech-stage" {
		t.Errorf("staging DEPLOY_DIR = %q", staging.Env["DEPLOY_DIR"])
	}
	if production.Env["DEPLOY_DIR"] != "/srv/apps/waiotech" {
		t.Errorf("production DEPLOY_DIR = %q", production.Env["DEPLOY_DIR"])
	}
}

// TestParseListsProblemsInAStableOrder parses a file with several
// problems in its env and targets maps many times: the message, which a
// pusher reads and compares between attempts, must not change.
func TestParseListsProblemsInAStableOrder(t *testing.T) {
	file := `
image: alpine:3.20
env:
  lower_a: x
  lower_b: x
  GITMAN_X: x
targets:
  Bad1: {branch: main}
  Bad2: {branch: main}
  ok: {}
steps:
  - name: s
    run: "true"
`
	_, first := Parse([]byte(file))
	if first == nil {
		t.Fatal("expected problems")
	}
	for i := 0; i < 50; i++ {
		if _, err := Parse([]byte(file)); err == nil || err.Error() != first.Error() {
			t.Fatalf("problems listed differently:\n%v\nthen\n%v", first, err)
		}
	}
	if !strings.Contains(first.Error(), `"GITMAN_X"`) || strings.Index(first.Error(), `"GITMAN_X"`) > strings.Index(first.Error(), `"lower_a"`) {
		t.Fatalf("problems not in key order:\n%v", first)
	}
}

// TestParseRejectsControlCharactersInStepNames covers names a database
// text column cannot store, or a page cannot show.
func TestParseRejectsControlCharactersInStepNames(t *testing.T) {
	for _, name := range []string{`"a\u0000b"`, `"a\tb"`, `"bell\u0007"`} {
		wantParseError(t, "image: alpine:3.20\nsteps:\n  - name: "+name+"\n    run: 'true'\n", "control character")
	}
}
