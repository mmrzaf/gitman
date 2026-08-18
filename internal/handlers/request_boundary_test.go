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
	tmpl, err := template.New("status").Funcs(templateFuncs).Parse(`{{statusLabel .}}|{{statusClass .}}|{{if canCancelRun .}}cancel{{else if canRetryRun .}}retry{{else}}none{{end}}`)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	for _, tt := range []struct {
		status models.CIStatus
		want   string
	}{
		{models.CIStatusPending, "Pending|badge-pending|cancel"},
		{models.CIStatusRunning, "Running|badge-running|cancel"},
		{models.CIStatusSuccess, "Success|badge-success|retry"},
		{models.CIStatusFailed, "Failed|badge-failed|retry"},
		{models.CIStatusSkipped, "Skipped|badge-skipped|retry"},
		{models.CIStatusCancelled, "Cancelled|badge-cancelled|retry"},
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
