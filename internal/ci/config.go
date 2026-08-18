package ci

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const ConfigFile = ".gitman-ci.yml"

const MaxConfigBytes = 256 * 1024

// secretRefRe matches ${{ secrets.SECRET_NAME }}.
var secretRefRe = regexp.MustCompile(`^\$\{\{\s*secrets\.([A-Z][A-Z0-9_]*)\s*\}\}$`)
var envKeyRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// Config is the validated representation of .gitman-ci.yml shared by the
// worker and web UI. Keeping one parser prevents the execution and presentation
// paths from disagreeing about what a pipeline means.
type Config struct {
	Image  string
	Docker bool
	Env    []EnvEntry
	Steps  []Step
}

// EnvEntry is a resolved environment variable declaration. If Secret is
// non-empty, the value must be pulled from the repository secret store.
type EnvEntry struct {
	Key    string
	Value  string
	Secret string
}

// Step is a named execution unit inside a CI run.
type Step struct {
	Name string
	Run  string
}

type rawConfig struct {
	Image  string            `yaml:"image"`
	Docker bool              `yaml:"docker"`
	Env    map[string]string `yaml:"env"`
	Steps  []struct {
		Name string `yaml:"name"`
		Run  string `yaml:"run"`
	} `yaml:"steps"`
}

// ParseConfig reads and validates a regular .gitman-ci.yml file.
func ParseConfig(path string) (*Config, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect config: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("CI config must be a regular file, not a symlink")
	}
	if info.Size() > MaxConfigBytes {
		return nil, fmt.Errorf("CI config is too large")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return ParseConfigBytes(data)
}

// ParseConfigBytes validates CI configuration already loaded from a repository
// object. This is used by the web UI to summarize the exact config at a run's
// immutable commit without checking files out to disk.
func ParseConfigBytes(data []byte) (*Config, error) {
	if len(data) > MaxConfigBytes {
		return nil, fmt.Errorf("CI config is too large")
	}

	var raw rawConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}
	// Decode a possible second document into an untyped value. KnownFields applies
	// to application config, not to the sentinel used only to detect another
	// document; decoding into struct{} would turn a valid second document into a
	// misleading "unknown field" error.
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("parse YAML: multiple documents are not supported")
		}
		return nil, fmt.Errorf("parse YAML: %w", err)
	}

	image := strings.TrimSpace(raw.Image)
	if image == "" {
		return nil, fmt.Errorf("'image' is required")
	}
	if len(image) > 512 || strings.HasPrefix(image, "-") || strings.ContainsAny(image, "\x00\r\n\t ") {
		return nil, fmt.Errorf("invalid image reference")
	}
	if len(raw.Steps) == 0 {
		return nil, fmt.Errorf("'steps' is required and must not be empty")
	}
	if len(raw.Steps) > 200 {
		return nil, fmt.Errorf("too many CI steps")
	}

	cfg := &Config{Image: image, Docker: raw.Docker}

	keys := make([]string, 0, len(raw.Env))
	for k := range raw.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, originalKey := range keys {
		value := raw.Env[originalKey]
		key := strings.TrimSpace(originalKey)
		if !envKeyRe.MatchString(key) {
			return nil, fmt.Errorf("invalid env key %q", key)
		}
		if strings.HasPrefix(key, "GITMAN_") {
			return nil, fmt.Errorf("env key %q is reserved", key)
		}
		if strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("env value for %q contains unsupported control characters", key)
		}

		entry := EnvEntry{Key: key}
		if match := secretRefRe.FindStringSubmatch(value); match != nil {
			entry.Secret = match[1]
		} else {
			entry.Value = value
		}
		cfg.Env = append(cfg.Env, entry)
	}

	for i, rawStep := range raw.Steps {
		name := strings.TrimSpace(rawStep.Name)
		if name == "" {
			return nil, fmt.Errorf("step %d: 'name' is required", i+1)
		}
		if len(name) > 120 {
			return nil, fmt.Errorf("step %d: 'name' is too long", i+1)
		}
		if strings.ContainsAny(name, "\x00\r\n") {
			return nil, fmt.Errorf("step %d: 'name' contains unsupported control characters", i+1)
		}
		run := strings.TrimRight(rawStep.Run, "\r\n")
		if strings.TrimSpace(run) == "" {
			return nil, fmt.Errorf("step %q: 'run' is required", name)
		}
		if strings.ContainsRune(run, '\x00') {
			return nil, fmt.Errorf("step %q: 'run' contains unsupported control characters", name)
		}
		cfg.Steps = append(cfg.Steps, Step{Name: name, Run: run})
	}

	return cfg, nil
}
