package git

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// Service is one of the two Git smart-protocol services.
type Service string

const (
	UploadPack  Service = "git-upload-pack"
	ReceivePack Service = "git-receive-pack"
)

// ParseService validates a service name from a request.
func ParseService(s string) (Service, bool) {
	switch Service(s) {
	case UploadPack, ReceivePack:
		return Service(s), true
	}
	return "", false
}

func (s Service) command() string {
	return strings.TrimPrefix(string(s), "git-")
}

// transportTimeout bounds one clone, fetch or push. It is generous
// because a first clone of a large repository over a slow link is slow,
// not broken.
const transportTimeout = time.Hour

// MaxPushBytes caps the size of the pack one push may send: generous for
// even a large repository's history, while still bounding how much of a
// single push Gitman will commit memory and disk to at once.
const MaxPushBytes = 2 << 30

// TransportOptions describes one smart-protocol invocation.
type TransportOptions struct {
	// Service is the protocol service to run.
	Service Service
	// Protocol is the client's Git-Protocol header value, passed through
	// so a client asking for protocol version 2 gets it. It must already
	// have been validated with ValidProtocolHeader.
	Protocol string
	// HooksPath is the directory of hook scripts receive-pack runs.
	HooksPath string
	// Env is extra environment for the Git process and, through it, for
	// the hooks it runs.
	Env []string
}

// A real Git-Protocol header value is short (e.g. "version=2"); 256 is a
// generous sanity bound against a client sending something implausible,
// not a limit expected to matter for a well-behaved one.
var protocolHeaderPattern = regexp.MustCompile(`^[A-Za-z0-9=:.,_-]{0,256}$`)

// ValidProtocolHeader reports whether a Git-Protocol header value is safe
// to pass to Git.
func ValidProtocolHeader(v string) bool {
	return protocolHeaderPattern.MatchString(v)
}

// usesProtocolV2 reports whether the invocation will speak protocol
// version 2. Only upload-pack implements it; receive-pack always speaks
// version 0, whatever the client asked for.
func (o TransportOptions) usesProtocolV2() bool {
	if o.Service != UploadPack {
		return false
	}
	for _, part := range strings.Split(o.Protocol, ":") {
		if part == "version=2" {
			return true
		}
	}
	return false
}

func (o TransportOptions) args(repoPath string, extra ...string) []string {
	var args []string
	switch o.Service {
	case ReceivePack:
		args = append(args,
			"-c", "core.hooksPath="+o.HooksPath,
			"-c", fmt.Sprintf("receive.maxInputSize=%d", MaxPushBytes),
			"-c", "receive.advertisePushOptions=false",
		)
	case UploadPack:
		// Filters allow partial clones; reachable-object requests let a
		// worker fetch exactly the commit a run is for, by hash.
		args = append(args,
			"-c", "uploadpack.allowFilter=true",
			"-c", "uploadpack.allowReachableSHA1InWant=true",
		)
	}
	args = append(args, o.Service.command(), "--stateless-rpc")
	args = append(args, extra...)
	return append(args, repoPath)
}

func (o TransportOptions) env() []string {
	env := append([]string{}, o.Env...)
	if o.Service == UploadPack && o.Protocol != "" {
		env = append(env, "GIT_PROTOCOL="+o.Protocol)
	}
	return env
}

// AdvertiseRefs writes the response to GET /info/refs?service=...: the
// service announcement (except under protocol v2, which has none) and
// the ref advertisement.
func (r *Repo) AdvertiseRefs(ctx context.Context, o TransportOptions, w io.Writer) error {
	if !o.usesProtocolV2() {
		if _, err := io.WriteString(w, PktLine("# service="+string(o.Service)+"\n")+"0000"); err != nil {
			return err
		}
	}
	_, err := run(ctx, cmdOptions{dir: r.path, env: o.env(), stdout: w, timeout: transportTimeout},
		o.args(r.path, "--advertise-refs")...)
	return err
}

// ServeRPC runs one request of a service: it feeds the client's request
// body to Git and streams Git's response to w.
func (r *Repo) ServeRPC(ctx context.Context, o TransportOptions, body io.Reader, w io.Writer) error {
	_, err := run(ctx, cmdOptions{dir: r.path, env: o.env(), stdin: body, stdout: w, timeout: transportTimeout},
		o.args(r.path)...)
	return err
}

// PktLine frames s as one Git pkt-line: a four-digit hex length that
// counts itself, then the payload.
func PktLine(s string) string {
	return fmt.Sprintf("%04x%s", len(s)+4, s)
}
