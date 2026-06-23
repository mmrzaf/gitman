package ci

import (
	"context"
	"fmt"

	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
)

const (
	PolicySourceDefault = "default-policy"
	PolicySourceRule    = "explicit-rule"
)

type RefPolicy struct {
	AutoRun           bool
	AllowSecrets      bool
	AllowDockerSocket bool
	Source            string
	RefType           string
	RefName           string
	RuleRefName       string
}

type Resolver struct {
	DB        *db.DB
	ReposPath string
}

func (r Resolver) Resolve(ctx context.Context, owner *models.User, repo *models.Repository, branch, tag string) (RefPolicy, error) {
	if branch == "" && tag == "" {
		return RefPolicy{}, fmt.Errorf("CI run has no branch or tag")
	}
	if branch != "" && tag != "" {
		return RefPolicy{}, fmt.Errorf("CI run targets both branch and tag")
	}

	refType, refName := "branch", branch
	if tag != "" {
		refType, refName = "tag", tag
	}
	if err := git.ValidateRefName(refName); err != nil {
		return RefPolicy{}, fmt.Errorf("invalid CI ref: %w", err)
	}

	rule, err := r.DB.MatchRepoCIRefRule(ctx, repo.ID, refType, refName)
	if err != nil {
		return RefPolicy{}, fmt.Errorf("load CI ref rule: %w", err)
	}
	if rule != nil {
		return RefPolicy{
			AutoRun:           rule.AutoRun,
			AllowSecrets:      rule.AllowSecrets,
			AllowDockerSocket: rule.AllowDockerSocket,
			Source:            PolicySourceRule,
			RefType:           refType,
			RefName:           refName,
			RuleRefName:       rule.RefName,
		}, nil
	}

	if refType == "tag" {
		return RefPolicy{Source: PolicySourceDefault, RefType: refType, RefName: refName}, nil
	}
	repoPath, err := git.SecureRepoPath(r.ReposPath, owner.Username, repo.Name)
	if err != nil {
		return RefPolicy{}, fmt.Errorf("resolve repository path: %w", err)
	}
	defaultBranch, err := git.GetDefaultBranch(ctx, repoPath)
	if err != nil {
		return RefPolicy{}, fmt.Errorf("resolve default branch: %w", err)
	}
	if defaultBranch == "" {
		return RefPolicy{}, fmt.Errorf("repository has no default branch")
	}
	if branch == defaultBranch {
		return RefPolicy{
			AutoRun:      true,
			AllowSecrets: true,
			Source:       PolicySourceDefault,
			RefType:      refType,
			RefName:      refName,
		}, nil
	}
	return RefPolicy{Source: PolicySourceDefault, RefType: refType, RefName: refName}, nil
}
