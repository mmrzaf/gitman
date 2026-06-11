package worker

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

const ciConfigFile = ".gitman-ci.yml"

// secretRefRe matches ${{ secrets.SECRET_NAME }}.
var secretRefRe = regexp.MustCompile(`^\$\{\{\s*secrets\.([A-Z][A-Z0-9_]*)\s*\}\}$`)

var envKeyRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// Keep the image parser intentionally conservative. Docker supports more, but
// CI config is attacker-controlled input; rejecting edge cases is safer.
var imageRefRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]*(?::[a-zA-Z0-9._-]+)?(?:@[A-Za-z0-9_+.-]+:[A-Fa-f0-9]+)?$`)

// CIConfig holds the parsed contents of .gitman-ci.yml.
type CIConfig struct {
	Image  string
	Docker bool
	Env    []envEntry
	Steps  []CIStep
}

// envEntry is a resolved environment variable declaration.
// If Secret is non-empty, the value must be pulled from the secrets store.
// Otherwise Value is used directly.
type envEntry struct {
	Key    string
	Value  string
	Secret string
}

// CIStep is a named execution unit inside a CI run.
type CIStep struct {
	Name string
	Run  string
}

// rawConfig mirrors the YAML layout for unmarshalling.
type rawConfig struct {
	Image  string            `yaml:"image"`
	Docker bool              `yaml:"docker"`
	Env    map[string]string `yaml:"env"`
	Steps  []struct {
		Name string `yaml:"name"`
		Run  string `yaml:"run"`
	} `yaml:"steps"`
}

// parseCIConfig reads and validates a .gitman-ci.yml file.
// It returns a structured CIConfig ready for execution.
func parseCIConfig(path string) (*CIConfig, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect config: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("CI config must be a regular file, not a symlink")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if len(data) > 256*1024 {
		return nil, fmt.Errorf("CI config is too large")
	}

	var raw rawConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("parse YAML: multiple documents are not supported")
		}
		return nil, fmt.Errorf("parse YAML: %w", err)
	}

	image := strings.TrimSpace(raw.Image)
	if image == "" {
		return nil, fmt.Errorf("'image' is required")
	}
	if strings.HasPrefix(image, "-") || strings.ContainsAny(image, " \t\r\n") || !imageRefRe.MatchString(image) {
		return nil, fmt.Errorf("invalid image reference")
	}
	if len(raw.Steps) == 0 {
		return nil, fmt.Errorf("'steps' is required and must not be empty")
	}
	if len(raw.Steps) > 200 {
		return nil, fmt.Errorf("too many CI steps")
	}

	cfg := &CIConfig{Image: image, Docker: raw.Docker}

	keys := make([]string, 0, len(raw.Env))
	for k := range raw.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		v := raw.Env[k]
		k = strings.TrimSpace(k)
		if !envKeyRe.MatchString(k) {
			return nil, fmt.Errorf("invalid env key %q", k)
		}
		if strings.HasPrefix(k, "GITMAN_") {
			return nil, fmt.Errorf("env key %q is reserved", k)
		}
		if strings.ContainsAny(v, "\x00\r\n") {
			return nil, fmt.Errorf("env value for %q contains unsupported control characters", k)
		}

		entry := envEntry{Key: k}
		if m := secretRefRe.FindStringSubmatch(v); m != nil {
			entry.Secret = m[1]
		} else {
			entry.Value = v
		}
		cfg.Env = append(cfg.Env, entry)
	}

	for i, s := range raw.Steps {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			return nil, fmt.Errorf("step %d: 'name' is required", i+1)
		}
		if len(name) > 120 {
			return nil, fmt.Errorf("step %d: 'name' is too long", i+1)
		}
		if strings.ContainsAny(name, "\x00\r\n") {
			return nil, fmt.Errorf("step %d: 'name' contains unsupported control characters", i+1)
		}
		run := strings.TrimRight(s.Run, "\r\n")
		if strings.TrimSpace(run) == "" {
			return nil, fmt.Errorf("step %q: 'run' is required", name)
		}
		if strings.ContainsRune(run, '\x00') {
			return nil, fmt.Errorf("step %q: 'run' contains unsupported control characters", name)
		}
		cfg.Steps = append(cfg.Steps, CIStep{Name: name, Run: run})
	}

	return cfg, nil
}
