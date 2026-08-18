package handlers

import (
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/models"
)

func TestDecodeTriggerRequestAcceptsJSONMediaTypeParameters(t *testing.T) {
	req := httptest.NewRequest("POST", "/repo/ci/trigger", strings.NewReader(`{"branch":"main"}`))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	got, err := decodeTriggerRequest(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "main" {
		t.Fatalf("branch = %q, want main", got.Branch)
	}
}

func TestDecodeTriggerRequestAcceptsUnifiedRevisionSelector(t *testing.T) {
	for _, tt := range []struct {
		form   string
		branch string
		tag    string
	}{
		{"revision=branch%3Amain", "main", ""},
		{"revision=tag%3Av1.0.0", "", "v1.0.0"},
	} {
		req := httptest.NewRequest("POST", "/repo/ci/trigger", strings.NewReader(tt.form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		got, err := decodeTriggerRequest(httptest.NewRecorder(), req)
		if err != nil {
			t.Fatal(err)
		}
		if got.Branch != tt.branch || got.Tag != tt.tag {
			t.Fatalf("decoded branch=%q tag=%q, want branch=%q tag=%q", got.Branch, got.Tag, tt.branch, tt.tag)
		}
	}
}

func TestDecodeTriggerRequestRejectsUnknownRevisionSelector(t *testing.T) {
	req := httptest.NewRequest("POST", "/repo/ci/trigger", strings.NewReader("revision=commit%3Adeadbeef"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_, err := decodeTriggerRequest(httptest.NewRecorder(), req)
	if err == nil || !apperr.Is(err, apperr.KindInvalid) {
		t.Fatalf("error = %v, want invalid", err)
	}
}

func TestDecodeTriggerRequestRejectsUnsupportedContentType(t *testing.T) {
	req := httptest.NewRequest("POST", "/repo/ci/trigger", strings.NewReader("branch=main"))
	req.Header.Set("Content-Type", "text/plain")
	_, err := decodeTriggerRequest(httptest.NewRecorder(), req)
	if err == nil || !apperr.Is(err, apperr.KindUnsupported) {
		t.Fatalf("error = %v, want unsupported", err)
	}
}

func TestRequestMediaTypeRejectsMalformedHeader(t *testing.T) {
	req := httptest.NewRequest("POST", "/repo/ci/trigger", nil)
	req.Header.Set("Content-Type", `application/json; charset="`)
	_, err := requestMediaType(req)
	if err == nil || !apperr.Is(err, apperr.KindInvalid) {
		t.Fatalf("error = %v, want invalid", err)
	}
}

func TestCIStatusTemplateHelpersAcceptTypedStatus(t *testing.T) {
	tmpl, err := template.New("status").Funcs(templateFuncs).Parse(`{{statusLabel .}}|{{if canCancelRun .}}cancel{{else if canRetryRun .}}retry{{else}}none{{end}}`)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	for _, tt := range []struct {
		status models.CIStatus
		want   string
	}{
		{models.CIStatusPending, "Pending|cancel"},
		{models.CIStatusRunning, "Running|cancel"},
		{models.CIStatusSuccess, "Success|retry"},
		{models.CIStatusFailed, "Failed|retry"},
		{models.CIStatusSkipped, "Skipped|retry"},
		{models.CIStatusCancelled, "Cancelled|retry"},
	} {
		var out strings.Builder
		if err := tmpl.Execute(&out, tt.status); err != nil {
			t.Fatalf("execute %q: %v", tt.status, err)
		}
		if got := out.String(); got != tt.want {
			t.Fatalf("status %q rendered %q, want %q", tt.status, got, tt.want)
		}
	}
}
