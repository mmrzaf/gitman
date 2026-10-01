package ci

import "testing"

func TestNormalizeImageReference(t *testing.T) {
	for ref, want := range map[string]string{
		"alpine": "docker.io/library/alpine:latest", "alpine:3.20": "docker.io/library/alpine:3.20",
		"docker.io/alpine": "docker.io/library/alpine:latest", "index.docker.io/library/alpine:latest": "docker.io/library/alpine:latest",
		"team/app": "docker.io/team/app:latest", "localhost:5000/app": "localhost:5000/app:latest",
		"registry.example/app:v1": "registry.example/app:v1", "alpine@sha256:abc": "docker.io/library/alpine@sha256:abc", "sha256:abc": "sha256:abc",
	} {
		if got := NormalizeImageReference(ref); got != want {
			t.Errorf("%s = %s, want %s", ref, got, want)
		}
	}
}
