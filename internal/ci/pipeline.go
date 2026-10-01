// Package ci is Gitman's pipelines: parsing and validating .gitman.yml
// (this file), and the runs and deployments a pipeline produces
// (run.go, scheduler.go, deployment.go). service.go holds the
// package's orchestration; store.go holds every SQL statement it runs.
package ci

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mmrzaf/gitman/internal/git"
	"gopkg.in/yaml.v3"
)

// Config is a fully parsed, fully validated .gitman.yml. Every field has
// already passed every check Gitman makes, so anything holding a *Config
// can trust its shape: there is no second validation pass hiding
// downstream.
type Config struct {
	Image    string
	Docker   bool
	Timeout  time.Duration // zero means "use the instance's default"
	Requires []string
	Env      map[string]string
	Targets  map[string]Target
	Steps    []Step
}

// Target is one entry of the pipeline's targets map: a branch or tag
// pattern, and the environment overrides that apply when a run resolves
// to it.
type Target struct {
	Kind    git.Kind
	Pattern string
	Env     map[string]string
}

// WhenKind is the shape of a step's "when" condition.
type WhenKind string

const (
	// WhenAlways runs the step regardless of target or ref kind. It is
	// the default when a step has no "when".
	WhenAlways WhenKind = "always"
	// WhenTarget runs the step when the run matched any target.
	WhenTarget WhenKind = "target"
	// WhenBranch runs the step when the run's ref is a branch, whether or
	// not it matched a target.
	WhenBranch WhenKind = "branch"
	// WhenTag runs the step when the run's ref is a tag, whether or not
	// it matched a target.
	WhenTag WhenKind = "tag"
	// WhenNamed runs the step only when the run matched the specific
	// target named in When.Name.
	WhenNamed WhenKind = "named"
)

// When is a step's parsed "when" condition.
type When struct {
	Kind WhenKind
	Name string // set only when Kind == WhenNamed
}

// ShouldRun reports whether a step with this condition should run, given
// the ref kind of the run and the target name it resolved to (empty if
// none did).
func (w When) ShouldRun(refKind git.Kind, targetName string) bool {
	switch w.Kind {
	case WhenAlways:
		return true
	case WhenBranch:
		return refKind == git.KindBranch
	case WhenTag:
		return refKind == git.KindTag
	case WhenTarget:
		return targetName != ""
	case WhenNamed:
		return targetName == w.Name
	default:
		return false
	}
}

// Step is one entry of the pipeline's steps list.
type StepKind string

const (
	StepRun    StepKind = "run"
	StepDeploy StepKind = "deploy"
)

type Step struct {
	Type StepKind
	Name string
	When When
	Run  string
}

// ResolveTarget returns the name of the target that matches kind/name,
// following git's pattern-specificity rules: an exact pattern beats a
// wildcard, and among wildcards the more specific one wins — the same
// rule ref_rules uses to resolve overlapping patterns, so Gitman has
// exactly one way to decide "which pattern wins" anywhere it matters. It
// returns ok == false when no target matches.
func (c *Config) ResolveTarget(kind git.Kind, name string) (targetName string, ok bool) {
	var names, patterns []string
	for tname, target := range c.Targets {
		if target.Kind != kind {
			continue
		}
		names = append(names, tname)
		patterns = append(patterns, target.Pattern)
	}
	idx := git.SelectPattern(patterns, name)
	if idx < 0 {
		return "", false
	}
	return names[idx], true
}

// RunContext is everything about one run that its pipeline's steps and
// env can reference through built-in $GITMAN_* variables.
type RunContext struct {
	Repo      string
	RunNumber int64
	Commit    string // full commit hash
	RefKind   git.Kind
	RefName   string // branch or tag name
	Target    string // resolved target name; empty if none matched
}

var versionUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// Version returns the sanitized version string used as $GITMAN_VERSION:
// for a tag ref, the tag name with every character other than a letter,
// digit, dot, dash or underscore replaced by '-', leading and trailing
// dots/dashes trimmed, and the result capped at 128 characters; for
// a branch, the first 12 characters of the commit hash.
func (rc RunContext) Version() string {
	if rc.RefKind == git.KindTag {
		if v := sanitizeVersion(rc.RefName); v != "" {
			return v
		}
	}
	return firstN(rc.Commit, 12)
}

func sanitizeVersion(raw string) string {
	v := versionUnsafe.ReplaceAllString(raw, "-")
	v = strings.Trim(v, ".-")
	if len(v) > 128 {
		v = strings.TrimRight(v[:128], ".-")
	}
	return v
}

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Variables returns the run's built-in $GITMAN_* environment variables.
// $GITMAN_SUMMARY is not among them: it is a workspace file path the
// worker assigns per run, not a value derivable from a RunContext alone.
func (rc RunContext) Variables() map[string]string {
	return map[string]string{
		"GITMAN_REPO":     rc.Repo,
		"GITMAN_RUN":      strconv.FormatInt(rc.RunNumber, 10),
		"GITMAN_COMMIT":   rc.Commit,
		"GITMAN_SHORT":    firstN(rc.Commit, 7),
		"GITMAN_REF":      rc.RefName,
		"GITMAN_REF_KIND": string(rc.RefKind),
		"GITMAN_VERSION":  rc.Version(),
		"GITMAN_TARGET":   rc.Target,
	}
}

// ResolvedEnv returns the full environment for one run of this pipeline:
// Gitman's built-in variables, then the pipeline's shared env, then the
// resolved target's own env layered on top (a target's env overrides the
// shared env for the same key). Because env keys are validated at parse
// time to never start with "GITMAN_" (see validateEnv), the built-ins can
// never be shadowed by either layer, and this function does not need to
// defend against that itself.
func (c *Config) ResolvedEnv(rc RunContext) map[string]string {
	out := make(map[string]string, len(c.Env)+8)
	for k, v := range rc.Variables() {
		out[k] = v
	}
	for k, v := range c.Env {
		out[k] = v
	}
	if rc.Target != "" {
		if target, ok := c.Targets[rc.Target]; ok {
			for k, v := range target.Env {
				out[k] = v
			}
		}
	}
	return out
}

// rawConfig is .gitman.yml's on-disk shape. KnownFields(true) makes an
// unrecognized key a parse error, so a typo or a leftover key from an
// older format is caught immediately instead of being silently ignored.
type rawConfig struct {
	Image    string               `yaml:"image"`
	Docker   bool                 `yaml:"docker"`
	Timeout  string               `yaml:"timeout"`
	Requires []string             `yaml:"requires"`
	Env      map[string]string    `yaml:"env"`
	Targets  map[string]rawTarget `yaml:"targets"`
	Steps    []rawStep            `yaml:"steps"`
}

type rawTarget struct {
	Branch string            `yaml:"branch"`
	Tag    string            `yaml:"tag"`
	Env    map[string]string `yaml:"env"`
}

type rawStep struct {
	Type string `yaml:"type"`
	Name string `yaml:"name"`
	When string `yaml:"when"`
	Run  string `yaml:"run"`
}

// Limits on a pipeline file. They are far above anything a real pipeline
// needs and exist so a malformed or hostile file cannot make a push hook
// do unbounded work.
const (
	MaxFileBytes   = 256 << 10
	MaxSteps       = 64
	MaxTargets     = 16
	MaxRequires    = 32
	MaxEnvVars     = 128
	MaxRunBytes    = 64 << 10
	MaxStepNameLen = 100
	MaxEnvValueLen = 8 << 10
	// MaxTimeout caps a run's timeout. Each worker runs one run at a
	// time, so a run with no real limit could hold a worker for good.
	MaxTimeout = 24 * time.Hour
)

// FileName is the pipeline file's path at the root of a repository.
const FileName = ".gitman.yml"

// Parse reads and fully validates a .gitman.yml file. On success, every
// field of the returned Config is already known-good: nothing downstream
// needs to re-check it.
func Parse(data []byte) (*Config, error) {
	if len(data) > MaxFileBytes {
		return nil, fmt.Errorf("pipeline file is %d bytes; the limit is %d", len(data), MaxFileBytes)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var raw rawConfig
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("pipeline file is empty")
		}
		return nil, fmt.Errorf("parse pipeline file: %w", err)
	}

	// A second document in the same file is rejected outright: exactly
	// one pipeline per file, with no multi-document YAML feature to
	// explain.
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, errors.New("pipeline file must contain exactly one YAML document")
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse pipeline file: %w", err)
	}

	return validate(&raw)
}

// sortedKeys returns a map's keys in order, so problems are listed in the
// same order every time the same file is checked.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// validator accumulates every problem found in one pass, rather than
// stopping at the first: gitman check and a rejected push both show the
// person the whole list at once instead of one error per retry.
type validator struct {
	errs []string
}

func (v *validator) errorf(format string, args ...any) {
	v.errs = append(v.errs, fmt.Sprintf(format, args...))
}

func (v *validator) err() error {
	if len(v.errs) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("pipeline file is invalid:")
	for _, e := range v.errs {
		b.WriteString("\n  - ")
		b.WriteString(e)
	}
	return errors.New(b.String())
}

// imageRefPattern is the shape of a Docker image reference, such as
// golang:1.25-alpine or registry.example.com/team/app@sha256:…. It must
// start with a letter or digit: a value starting with "-" would be read
// by the docker client as an option.
var imageRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]{0,254}$`)

var envKeyPattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// validateEnv checks every key of a raw env map against Gitman's variable
// naming rule (uppercase letters, digits and underscores, not starting
// with a digit) and rejects the "GITMAN_" prefix Gitman reserves for its
// own built-in variables, so a repository's .gitman.yml can never shadow
// $GITMAN_COMMIT, $GITMAN_TARGET, and the rest.
func validateEnv(v *validator, label string, raw map[string]string) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > MaxEnvVars {
		v.errorf("%s: %d variables; the limit is %d", label, len(raw), MaxEnvVars)
		return nil
	}
	out := make(map[string]string, len(raw))
	for _, key := range sortedKeys(raw) {
		value := raw[key]
		if !envKeyPattern.MatchString(key) {
			v.errorf("%s: %q is not a valid variable name (uppercase letters, digits and underscores only, must not start with a digit)", label, key)
			continue
		}
		if strings.HasPrefix(key, "GITMAN_") {
			v.errorf("%s: %q starts with the \"GITMAN_\" prefix, which is reserved for Gitman's own variables", label, key)
			continue
		}
		if len(value) > MaxEnvValueLen {
			v.errorf("%s: %q is %d bytes; the limit is %d", label, key, len(value), MaxEnvValueLen)
			continue
		}
		if strings.IndexFunc(value, unicode.IsControl) >= 0 {
			v.errorf("%s: %q contains a control character", label, key)
			continue
		}
		out[key] = value
	}
	return out
}

var targetNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

func validateTargetName(name string) error {
	if !targetNamePattern.MatchString(name) {
		return fmt.Errorf("target name %q must start with a lowercase letter and contain only lowercase letters, digits, dashes and underscores", name)
	}
	return nil
}

// parseWhen validates a step's raw "when" string against the fixed set of
// conditions plus the pipeline's own declared target names.
func parseWhen(raw string, targetNames map[string]bool) (When, error) {
	switch raw {
	case "", string(WhenAlways):
		return When{Kind: WhenAlways}, nil
	case string(WhenTarget):
		return When{Kind: WhenTarget}, nil
	case string(WhenBranch):
		return When{Kind: WhenBranch}, nil
	case string(WhenTag):
		return When{Kind: WhenTag}, nil
	default:
		if !targetNames[raw] {
			return When{}, fmt.Errorf(`when: %q is not "always", "target", "branch", "tag", or a declared target name`, raw)
		}
		return When{Kind: WhenNamed, Name: raw}, nil
	}
}

func validate(raw *rawConfig) (*Config, error) {
	v := &validator{}
	cfg := &Config{}

	image := strings.TrimSpace(raw.Image)
	switch {
	case image == "":
		v.errorf("image is required")
	case !imageRefPattern.MatchString(image):
		v.errorf("image: %q is not an image reference, such as golang:1.25-alpine", image)
	default:
		cfg.Image = image
	}

	cfg.Docker = raw.Docker

	if timeout := strings.TrimSpace(raw.Timeout); timeout != "" {
		d, err := time.ParseDuration(timeout)
		switch {
		case err != nil:
			v.errorf("timeout: %v", err)
		case d <= 0:
			v.errorf("timeout must be positive")
		case d > MaxTimeout:
			v.errorf("timeout: %s is longer than the limit of %s", d, MaxTimeout)
		default:
			cfg.Timeout = d
		}
	}

	if len(raw.Requires) > MaxRequires {
		v.errorf("requires: %d images; the limit is %d", len(raw.Requires), MaxRequires)
		raw.Requires = nil
	}
	seenRequires := make(map[string]bool, len(raw.Requires))
	for i, r := range raw.Requires {
		r = strings.TrimSpace(r)
		switch {
		case r == "":
			v.errorf("requires[%d]: must not be empty", i)
		case !imageRefPattern.MatchString(r):
			v.errorf("requires[%d]: %q is not an image reference, such as postgres:16-alpine", i, r)
		case seenRequires[r]:
			v.errorf("requires[%d]: %q is listed more than once", i, r)
		default:
			seenRequires[r] = true
			cfg.Requires = append(cfg.Requires, r)
		}
	}

	cfg.Env = validateEnv(v, "env", raw.Env)

	if len(raw.Targets) > MaxTargets {
		v.errorf("targets: %d targets; the limit is %d", len(raw.Targets), MaxTargets)
		raw.Targets = nil
	}
	cfg.Targets = make(map[string]Target, len(raw.Targets))
	seenTargetPatterns := make(map[string]string, len(raw.Targets))
	for _, name := range sortedKeys(raw.Targets) {
		rt := raw.Targets[name]
		if err := validateTargetName(name); err != nil {
			v.errorf("targets.%s: %v", name, err)
			continue
		}

		hasBranch := strings.TrimSpace(rt.Branch) != ""
		hasTag := strings.TrimSpace(rt.Tag) != ""
		if hasBranch == hasTag {
			v.errorf("targets.%s: must set exactly one of branch or tag", name)
			continue
		}

		kind, pattern := git.KindTag, strings.TrimSpace(rt.Tag)
		if hasBranch {
			kind, pattern = git.KindBranch, strings.TrimSpace(rt.Branch)
		}
		if err := git.ValidatePattern(pattern); err != nil {
			v.errorf("targets.%s: %v", name, err)
			continue
		}

		key := string(kind) + ":" + pattern
		if other, exists := seenTargetPatterns[key]; exists {
			v.errorf("targets.%s: matches the same %s pattern %q as targets.%s", name, kind, pattern, other)
			continue
		}
		seenTargetPatterns[key] = name

		cfg.Targets[name] = Target{
			Kind:    kind,
			Pattern: pattern,
			Env:     validateEnv(v, fmt.Sprintf("targets.%s.env", name), rt.Env),
		}
	}

	targetNames := make(map[string]bool, len(cfg.Targets))
	for name := range cfg.Targets {
		targetNames[name] = true
	}

	switch {
	case len(raw.Steps) == 0:
		v.errorf("steps: at least one step is required")
	case len(raw.Steps) > MaxSteps:
		v.errorf("steps: %d steps; the limit is %d", len(raw.Steps), MaxSteps)
		raw.Steps = nil
	}
	seenStepNames := make(map[string]bool, len(raw.Steps))
	deployCount := 0
	for i, rs := range raw.Steps {
		name := strings.TrimSpace(rs.Name)
		if name == "" {
			v.errorf("steps[%d]: name is required", i)
			continue
		}
		if utf8.RuneCountInString(name) > MaxStepNameLen {
			v.errorf("steps[%d]: name is longer than %d characters", i, MaxStepNameLen)
			continue
		}
		if strings.IndexFunc(name, unicode.IsControl) >= 0 {
			v.errorf("steps[%d]: name %q contains a control character", i, name)
			continue
		}
		if seenStepNames[name] {
			v.errorf("steps[%d]: %q is used by more than one step", i, name)
			continue
		}
		seenStepNames[name] = true

		if strings.TrimSpace(rs.Run) == "" {
			v.errorf("steps[%d] %q: run is required", i, name)
			continue
		}
		if len(rs.Run) > MaxRunBytes {
			v.errorf("steps[%d] %q: run is %d bytes; the limit is %d", i, name, len(rs.Run), MaxRunBytes)
			continue
		}
		if strings.ContainsRune(rs.Run, 0) {
			v.errorf("steps[%d] %q: run contains a NUL byte", i, name)
			continue
		}

		when, err := parseWhen(strings.TrimSpace(rs.When), targetNames)
		if err != nil {
			v.errorf("steps[%d] %q: %v", i, name, err)
			continue
		}

		kind := StepKind(rs.Type)
		if kind == "" {
			kind = StepRun
		}
		if kind != StepRun && kind != StepDeploy {
			v.errorf("steps[%d] %q: type must be run or deploy", i, name)
			continue
		}
		if kind == StepDeploy {
			deployCount++
			if deployCount > 1 {
				v.errorf("steps: at most one deploy step is allowed")
			}
			if when.Kind != WhenTarget && when.Kind != WhenNamed {
				v.errorf("steps[%d] %q: deploy requires when: target or a configured target name", i, name)
			}
			if len(cfg.Targets) == 0 {
				v.errorf("steps[%d] %q: deploy requires at least one target", i, name)
			}
		}
		cfg.Steps = append(cfg.Steps, Step{Name: name, Type: kind, When: when, Run: rs.Run})
	}

	if err := v.err(); err != nil {
		return nil, err
	}
	return cfg, nil
}
