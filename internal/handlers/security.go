package handlers

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/models"
)

const securityActivityLimit = 100

type SecurityEventView struct {
	Summary   string
	Detail    string
	SourceIP  string
	Actor     string
	CreatedAt time.Time
	Severity  string
}

type SecurityPageData struct {
	PageData
	Events []SecurityEventView
}

func (app *App) HandleSecurityGET(w http.ResponseWriter, r *http.Request) {
	user := GetUser(r)
	events, err := app.DB.ListAuditEventsForUser(r.Context(), user.ID, securityActivityLimit)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Security activity is temporarily unavailable", err))
		return
	}
	views := make([]SecurityEventView, 0, len(events))
	for _, event := range events {
		views = append(views, securityEventView(event))
	}
	app.renderPage(w, r, "security.html", &SecurityPageData{
		PageData: PageData{Title: "Security activity", User: user},
		Events:   views,
	})
}

func securityEventView(event models.AuditEvent) SecurityEventView {
	view := SecurityEventView{
		SourceIP:  event.SourceIP,
		Actor:     event.ActorUsername,
		CreatedAt: event.CreatedAt,
	}
	detail := func(parts ...string) string {
		clean := parts[:0]
		for _, part := range parts {
			if part = strings.TrimSpace(part); part != "" {
				clean = append(clean, part)
			}
		}
		return strings.Join(clean, " · ")
	}

	switch event.Action {
	case models.AuditActionLoginSucceeded:
		view.Summary = "Signed in"
	case models.AuditActionLoginFailed:
		view.Summary = "Failed sign-in attempt"
		view.Severity = "warning"
	case models.AuditActionUserRegistered:
		view.Summary = "Account created"
	case models.AuditActionUserCreated:
		view.Summary = "Account created by administrator"
	case models.AuditActionPasswordReset:
		view.Summary = "Password reset by administrator"
		view.Severity = "warning"
	case models.AuditActionTokenCreated:
		view.Summary = "Access token created"
		view.Detail = detail(event.Metadata["name"], auditScopeDetail(event.Metadata["scope"]), expiryDetail(event.Metadata["expires_at"]))
	case models.AuditActionTokenRevoked:
		view.Summary = "Access token revoked"
	case models.AuditActionSSHKeyCreated:
		view.Summary = "SSH key added"
		view.Detail = event.Metadata["name"]
	case models.AuditActionSSHKeyDeleted:
		view.Summary = "SSH key removed"
		view.Detail = event.Metadata["name"]
	case models.AuditActionRepositoryCreated:
		view.Summary = "Repository created"
		view.Detail = event.Metadata["name"]
	case models.AuditActionRepositoryUpdated:
		view.Summary = "Repository settings changed"
		if event.Metadata["is_private"] != "" {
			view.Detail = "Private: " + event.Metadata["is_private"]
		}
	case models.AuditActionRepositoryDeleted:
		view.Summary = "Repository deleted"
		view.Severity = "warning"
	case models.AuditActionCollaboratorUpsert:
		view.Summary = "Repository collaborator changed"
		view.Detail = detail(event.Metadata["collaborator_username"], event.Metadata["access_level"])
	case models.AuditActionCollaboratorRemoved:
		view.Summary = "Repository collaborator removed"
	case models.AuditActionCISecretUpserted:
		view.Summary = "CI secret changed"
		view.Detail = event.Metadata["key"]
	case models.AuditActionCISecretDeleted:
		view.Summary = "CI secret removed"
	case models.AuditActionCIRefRuleUpserted:
		view.Summary = "CI trusted ref changed"
		view.Detail = detail(event.Metadata["ref_type"], event.Metadata["ref_name"])
	case models.AuditActionCIRefRuleDeleted:
		view.Summary = "CI trusted ref removed"
		view.Detail = detail(event.Metadata["ref_type"], event.Metadata["ref_name"])
	case models.AuditActionUserDeleted:
		view.Summary = "Account deleted by administrator"
		view.Severity = "warning"
	default:
		view.Summary = "Security event"
		view.Detail = event.Action
	}
	return view
}

func auditScopeDetail(raw string) string {
	switch models.AccessTokenScope(strings.TrimSpace(raw)) {
	case models.AccessTokenScopeRepoRead:
		return "read-only"
	case models.AccessTokenScopeRepoWrite:
		return "read & write"
	default:
		return ""
	}
}

func expiryDetail(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	when, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("expires %s", when.Local().Format("Jan 02, 2006"))
}
