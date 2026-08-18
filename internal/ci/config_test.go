package ci

import (
	"strings"
	"testing"
)

func TestParseConfigBytesMatchesWorkerContract(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
image: alpine:3.20
docker: true
env:
  MODE: test
  TOKEN: ${{ secrets.API_TOKEN }}
steps:
  - name: Test
    run: go test ./...
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Image != "alpine:3.20" || !cfg.Docker || len(cfg.Steps) != 1 || cfg.Steps[0].Name != "Test" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if len(cfg.Env) != 2 || cfg.Env[1].Secret != "API_TOKEN" {
		t.Fatalf("unexpected environment: %+v", cfg.Env)
	}
}

func TestParseConfigBytesRejectsUnknownFields(t *testing.T) {
	_, err := ParseConfigBytes([]byte("image: alpine\nunknown: true\nsteps:\n- name: test\n  run: echo ok\n"))
	if err == nil || !strings.Contains(err.Error(), "field unknown") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}

func TestParseConfigBytesRejectsMultipleDocuments(t *testing.T) {
	_, err := ParseConfigBytes([]byte("image: alpine\nsteps:\n- name: one\n  run: echo one\n---\nimage: alpine\nsteps:\n- name: two\n  run: echo two\n"))
	if err == nil || !strings.Contains(err.Error(), "multiple documents") {
		t.Fatalf("expected multiple document error, got %v", err)
	}
}
