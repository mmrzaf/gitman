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

// AccessToken represents a personal access token for Git HTTP auth.
type AccessToken struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Name      string    `json:"name"`
	TokenHash string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
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

// CIRun represents a single CI/CD pipeline execution.
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
