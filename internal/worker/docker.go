package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Docker runs steps as containers through the docker command-line
// client, the same client an operator uses to inspect them.
type Docker struct {
	// Binary is the docker client to run, "docker" to find it on PATH.
	Binary string
	// Socket is the Docker socket on the host, mounted into steps of a
	// pipeline that uses Docker itself.
	Socket string
	// Instance identifies the Gitman instance whose steps these are. Every
	// step container is labelled with it, and leftovers are looked for
	// only among containers carrying it, so two Gitman instances sharing
	// one Docker host never remove each other's steps.
	Instance string
}

// Container labels. runLabel carries the run a container belongs to, so
// leftovers of a crashed worker can be found and removed; instanceLabel
// carries Docker.Instance.
const (
	runLabel      = "gitman.run"
	instanceLabel = "gitman.instance"
)

// clientWaitDelay bounds how long a docker client that has been killed
// may keep its output pipes open through a child process it forked.
const clientWaitDelay = 10 * time.Second

// removeTimeout bounds a force-remove issued after the step's own context
// has already ended, so a docker daemon that never answers cannot leave
// that cleanup running forever.
const removeTimeout = 30 * time.Second

// command returns a docker client invocation bound to ctx: when ctx ends
// the client is killed, which is the only way to stop it while it is
// blocked on a daemon that has stopped answering.
func (d *Docker) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, d.Binary, args...)
	cmd.WaitDelay = clientWaitDelay
	return cmd
}

// containerSpec is one step's container.
type containerSpec struct {
	Name   string
	RunID  string
	Image  string
	Script string
	// Source and Meta are host directories mounted at /workspace and
	// /gitman.
	Source string
	Meta   string
	// Env holds the step's ordinary variables, passed on the command
	// line. Secrets are never on a command line, which other users of
	// the host can read: they reach the container through the client's
	// own environment. A secret named like an ordinary variable wins.
	Env          map[string]string
	Secrets      map[string]string
	DockerSocket bool
}

// args is the "docker run" command line for spec. MKNOD is dropped: the
// workspace is a host directory the worker reads as root afterwards, and
// a step must not be able to leave a device node there.
func (spec containerSpec) args(socket, instance string) []string {
	args := []string{"run", "--rm", "--name", spec.Name,
		"--label", runLabel + "=" + spec.RunID,
		"--label", instanceLabel + "=" + instance,
		"--cap-drop", "MKNOD",
		"--workdir", containerSource,
		"--volume", spec.Source + ":" + containerSource,
		"--volume", spec.Meta + ":" + containerMeta,
	}
	if spec.DockerSocket {
		args = append(args, "--volume", socket+":/var/run/docker.sock")
	}
	for _, key := range sortedKeys(spec.Env) {
		if _, secret := spec.Secrets[key]; !secret {
			args = append(args, "--env", key+"="+spec.Env[key])
		}
	}
	for _, key := range sortedKeys(spec.Secrets) {
		args = append(args, "--env", key)
	}
	// "--" ends the options, so the image can never be read as one.
	return append(args, "--entrypoint", "/bin/sh", "--", spec.Image, "-ec", spec.Script)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Available reports whether the Docker daemon answers. A worker checks
// this before claiming a run, so a host whose daemon is down leaves runs
// queued for other workers instead of claiming and failing every one.
func (d *Docker) Available(ctx context.Context) error {
	out, err := d.command(ctx, "version", "--format", "{{.Server.Version}}").CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker version: %s", strings.TrimSpace(lastLines(string(out), 3)))
	}
	return nil
}

// ImageExists reports whether image is present on the Docker host.
// Gitman never pulls images; a missing image is a setup problem for the
// operator, not something a run fixes by downloading.
func (d *Docker) ImageExists(ctx context.Context, image string) (bool, error) {
	out, err := d.command(ctx, "image", "inspect", "--format", "{{.Id}}", "--", image).CombinedOutput()
	if err == nil {
		return true, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && strings.Contains(strings.ToLower(string(out)), "no such image") {
		return false, nil
	}
	return false, fmt.Errorf("docker image inspect %s: %s", image, strings.TrimSpace(lastLines(string(out), 3)))
}

// Run runs one step's container, streaming its combined output to out,
// and returns the step's exit code. When ctx ends first, the docker
// client is killed and the container is removed in the background —
// stopping only the client would leave the container running — and Run
// returns ctx's error.
func (d *Docker) Run(ctx context.Context, spec containerSpec, out io.Writer) (int, error) {
	cmd := d.command(ctx, spec.args(d.Socket, d.Instance)...)
	cmd.Env = os.Environ()
	for _, key := range sortedKeys(spec.Secrets) {
		cmd.Env = append(cmd.Env, key+"="+spec.Secrets[key])
	}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		return -1, fmt.Errorf("start docker: %w", err)
	}

	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			d.remove(spec.Name)
		case <-stopped:
		}
	}()
	err := cmd.Wait()
	close(stopped)

	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return -1, fmt.Errorf("run docker: %w", err)
	}
	return 0, nil
}

// remove force-removes a container, with its own deadline: it runs when
// the step's context has already ended.
func (d *Docker) remove(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), removeTimeout)
	defer cancel()
	_ = d.command(ctx, "rm", "--force", name).Run()
}

// runContainers lists this instance's step containers on the Docker
// host, running or not, by container ID, with the run each belongs to.
func (d *Docker) runContainers(ctx context.Context) (map[string]string, error) {
	out, err := d.command(ctx, "ps", "--all",
		"--filter", "label="+runLabel,
		"--filter", "label="+instanceLabel+"="+d.Instance,
		"--format", "{{.ID}} {{.Label \""+runLabel+"\"}}").Output()
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	containers := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if containerID, runID, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			containers[containerID] = runID
		}
	}
	return containers, nil
}

// removeContainer force-removes one container.
func (d *Docker) removeContainer(ctx context.Context, containerID string) error {
	if err := d.command(ctx, "rm", "--force", containerID).Run(); err != nil {
		return fmt.Errorf("remove container %s: %w", containerID, err)
	}
	return nil
}
