// deployment.go describes what has shipped where. A deployment is
// recorded when a run with a target passes; it is never changed or
// removed afterwards, not even when retention prunes the run that made
// it.

package ci

import (
	"github.com/mmrzaf/gitman/internal/git"
	"time"
)

// Deployment is one shipped version.
type Deployment struct {
	RepoID  string
	Target  string
	Version string
	Commit  string
	// SourceKind and SourceName identify the ref that ran the deployment.
	// They remain available after the originating run is pruned.
	SourceKind git.Kind
	SourceName string
	// RunNumber is zero when the run that shipped it has since been
	// removed by retention.
	RunNumber int64
	Person    string
	At        time.Time
}
