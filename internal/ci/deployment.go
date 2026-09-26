// deployment.go describes what has shipped where. A deployment is
// recorded when a run with a target passes; it is never changed or
// removed afterwards, not even when retention prunes the run that made
// it.

package ci

import "time"

// Deployment is one shipped version.
type Deployment struct {
	RepoID  string
	Target  string
	Version string
	Commit  string
	// RunNumber is zero when the run that shipped it has since been
	// removed by retention.
	RunNumber int64
	Person    string
	At        time.Time
}
