package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/mmrzaf/gitman/internal/models"
)

func scanRepoCIRefRule(scanner rowScanner) (*models.RepoCIRefRule, error) {
	var rule models.RepoCIRefRule
	var createdAt, updatedAt int64
	if err := scanner.Scan(
		&rule.RepoID,
		&rule.RefType,
		&rule.RefName,
		&rule.AutoRun,
		&rule.AllowSecrets,
		&rule.AllowDockerSocket,
		&createdAt,
		&updatedAt,
	); err != nil {
		return nil, err
	}
	rule.CreatedAt = unixToTime(createdAt)
	rule.UpdatedAt = unixToTime(updatedAt)
	return &rule, nil
}

func validateCIRefRule(refType, refName string) error {
	if refType != "branch" && refType != "tag" {
		return fmt.Errorf("invalid CI ref rule type %q", refType)
	}
	if refName == "" {
		return fmt.Errorf("CI ref rule name is required")
	}
	return nil
}

func (db *DB) UpsertRepoCIRefRule(ctx context.Context, rule models.RepoCIRefRule) error {
	if err := validateCIRefRule(rule.RefType, rule.RefName); err != nil {
		return err
	}
	now := time.Now().Unix()
	_, err := db.ExecContext(ctx, `
		INSERT INTO repo_ci_ref_rules (
			repo_id, ref_type, ref_name, auto_run, allow_secrets,
			allow_docker_socket, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo_id, ref_type, ref_name)
		DO UPDATE SET
			auto_run = excluded.auto_run,
			allow_secrets = excluded.allow_secrets,
			allow_docker_socket = excluded.allow_docker_socket,
			updated_at = excluded.updated_at
	`, rule.RepoID, rule.RefType, rule.RefName, rule.AutoRun, rule.AllowSecrets, rule.AllowDockerSocket, now, now)
	return err
}

func (db *DB) GetRepoCIRefRule(ctx context.Context, repoID, refType, refName string) (*models.RepoCIRefRule, error) {
	if err := validateCIRefRule(refType, refName); err != nil {
		return nil, err
	}
	rule, err := scanRepoCIRefRule(db.QueryRowContext(ctx, `
		SELECT repo_id, ref_type, ref_name, auto_run, allow_secrets,
		       allow_docker_socket, created_at, updated_at
		FROM repo_ci_ref_rules
		WHERE repo_id = ? AND ref_type = ? AND ref_name = ?
	`, repoID, refType, refName))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return rule, err
}

func (db *DB) ListRepoCIRefRules(ctx context.Context, repoID string) (rules []models.RepoCIRefRule, err error) {
	rows, err := db.QueryContext(ctx, `
		SELECT repo_id, ref_type, ref_name, auto_run, allow_secrets,
		       allow_docker_socket, created_at, updated_at
		FROM repo_ci_ref_rules
		WHERE repo_id = ?
		ORDER BY ref_type ASC, ref_name ASC
	`, repoID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	for rows.Next() {
		rule, err := scanRepoCIRefRule(rows)
		if err != nil {
			return nil, err
		}
		rules = append(rules, *rule)
	}
	return rules, rows.Err()
}

func (db *DB) DeleteRepoCIRefRule(ctx context.Context, repoID, refType, refName string) error {
	if err := validateCIRefRule(refType, refName); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `
		DELETE FROM repo_ci_ref_rules
		WHERE repo_id = ? AND ref_type = ? AND ref_name = ?
	`, repoID, refType, refName)
	return err
}
