package handlers

import (
	"bytes"
	"testing"
)

func TestEmbeddedTemplatesLoad(t *testing.T) {
	templates, err := LoadTemplates()
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) == 0 {
		t.Fatal("no embedded page templates loaded")
	}
}

func TestProgressiveEnhancementDoesNotExposeDeadControls(t *testing.T) {
	ciRun, err := embeddedFiles.ReadFile("templates/pages/repo_ci_run.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`ci-log-view-tabs js-only`,
		`class="secondary small active js-only" type="button" data-log-follow`,
		`class="secondary small active js-only" type="button" data-log-wrap`,
		`ci-log-search js-only`,
		`ci-new-output js-only`,
		`live-note js-only`,
	} {
		if !bytes.Contains(ciRun, []byte(required)) {
			t.Fatalf("CI run template is missing progressive-enhancement marker %q", required)
		}
	}
	artifacts, err := embeddedFiles.ReadFile("templates/partials/artifact_tree.html")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(artifacts, []byte(`secondary small js-only`)) {
		t.Fatal("artifact copy action must be hidden without JavaScript")
	}
	css, err := embeddedFiles.ReadFile("static/css/gitman.css")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(css, []byte(`.site-nav a[href="/keys"]`)) || bytes.Contains(css, []byte(`.site-nav a[href="/tokens"]`)) {
		t.Fatal("mobile CSS must not hide account navigation")
	}
}
