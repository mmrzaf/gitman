package ci

import "strings"

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
