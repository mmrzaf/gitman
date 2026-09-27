package worker

import (
	"testing"

	"github.com/mmrzaf/gitman/internal/worker/dockertest"
)

// fakeDocker is a dockertest fake with a Docker client already pointed
// at it.
type fakeDocker struct {
	*dockertest.Fake
	docker *Docker
}

func newFakeDocker(t *testing.T, images ...string) *fakeDocker {
	t.Helper()
	fake := dockertest.New(t, images...)
	return &fakeDocker{Fake: fake, docker: &Docker{Binary: fake.Binary, Socket: "/var/run/docker.sock", Instance: "test-instance"}}
}

func (f *fakeDocker) calls(t *testing.T) [][]string    { return f.Calls(t) }
func (f *fakeDocker) runCalls(t *testing.T) [][]string { return f.CallsTo(t, "run") }
