package ci

import "strings"

// NormalizeImageRepository returns the canonical repository part of ref:
// Docker's default registry and namespace are applied, and any tag or digest
// is removed.
func NormalizeImageRepository(ref string) string {
	normalized := NormalizeImageReference(ref)
	if i := strings.IndexByte(normalized, '@'); i >= 0 {
		normalized = normalized[:i]
	}
	if i := strings.LastIndexByte(normalized, ':'); i > strings.LastIndexByte(normalized, '/') {
		normalized = normalized[:i]
	}
	return normalized
}

// ImageTagForVersion is the one mapping from a recorded deployment version to
// the tag a pipeline builds for it. The version is the run's GITMAN_VERSION,
// which is already safe as a Docker tag.
func ImageTagForVersion(repository, version string) string {
	return repository + ":" + version
}

// NormalizeImageReference matches Docker's default registry, namespace and tag.
// Digests and daemon image IDs retain their identity.
func NormalizeImageReference(ref string) string {
	if strings.HasPrefix(ref, "sha256:") {
		return ref
	}
	if len(ref) == 64 && strings.Trim(ref, "0123456789abcdef") == "" {
		return "sha256:" + ref
	}
	parts := strings.SplitN(ref, "/", 2)
	registry, name := "docker.io", ref
	if len(parts) == 2 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		registry, name = parts[0], parts[1]
	}
	if registry == "index.docker.io" {
		registry = "docker.io"
	}
	if registry == "docker.io" && !strings.Contains(name, "/") {
		name = "library/" + name
	}
	if !strings.Contains(name, "@") && strings.LastIndex(name, ":") < strings.LastIndex(name, "/")+1 {
		name += ":latest"
	}
	return registry + "/" + name
}

func normalizeImages(images []string) []string {
	result := make([]string, 0, len(images))
	for _, ref := range images {
		result = append(result, NormalizeImageReference(ref))
	}
	return result
}
