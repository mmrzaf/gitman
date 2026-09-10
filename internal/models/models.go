package models

import "time"

type AccessLevel string

const (
	AccessRead  AccessLevel = "read"
	AccessWrite AccessLevel = "write"
)

func (level AccessLevel) Valid() bool { return level == AccessRead || level == AccessWrite }

type CIStatus string

const (
	CIStatusPending   CIStatus = "pending"
	CIStatusRunning   CIStatus = "running"
	CIStatusSuccess   CIStatus = "success"
	CIStatusFailed    CIStatus = "failed"
	CIStatusSkipped   CIStatus = "skipped"
	CIStatusCancelled CIStatus = "cancelled"
)

func (status CIStatus) Valid() bool {
	switch status {
	case CIStatusPending, CIStatusRunning, CIStatusSuccess, CIStatusFailed, CIStatusSkipped, CIStatusCancelled:
		return true
	default:
		return false
	}
}

func (status CIStatus) Terminal() bool {
	switch status {
	case CIStatusSuccess, CIStatusFailed, CIStatusSkipped, CIStatusCancelled:
		return true
	default:
		return false
	}
}

type CIEvent string

const (
	CIEventPush   CIEvent = "push"
	CIEventManual CIEvent = "manual"
	CIEventRetry  CIEvent = "retry"
)

func (event CIEvent) Valid() bool {
	return event == CIEventPush || event == CIEventManual || event == CIEventRetry
}

type CIRefType string

const (
	CIRefBranch CIRefType = "branch"
	CIRefTag    CIRefType = "tag"
)

func (refType CIRefType) Valid() bool { return refType == CIRefBranch || refType == CIRefTag }

// User represents an account in the system.
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Repository represents a git repository.
type Repository struct {
	ID          string    `json:"id"`
	OwnerID     string    `json:"owner_id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	IsPrivate   bool      `json:"is_private"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Collaborator represents a user with access level on a repository.
type Collaborator struct {
	User        User        `json:"user"`
	AccessLevel AccessLevel `json:"access_level"`
	CreatedAt   time.Time   `json:"created_at"`
}

type AccessTokenScope string

const (
	AccessTokenScopeRepoRead  AccessTokenScope = "repo:read"
	AccessTokenScopeRepoWrite AccessTokenScope = "repo:write"
)

func (scope AccessTokenScope) Valid() bool {
	return scope == AccessTokenScopeRepoRead || scope == AccessTokenScopeRepoWrite
}

func AccessTokenScopeAllows(granted, required AccessTokenScope) bool {
	if !granted.Valid() || !required.Valid() {
		return false
	}
	if granted == AccessTokenScopeRepoWrite {
		return true
	}
	return required == AccessTokenScopeRepoRead
}

// AccessToken represents a personal access token for Git HTTP and API auth.
type AccessToken struct {
	ID         string           `json:"id"`
	UserID     string           `json:"user_id"`
	Name       string           `json:"name"`
	TokenHash  string           `json:"-"`
	Scope      AccessTokenScope `json:"scope"`
	CreatedAt  time.Time        `json:"created_at"`
	ExpiresAt  *time.Time       `json:"expires_at,omitempty"`
	LastUsedAt *time.Time       `json:"last_used_at,omitempty"`
}

const (
	AuditActionLoginSucceeded      = "auth.login.succeeded"
	AuditActionLoginFailed         = "auth.login.failed"
	AuditActionUserRegistered      = "auth.user.registered"
	AuditActionPasswordReset       = "auth.password.reset"
	AuditActionUserCreated         = "admin.user.created"
	AuditActionUserDeleted         = "admin.user.deleted"
	AuditActionTokenCreated        = "auth.token.created"
	AuditActionTokenRevoked        = "auth.token.revoked"
	AuditActionSSHKeyCreated       = "auth.ssh_key.created"
	AuditActionSSHKeyDeleted       = "auth.ssh_key.deleted"
	AuditActionCollaboratorUpsert  = "repo.collaborator.upserted"
	AuditActionCollaboratorRemoved = "repo.collaborator.removed"
	AuditActionRepositoryCreated   = "repo.created"
	AuditActionRepositoryUpdated   = "repo.settings.updated"
	AuditActionRepositoryDeleted   = "repo.deleted"
	AuditActionCISecretUpserted    = "ci.secret.upserted"
	AuditActionCISecretDeleted     = "ci.secret.deleted"
	AuditActionCIRefRuleUpserted   = "ci.ref_rule.upserted"
	AuditActionCIRefRuleDeleted    = "ci.ref_rule.deleted"
)

type AuditEvent struct {
	ID            string            `json:"id"`
	ActorUserID   string            `json:"actor_user_id,omitempty"`
	ActorUsername string            `json:"actor_username,omitempty"`
	Action        string            `json:"action"`
	TargetType    string            `json:"target_type,omitempty"`
	TargetID      string            `json:"target_id,omitempty"`
	SourceIP      string            `json:"source_ip,omitempty"`
	RequestID     string            `json:"request_id,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
}

// SSHKey represents an SSH public key attached to a user.
type SSHKey struct {
	ID          string    `json:"id"`
	UserID      string    `json:"user_id"`
	Name        string    `json:"name"`
	PublicKey   string    `json:"public_key"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// CIRun represents a single CI pipeline execution.
// Status values: pending | running | success | failed | skipped | cancelled
type CIRun struct {
	ID           string     `json:"id"`
	RepoID       string     `json:"repo_id"`
	CommitHash   string     `json:"commit_hash"`
	Branch       string     `json:"branch"`
	Tag          string     `json:"tag"`
	Event        CIEvent    `json:"event"`
	Status       CIStatus   `json:"status"`
	LogFile      string     `json:"log_file"`
	CancelReason string     `json:"cancel_reason"`
	StatusReason string     `json:"status_reason"`
	RetryOfRunID string     `json:"retry_of_run_id,omitempty"`
	AttemptID    string     `json:"attempt_id,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	HeartbeatAt  *time.Time `json:"heartbeat_at,omitempty"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
}

// CIWorker reports the durable liveness/admission state of one worker process.
// A stale heartbeat means the process should be treated as offline even if its
// last recorded healthy flag was true.
const CIWorkerStaleAfter = 45 * time.Second

type CIWorker struct {
	ID            string     `json:"id"`
	Hostname      string     `json:"hostname"`
	PID           int        `json:"pid"`
	Concurrency   int        `json:"concurrency"`
	Healthy       bool       `json:"healthy"`
	StatusMessage string     `json:"status_message,omitempty"`
	ActiveJobs    int        `json:"active_jobs"`
	StartedAt     time.Time  `json:"started_at"`
	HeartbeatAt   time.Time  `json:"heartbeat_at"`
	StoppedAt     *time.Time `json:"stopped_at,omitempty"`
}

// RepoCIRefRule stores per-repository trust settings for one exact CI ref or glob ref pattern.
type RepoCIRefRule struct {
	RepoID            string    `json:"repo_id"`
	RefType           CIRefType `json:"ref_type"`
	RefName           string    `json:"ref_name"`
	AutoRun           bool      `json:"auto_run"`
	AllowSecrets      bool      `json:"allow_secrets"`
	AllowDockerSocket bool      `json:"allow_docker_socket"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// RepoSecret represents an encrypted key/value pair for CI environment injection.
type RepoSecret struct {
	ID             string    `json:"id"`
	RepoID         string    `json:"repo_id"`
	Key            string    `json:"key"`
	EncryptedValue string    `json:"-"`
	CreatedAt      time.Time `json:"created_at"`
}
