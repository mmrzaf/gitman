package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/models"
)

func WriteAuditEvents(ctx context.Context, database *db.DB, out io.Writer, limit int, jsonLines bool) error {
	events, err := database.ListAuditEvents(ctx, limit)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	for _, event := range events {
		if jsonLines {
			if err := encoder.Encode(event); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintln(out, formatAuditEvent(event)); err != nil {
			return err
		}
	}
	return nil
}

func formatAuditEvent(event models.AuditEvent) string {
	actor := event.ActorUsername
	if actor == "" {
		actor = "anonymous"
	}
	target := event.TargetType
	if event.TargetID != "" {
		if target != "" {
			target += ":"
		}
		target += event.TargetID
	}
	if target == "" {
		target = "-"
	}
	ip := event.SourceIP
	if ip == "" {
		ip = "-"
	}
	metadata := ""
	if len(event.Metadata) != 0 {
		encoded, err := json.Marshal(event.Metadata)
		if err == nil {
			metadata = " metadata=" + string(encoded)
		}
	}
	return fmt.Sprintf("%s action=%s actor=%s target=%s ip=%s%s",
		event.CreatedAt.UTC().Format(time.RFC3339),
		quoteAuditField(event.Action),
		quoteAuditField(actor),
		quoteAuditField(target),
		quoteAuditField(ip),
		metadata,
	)
}

func quoteAuditField(value string) string {
	if strings.ContainsAny(value, " \t\r\n\"") {
		encoded, _ := json.Marshal(value)
		return string(encoded)
	}
	return value
}
