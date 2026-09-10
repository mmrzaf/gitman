package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/mmrzaf/gitman/internal/models"
)

const auditWriteTimeout = 2 * time.Second

func (app *App) recordAuditEvent(r *http.Request, actor *models.User, action, targetType, targetID string, metadata map[string]string) {
	if app == nil || app.DB == nil || r == nil {
		return
	}
	event := models.AuditEvent{
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		SourceIP:   app.clientIP(r),
		RequestID:  RequestID(r),
		Metadata:   metadata,
	}
	if actor != nil {
		event.ActorUserID = actor.ID
		event.ActorUsername = actor.Username
	}

	// Once the protected mutation has committed, a client disconnect must not be
	// able to cancel the corresponding audit write. Keep the write synchronous
	// and tightly bounded so the event is durable before the handler returns
	// without allowing a database problem to hang the request indefinitely.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), auditWriteTimeout)
	defer cancel()
	if err := app.DB.RecordAuditEvent(ctx, event); err != nil {
		slog.Error("record audit event",
			"request_id", RequestID(r),
			"action", action,
			"target_type", targetType,
			"target_id", targetID,
			"error", err,
		)
	}
}
