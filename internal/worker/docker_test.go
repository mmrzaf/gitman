package worker

import (
	"context"
	"github.com/mmrzaf/gitman/internal/config"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func testSpec(t *testing.T, script string) containerSpec {
	ws, err := createWorkspace(t.TempDir(), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	return containerSpec{
		Name: "gitman-run-1-0", RunID: "run-1", Image: "alpine:3.20", Script: script,
		Source: ws.source(), Meta: ws.meta(),
		Env:     map[string]string{"STAGE": "test", "SHARED": "from pipeline"},
		Secrets: map[string]string{"DEPLOY_TOKEN": "hunter2-secret", "SHARED": "from secret"},
	}
}

func TestRunPassesEnvironmentAndKeepsSecretsOffTheCommandLine(t *testing.T) {
	fake := newFakeDocker(t)
	spec := testSpec(t, `echo "stage=$STAGE token=$DEPLOY_TOKEN shared=$SHARED"; echo "$STAGE" > seen.txt`)
	var out strings.Builder
	code, err := fake.docker.Run(context.Background(), spec, &out)
	if err != nil || code != 0 {
		t.Fatalf("Run = %d, %v\n%s", code, err, out.String())
	}
	if got := strings.TrimSpace(out.String()); got != "stage=test token=hunter2-secret shared=from secret" {
		t.Fatalf("step output = %q", got)
	}
	if data, _ := os.ReadFile(filepath.Join(spec.Source, "seen.txt")); string(data) != "test\n" {
		t.Fatalf("step did not run in the mounted workspace: %q", data)
	}
	args := fake.runCalls(t)[0]
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "hunter2-secret") || strings.Contains(joined, "from secret") {
		t.Fatalf("a secret value is on the command line: %s", joined)
	}
	for _, want := range []string{"--memory 2048m", "--cpus 2", "--pids-limit 256", "--pull never", "--memory-swap 2048m", "--label gitman.run=run-1", "--env STAGE=test", "--env DEPLOY_TOKEN", "--env SHARED", "--entrypoint /bin/sh"} {
		if !strings.Contains(joined, want) {
			t.Errorf("command line lacks %q: %s", want, joined)
		}
	}
	if slices.Contains(args, "SHARED=from pipeline") {
		t.Error("a pipeline variable shadowed by a secret was still passed")
	}
	if strings.Contains(joined, "docker.sock") {
		t.Error("the Docker socket was mounted into a step that did not ask for it")
	}
}

func TestRunMountsTheSocketOnlyWhenAllowed(t *testing.T) {
	fake := newFakeDocker(t)
	spec := testSpec(t, "true")
	spec.DockerSocket = true
	if code, err := fake.docker.Run(context.Background(), spec, &strings.Builder{}); err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	if !strings.Contains(strings.Join(fake.runCalls(t)[0], " "), "--volume /var/run/docker.sock:/var/run/docker.sock") {
		t.Fatal("the Docker socket was not mounted")
	}
}

func TestRunReturnsTheExitCode(t *testing.T) {
	fake := newFakeDocker(t)
	code, err := fake.docker.Run(context.Background(), testSpec(t, "echo failing; exit 3"), &strings.Builder{})
	if err != nil || code != 3 {
		t.Fatalf("Run = %d, %v; want exit code 3", code, err)
	}
}

func TestRunRemovesTheContainerWhenStopped(t *testing.T) {
	fake := newFakeDocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := fake.docker.Run(ctx, testSpec(t, "sleep 30"), &strings.Builder{})
	if err == nil {
		t.Fatal("expected Run to report that it was stopped")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("Run did not return promptly after being stopped")
	}
	var removed bool
	for _, c := range fake.calls(t) {
		if c[0] == "rm" && c[len(c)-1] == "gitman-run-1-0" {
			removed = true
		}
	}
	if !removed {
		t.Fatal("the container was not removed")
	}
}

// TestRunEndsPromptlyAgainstAWedgedDaemon is a Docker daemon that has
// stopped answering: every client command blocks, including the
// "docker rm --force" Run starts once its context ends. Run must still
// return, bounded by killing its own client, not by the daemon.
func TestRunEndsPromptlyAgainstAWedgedDaemon(t *testing.T) {
	fake := newFakeDocker(t)
	t.Setenv("FAKE_DOCKER_HANG", "all")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := fake.docker.Run(ctx, testSpec(t, "true"), &strings.Builder{})
	if err == nil {
		t.Fatal("expected Run to report that it was stopped")
	}
	if elapsed := time.Since(start); elapsed > removeTimeout+clientWaitDelay+5*time.Second {
		t.Fatalf("Run took %s against an unresponsive daemon", elapsed)
	}
}

// TestImageExistsEndsWithItsContext is the pre-run image check against a
// daemon that has stopped answering: it must end when its context does.
func TestImageExistsEndsWithItsContext(t *testing.T) {
	fake := newFakeDocker(t, "alpine:3.20")
	t.Setenv("FAKE_DOCKER_HANG", "image")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := fake.docker.ImageExists(ctx, "alpine:3.20"); err == nil {
		t.Fatal("expected an error once the context ended")
	}
	if elapsed := time.Since(start); elapsed > removeTimeout+clientWaitDelay+5*time.Second {
		t.Fatalf("ImageExists took %s against an unresponsive daemon", elapsed)
	}
}

func TestAvailable(t *testing.T) {
	fake := newFakeDocker(t)
	if err := fake.docker.Available(context.Background()); err != nil {
		t.Fatalf("Available with a running daemon = %v", err)
	}
	t.Setenv("FAKE_DOCKER_DOWN", "1")
	if err := fake.docker.Available(context.Background()); err == nil || !strings.Contains(err.Error(), "Cannot connect") {
		t.Fatalf("Available with the daemon down = %v", err)
	}
}

// TestContainersCarryTheInstance labels every step with its instance and
// run, drops MKNOD, and looks for leftovers only among this instance's
// containers, so instances sharing a Docker host leave each other alone.
func TestContainersCarryTheInstance(t *testing.T) {
	fake := newFakeDocker(t)
	if _, err := fake.docker.Run(context.Background(), testSpec(t, "true"), &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(fake.runCalls(t)[0], " ")
	for _, want := range []string{"--label gitman.run=run-1", "--label gitman.instance=test-instance", "--cap-drop MKNOD"} {
		if !strings.Contains(joined, want) {
			t.Errorf("run command line lacks %q: %s", want, joined)
		}
	}
	if _, err := fake.docker.runContainers(context.Background()); err != nil {
		t.Fatal(err)
	}
	ps := fake.CallsTo(t, "ps")
	if len(ps) != 1 || !slices.Contains(ps[0], "label=gitman.instance=test-instance") {
		t.Fatalf("ps = %q; want it filtered to this instance", ps)
	}
}

func TestImageExists(t *testing.T) {
	fake := newFakeDocker(t, "alpine:3.20")
	if ok, err := fake.docker.ImageExists(context.Background(), "alpine:3.20"); err != nil || !ok {
		t.Fatalf("ImageExists(present) = %v, %v", ok, err)
	}
	if ok, err := fake.docker.ImageExists(context.Background(), "missing:1"); err != nil || ok {
		t.Fatalf("ImageExists(missing) = %v, %v", ok, err)
	}
}

// TestRemoveLeftovers removes the containers and workspaces of runs that
// are not running, and never those of a run claimed while it looks: one
// that appears after the listing is left alone even though the running
// set was read before its claim was visible.
func TestRemoveLeftovers(t *testing.T) {
	fake := newFakeDocker(t)
	root := t.TempDir()
	for _, runID := range []string{"run-live", "run-dead"} {
		if _, err := createWorkspace(root, runID); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("FAKE_DOCKER_PS", "c1 run-live\nc2 run-dead\n")
	runningRuns := func(context.Context) (map[string]bool, error) {
		// A run claimed right now: its container and workspace appear, but
		// the set of running runs was read a moment before its claim.
		os.Setenv("FAKE_DOCKER_PS", "c1 run-live\nc2 run-dead\nc3 run-new\n")
		if _, err := createWorkspace(root, "run-new"); err != nil {
			t.Fatal(err)
		}
		return map[string]bool{"run-live": true}, nil
	}
	containers, workspaces, err := RemoveLeftovers(context.Background(), fake.docker, root, runningRuns, func(context.Context, string, string, int) error { return nil }, cleanupConfirmed)
	if err != nil || containers != 1 || workspaces != 1 {
		t.Fatalf("removeLeftovers = %d containers, %d workspaces, %v", containers, workspaces, err)
	}
	var rms []string
	for _, c := range fake.calls(t) {
		if c[0] == "rm" {
			rms = append(rms, c[len(c)-1])
		}
	}
	if len(rms) != 1 || rms[0] != "c2" {
		t.Fatalf("removed containers %v, want only c2", rms)
	}
	for runID, want := range map[string]bool{"run-live": true, "run-dead": false, "run-new": true} {
		if _, err := os.Stat(filepath.Join(root, runID)); (err == nil) != want {
			t.Errorf("workspace %s exists = %v, want %v", runID, err == nil, want)
		}
	}
	if c, w, err := RemoveLeftovers(context.Background(), fake.docker, filepath.Join(root, "missing"), runningRuns, func(context.Context, string, string, int) error { return nil }, cleanupConfirmed); err != nil || w != 0 {
		t.Fatalf("a missing workspace root = %d, %d, %v", c, w, err)
	}
}

// TestRunArgsEndOptionsBeforeTheImage keeps the image a positional
// argument whatever it says: every option comes before "--".
func TestRunArgsEndOptionsBeforeTheImage(t *testing.T) {
	spec := containerSpec{Name: "gitman-r-0", RunID: "r", Image: "alpine:3.20", Script: "true",
		Source: "/w/src", Meta: "/w/meta", Env: map[string]string{"A": "1"}, Secrets: map[string]string{"S": "x"}}
	args := spec.args("/var/run/docker.sock", "instance")
	i := slices.Index(args, "--")
	if i < 0 || i+1 >= len(args) || args[i+1] != spec.Image || slices.Index(args[i+1:], "--") >= 0 {
		t.Fatalf("args = %q; want every option, then --, then the image", args)
	}
}

func TestExitReceiptIsRetainedUntilRecorded(t *testing.T) {
	fake := newFakeDocker(t)
	spec := testSpec(t, "exit 7")
	persisted := false
	spec.Created = func(ctx context.Context, id string) error {
		if len(fake.CallsTo(t, "start")) != 0 {
			t.Fatal("container started before persistence")
		}
		persisted = id != ""
		return nil
	}
	code, err := fake.docker.Run(t.Context(), spec, &strings.Builder{})
	if err != nil || code != 7 || !persisted {
		t.Fatalf("Run = %d, %v, persisted %v", code, err, persisted)
	}
	if len(fake.CallsTo(t, "rm")) != 0 {
		t.Fatal("execution receipt was auto-removed")
	}
	state, err := fake.docker.state(t.Context(), spec.Name)
	if err != nil || state.Running || state.ExitCode != 7 {
		t.Fatalf("receipt = %+v, %v", state, err)
	}
}

func TestLeftoverCleanupRetainsWorkspaceWhenRemovalFails(t *testing.T) {
	fake := newFakeDocker(t)
	root := t.TempDir()
	if _, err := createWorkspace(root, "ended-run"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_DOCKER_PS", "container ended-run\n")
	t.Setenv("FAKE_DOCKER_RM_FAIL", "1")
	_, n, err := RemoveLeftovers(t.Context(), fake.docker, root, func(context.Context) (map[string]bool, error) { return map[string]bool{}, nil }, func(context.Context, string, string, int) error { return nil }, cleanupConfirmed)
	if err == nil || n != 0 {
		t.Fatalf("cleanup = %d, %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(root, "ended-run")); err != nil {
		t.Fatal("mounted workspace was removed")
	}
}

func TestLeftoverCleanupRespectsLiveExecutionOwnership(t *testing.T) {
	fake := newFakeDocker(t)
	root := t.TempDir()
	if _, err := createWorkspace(root, "ended-run"); err != nil {
		t.Fatal(err)
	}
	unlock, locked, err := executionLock(root, "ended-run")
	if err != nil || !locked {
		t.Fatalf("lock = %v, %v", locked, err)
	}
	defer unlock()
	t.Setenv("FAKE_DOCKER_PS", "container ended-run\n")
	c, n, err := RemoveLeftovers(t.Context(), fake.docker, root, func(context.Context) (map[string]bool, error) { return map[string]bool{}, nil }, func(context.Context, string, string, int) error { return nil }, cleanupConfirmed)
	if err != nil || c != 0 || n != 0 {
		t.Fatalf("cleanup touched live execution: %d, %d, %v", c, n, err)
	}
}

func cleanupConfirmed(context.Context, string) error { return nil }

func TestStepUsesOperatorResourceProfile(t *testing.T) {
	fake := newFakeDocker(t)
	fake.docker.Resources = config.Resources{MemoryMiB: 8192, CPUs: 8, PIDs: 1024, WorkspaceGiB: 100, DiskReserveGiB: 20}
	if _, err := fake.docker.Run(t.Context(), testSpec(t, "true"), &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(fake.runCalls(t)[0], " ")
	for _, want := range []string{"--memory 8192m", "--memory-swap 8192m", "--cpus 8", "--pids-limit 1024"} {
		if !strings.Contains(args, want) {
			t.Errorf("args lack %s: %s", want, args)
		}
	}
}
