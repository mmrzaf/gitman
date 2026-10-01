// Package dockertest gives tests a fake docker client: a shell script
// that records how it was called and emulates the handful of commands a
// worker uses, so worker code runs for real without a Docker daemon and
// without ever touching one that happens to be on the machine. It is
// imported only by tests, so the testing package never reaches the
// gitman binary.
package dockertest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// script emulates the docker client. It records each invocation's
// arguments, one per line, in a numbered file under $FAKE_DOCKER_DIR, and:
//
//   - version: succeeds, unless FAKE_DOCKER_DOWN is set.
//   - image inspect: succeeds for images listed in FAKE_DOCKER_IMAGES.
//   - run: executes the step script with /bin/sh in the host directory
//     mounted at /workspace, with GITMAN_SUMMARY pointing at the host
//     file mounted at /gitman/summary, and every --env variable set —
//     KEY=VALUE from the argument, bare KEY from the client's own
//     environment, as the real client does.
//   - rm --force NAME: kills the step started under that name, the way
//     removing a container ends the client attached to it.
//   - ps: prints FAKE_DOCKER_PS.
//
// FAKE_DOCKER_DOWN makes every command fail the way the real client does
// when its daemon is unreachable. FAKE_DOCKER_HANG names a command
// ("image", "ps", "run", or "all") that never returns, the way the real
// client blocks on a daemon that has stopped responding.
const script = `#!/bin/sh
dir="$FAKE_DOCKER_DIR"
while ! mkdir "$dir/call-lock" 2>/dev/null; do sleep 0.01; done
n=$(find "$dir" -maxdepth 1 -type f -name "call-*" | wc -l)
printf '%s\n' "$@" > "$dir/call-$(printf %03d "$n")"
rmdir "$dir/call-lock"
if [ "$FAKE_DOCKER_HANG" = "$1" ] || [ "$FAKE_DOCKER_HANG" = all ]; then
	exec sleep 300
fi
if [ -n "$FAKE_DOCKER_DOWN" ]; then
	echo "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?" >&2
	exit 1
fi
case "$1" in
version) echo 29.0.0; exit 0 ;;
info) echo fake-engine; exit 0 ;;
image)
 if [ "$2" = ls ]; then printf "%s\n" $FAKE_DOCKER_IMAGES; exit 0; fi
	eval "wanted=\${$#}"
	for image in $FAKE_DOCKER_IMAGES; do
		[ "$image" = "$wanted" ] && { echo sha256:abc; exit 0; }
	done
	echo "Error: No such image: $wanted" >&2; exit 1 ;;
ps)
 full=0
 for arg in "$@"; do [ "$arg" != --no-trunc ] || full=1; done
 if [ "$full" = 1 ]; then printf '%s' "$FAKE_DOCKER_PS"
 else printf '%s' "$FAKE_DOCKER_PS" | awk '{ if (length($1) == 64) $1=substr($1,1,12); print }'
 fi
 exit 0 ;;
kill)
 if [ -f "$dir/pid-$2" ]; then
  pid=$(cat "$dir/pid-$2")
  kill -9 $(ps -o pid= --ppid "$pid") "$pid" 2>/dev/null
 fi
 echo 137 > "$dir/exit-$2"
 exit 0 ;;
rm)
 if [ -n "$FAKE_DOCKER_RM_FAIL" ]; then echo "daemon unavailable" >&2; exit 1; fi
 if [ -f "$dir/pid-$3" ]; then
  pid=$(cat "$dir/pid-$3")
  kill -9 $(ps -o pid= --ppid "$pid") "$pid" 2>/dev/null
 fi
 rm -f "$dir/config-$3" "$dir/exit-$3" "$dir/pid-$3"
 exit 0 ;;
inspect)
 eval "name=\${$#}"
 if [ -f "$dir/exit-$name" ]; then
  printf '{"Running":false,"Status":"exited","ExitCode":%s}\n' "$(cat "$dir/exit-$name")"
 elif [ -f "$dir/config-$name" ]; then
  echo '{"Running":true,"Status":"running","ExitCode":0}'
 else
  echo "No such container: $name" >&2; exit 1
 fi
 exit 0 ;;
create)
	shift
	name=""; src=""; meta=""; script=""
	envfile="$dir/env-$$"; : > "$envfile"
	while [ $# -gt 0 ]; do
		case "$1" in
		--name) name="$2"; shift 2 ;;
		--volume)
			case "$2" in
			*:/workspace) src="${2%:/workspace}" ;;
			*:/gitman) meta="${2%:/gitman}" ;;
			esac
			shift 2 ;;
		--env)
			case "$2" in
			*=*) printf 'export %s\n' "$(printf '%s' "$2" | sed "s/=/='/; s/\$/'/")" >> "$envfile" ;;
			*) eval "v=\$$2"; printf "export %s='%s'\n" "$2" "$v" >> "$envfile" ;;
			esac
			shift 2 ;;
		-ec) script="$2"; shift 2 ;;
		*) shift ;;
		esac
	done

 # Save the creation spec. Secrets arrive through the create client's own
 # environment, then stay in the container configuration for start.
 printf "src='%s'\nmeta='%s'\nenvfile='%s'\n" "$src" "$meta" "$envfile" > "$dir/config-$name"
 printf '%s' "$script" > "$dir/script-$name"
 echo "$name"
 exit 0 ;;
start)
 eval "name=\${$#}"
 . "$dir/config-$name" || exit 125
 script=$(cat "$dir/script-$name")
 cd "$src" || exit 125
 (
  . "$envfile"
  export GITMAN_SUMMARY="$meta/summary"
  exec /bin/sh -ec "$script"
 ) &
 pid=$!
 echo "$pid" > "$dir/pid-$name"
 wait "$pid"
 code=$?
 echo "$code" > "$dir/exit-$name"
 exit "$code" ;;
esac
exit 0
`

// Fake is one fake docker client, private to the test that made it.
type Fake struct {
	// Binary is the path to pass as the docker client.
	Binary string
	dir    string
}

// New writes a fake docker client that reports images as present. It
// sets its configuration in the environment with t.Setenv, so a test
// using it cannot run in parallel with another.
func New(t testing.TB, images ...string) *Fake {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "docker")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_DOCKER_DIR", dir)
	t.Setenv("FAKE_DOCKER_IMAGES", strings.Join(images, " "))
	t.Setenv("FAKE_DOCKER_PS", "")
	t.Setenv("FAKE_DOCKER_DOWN", "")
	t.Setenv("FAKE_DOCKER_HANG", "")
	t.Setenv("FAKE_DOCKER_RM_FAIL", "")
	return &Fake{Binary: bin, dir: dir}
}

// Calls returns every recorded invocation's arguments, oldest first.
func (f *Fake) Calls(t testing.TB) [][]string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(f.dir, "call-*"))
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			t.Fatal(err)
		}
		calls = append(calls, strings.Split(strings.TrimRight(string(data), "\n"), "\n"))
	}
	return calls
}

// CallsTo returns the recorded invocations of one docker command, such
// as "run" or "rm".
func (f *Fake) CallsTo(t testing.TB, command string) [][]string {
	t.Helper()
	var matching [][]string
	for _, c := range f.Calls(t) {
		if c[0] == command {
			matching = append(matching, c)
		}
	}
	return matching
}
