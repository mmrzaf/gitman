package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/mmrzaf/gitman/internal/git"
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
	if err := validateCIRefRuleName(refName); err != nil {
		return err
	}
	return nil
}

func validateCIRefRuleName(refName string) error {
	refName = strings.TrimSpace(refName)
	if refName == "" {
		return fmt.Errorf("CI ref rule name is required")
	}
	if strings.ContainsAny(refName, "\x00\r\n") {
		return fmt.Errorf("CI ref rule name contains unsupported control characters")
	}
	if strings.ContainsAny(refName, "*?[") {
		if _, err := path.Match(refName, "test"); err != nil {
			return fmt.Errorf("invalid CI ref rule pattern %q: %w", refName, err)
		}
		return nil
	}
	if err := git.ValidateRefName(refName); err != nil {
		return fmt.Errorf("invalid CI ref rule name %q: %w", refName, err)
	}
	return nil
}

func isCIRefRulePattern(refName string) bool {
	return strings.ContainsAny(refName, "*?[")
}

func ciRefRuleSpecificity(refName string) int {
	score := 0
	for _, r := range refName {
		switch r {
		case '*', '?', '[', ']':
			score--
		default:
			score++
		}
	}
	return score
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

func (db *DB) MatchRepoCIRefRule(ctx context.Context, repoID, refType, refName string) (*models.RepoCIRefRule, error) {
	if refType != "branch" && refType != "tag" {
		return nil, fmt.Errorf("invalid CI ref rule type %q", refType)
	}
	if err := git.ValidateRefName(refName); err != nil {
		return nil, fmt.Errorf("invalid CI ref %q: %w", refName, err)
	}

	rule, err := db.GetRepoCIRefRule(ctx, repoID, refType, refName)
	if err != nil || rule != nil {
		return rule, err
	}

	rules, err := db.ListRepoCIRefRules(ctx, repoID)
	if err != nil {
		return nil, err
	}
	var matches []models.RepoCIRefRule
	for _, candidate := range rules {
		if candidate.RefType != refType || !isCIRefRulePattern(candidate.RefName) {
			continue
		}
		matched, err := path.Match(candidate.RefName, refName)
		if err != nil {
			return nil, fmt.Errorf("invalid stored CI ref rule pattern %q: %w", candidate.RefName, err)
		}
		if matched {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 0 {
		return nil, nil
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return ciRefRuleSpecificity(matches[i].RefName) > ciRefRuleSpecificity(matches[j].RefName)
	})
	return &matches[0], nil
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
