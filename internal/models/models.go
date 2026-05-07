package models

import "time"

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
	User        User      `json:"user"`
	AccessLevel string    `json:"access_level"`
	CreatedAt   time.Time `json:"created_at"`
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
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Name      string    `json:"name"`
	PublicKey string    `json:"public_key"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CIRun represents a single CI/CD pipeline execution.
// Status values: pending | running | success | failed | skipped
type CIRun struct {
	ID          string     `json:"id"`
	RepoID      string     `json:"repo_id"`
	CommitHash  string     `json:"commit_hash"`
	Branch      string     `json:"branch"`
	Tag         string     `json:"tag"`
	Event       string     `json:"event"`
	Status      string     `json:"status"`
	LogFile     string     `json:"log_file"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// RepoSecret represents an encrypted key/value pair for CI environment injection.
type RepoSecret struct {
	ID             string    `json:"id"`
	RepoID         string    `json:"repo_id"`
	Key            string    `json:"key"`
	EncryptedValue string    `json:"-"`
	CreatedAt      time.Time `json:"created_at"`
}
