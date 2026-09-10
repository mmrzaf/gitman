package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/mmrzaf/gitman/internal/models"
)

const maxAuditEventLimit = 500

// RecordAuditEvent appends an immutable security event. Metadata is serialized
// as JSON so callers can add small, non-sensitive context without requiring a
// schema change for every new event type.
func (db *DB) RecordAuditEvent(ctx context.Context, event models.AuditEvent) error {
	if event.Action == "" {
		return fmt.Errorf("audit action is required")
	}
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return fmt.Errorf("encode audit metadata: %w", err)
	}
	if len(metadata) > 16*1024 {
		return fmt.Errorf("audit metadata is too large")
	}
	id := event.ID
	if id == "" {
		id = uuid.New().String()
	}
	var actorUserID any
	if event.ActorUserID != "" {
		actorUserID = event.ActorUserID
	}
	_, err = db.sql.ExecContext(ctx, `
		INSERT INTO audit_events (
			id, actor_user_id, actor_username, action, target_type, target_id,
			source_ip, request_id, metadata_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, actorUserID, event.ActorUsername, event.Action, event.TargetType, event.TargetID,
		event.SourceIP, event.RequestID, string(metadata))
	return err
}

// ListAuditEvents returns newest-first audit events for operational consumers.
// The bounded limit prevents accidentally materializing the full audit history
// into memory.
func (db *DB) ListAuditEvents(ctx context.Context, limit int) ([]models.AuditEvent, error) {
	limit = boundedAuditLimit(limit)
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, actor_user_id, actor_username, action, target_type, target_id,
		       source_ip, request_id, metadata_json, created_at
		FROM audit_events
		ORDER BY created_at DESC, rowid DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	return scanAuditEvents(rows)
}

// ListAuditEventsForUser returns security activity relevant to one account.
// It includes actions performed by the account and events that target the
// account, such as failed logins and administrator password resets. Targeted
// events may deliberately have no authenticated actor.
func (db *DB) ListAuditEventsForUser(ctx context.Context, userID string, limit int) ([]models.AuditEvent, error) {
	if userID == "" {
		return nil, fmt.Errorf("user id is required")
	}
	limit = boundedAuditLimit(limit)
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, actor_user_id, actor_username, action, target_type, target_id,
		       source_ip, request_id, metadata_json, created_at
		FROM audit_events
		WHERE actor_user_id = ?
		   OR (target_type = 'user' AND target_id = ?)
		ORDER BY created_at DESC, rowid DESC
		LIMIT ?
	`, userID, userID, limit)
	if err != nil {
		return nil, err
	}
	return scanAuditEvents(rows)
}

func boundedAuditLimit(limit int) int {
	if limit <= 0 {
		return 100
	}
	if limit > maxAuditEventLimit {
		return maxAuditEventLimit
	}
	return limit
}

func scanAuditEvents(rows *sql.Rows) (events []models.AuditEvent, err error) {
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		var event models.AuditEvent
		var actorUserID sql.NullString
		var metadataJSON string
		var createdAt int64
		if err := rows.Scan(
			&event.ID, &actorUserID, &event.ActorUsername, &event.Action,
			&event.TargetType, &event.TargetID, &event.SourceIP, &event.RequestID,
			&metadataJSON, &createdAt,
		); err != nil {
			return nil, err
		}
		if actorUserID.Valid {
			event.ActorUserID = actorUserID.String
		}
		if err := json.Unmarshal([]byte(metadataJSON), &event.Metadata); err != nil {
			return nil, fmt.Errorf("decode audit metadata for event %s: %w", event.ID, err)
		}
		event.CreatedAt = unixToTime(createdAt)
		events = append(events, event)
	}
	return events, rows.Err()
}
