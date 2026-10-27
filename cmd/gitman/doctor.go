package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/worker"
)

// errDoctorFailed is silent: each failing check already printed what is
// wrong and what to do, so main must not add a second line.
var errDoctorFailed = errors.New("doctor found failures")

type doctor struct {
	out    io.Writer
	failed bool
}

func (d *doctor) ok(name, format string, args ...any) {
	fmt.Fprintf(d.out, "ok    %s: %s\n", name, fmt.Sprintf(format, args...))
}

func (d *doctor) warn(name, format string, args ...any) {
	fmt.Fprintf(d.out, "warn  %s: %s\n", name, fmt.Sprintf(format, args...))
}

func (d *doctor) fail(name, format string, args ...any) {
	d.failed = true
	fmt.Fprintf(d.out, "fail  %s: %s\n", name, fmt.Sprintf(format, args...))
}

// runDoctor checks a live installation without changing it: no migration,
// no storage binding, no owner lock and no workspace preparation. It is
// safe beside a running web or worker.
func runDoctor(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: gitman doctor")
	}
	ctx, stop := signalContext()
	defer stop()
	return doctorTo(ctx, os.Stdout)
}

func doctorTo(ctx context.Context, out io.Writer) error {
	d := &doctor{out: out}
	cfg, err := config.Load()
	if err != nil {
		d.fail("config", "%v", err)
		return errDoctorFailed
	}
	d.ok("config", "loaded from the environment")

	checkDataDir(d, cfg)
	checkDatabase(ctx, d, cfg)
	checkDocker(ctx, d)
	checkWarnings(d, cfg)
	if d.failed {
		return errDoctorFailed
	}
	return nil
}

func checkDataDir(d *doctor, cfg *config.Config) {
	info, err := os.Stat(cfg.DataDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			d.fail("data dir", "%s does not exist; create it or set %s", cfg.DataDir, config.EnvDataDir)
		} else {
			d.fail("data dir", "cannot read %s: %v", cfg.DataDir, err)
		}
		return
	}
	if !info.IsDir() {
		d.fail("data dir", "%s is not a directory", cfg.DataDir)
		return
	}
	// access(2)'s W_OK. Go's syscall package does not export it.
	const writeAccess = 0x2
	if err := syscall.Access(cfg.DataDir, writeAccess); err != nil {
		d.fail("data dir", "%s is not writable: %v", cfg.DataDir, err)
		return
	}
	d.ok("data dir", "%s exists and is writable", cfg.DataDir)
	for _, name := range []string{"repos", "hooks", "workspaces"} {
		path := filepath.Join(cfg.DataDir, name)
		info, err := os.Stat(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			d.warn(name, "%s does not exist yet; Gitman creates it when needed", path)
		case err != nil:
			d.fail(name, "cannot read %s: %v", path, err)
		case !info.IsDir():
			d.fail(name, "%s is not a directory", path)
		default:
			d.ok(name, "%s is present", path)
		}
	}
	reserve := uint64(cfg.Resources.DiskReserveGiB) << 30
	if err := worker.CheckReserve(cfg.DataDir, reserve); err != nil {
		d.fail("disk reserve", "less than %d GiB free (%v); free space or lower %s", cfg.Resources.DiskReserveGiB, err, config.EnvDiskReserveGiB)
	} else {
		d.ok("disk reserve", "at least %d GiB free", cfg.Resources.DiskReserveGiB)
	}
}

func checkDatabase(ctx context.Context, d *doctor, cfg *config.Config) {
	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	database, err := postgres.Connect(connectCtx, cfg.DatabaseURL, postgres.Options{MaxConns: 4, AcquireTimeout: 5 * time.Second})
	if err != nil {
		d.fail("database", "%v; check %s", err, config.EnvDatabaseURL)
		return
	}
	defer database.Close()
	d.ok("database", "connected")

	status, err := database.MigrationStatus(connectCtx)
	if err != nil {
		d.fail("schema", "%v; run gitman admin migrate", err)
		return
	}
	switch {
	case len(status.DatabaseAhead) > 0:
		d.fail("schema", "database is ahead of this binary by migration %d (%s); run a newer Gitman or restore a matching backup",
			status.DatabaseAhead[0].Version, status.DatabaseAhead[0].Name)
	case len(status.Pending) > 0:
		d.warn("schema", "%d migration(s) pending; run gitman admin migrate", len(status.Pending))
	default:
		d.ok("schema", "all %d migration(s) applied", len(status.Applied))
	}

	workers, err := ci.NewService(database).Workers(connectCtx)
	if err != nil {
		d.fail("workers", "%v", err)
		return
	}
	if len(workers) == 0 {
		d.warn("workers", "no worker has registered; queued runs wait")
		return
	}
	ready := 0
	for _, w := range workers {
		if w.Ready {
			ready++
		}
	}
	if ready == 0 {
		reason := workers[0].Reason
		if reason == "" {
			reason = "not ready"
		}
		d.warn("workers", "%d registered, none ready (%s)", len(workers), reason)
	} else {
		d.ok("workers", "%d registered, %d ready", len(workers), ready)
	}
}

func checkDocker(ctx context.Context, d *doctor) {
	path, err := exec.LookPath("docker")
	if err != nil {
		d.warn("docker", "client not on PATH; a worker on this host cannot run pipelines")
		return
	}
	d.ok("docker", "client at %s", path)
	const socket = "/var/run/docker.sock"
	if info, err := os.Stat(socket); err != nil {
		d.warn("docker", "%s is unavailable: %v", socket, err)
		return
	} else if info.Mode()&os.ModeSocket == 0 {
		d.warn("docker", "%s is not a socket", socket)
		return
	}
	client := &worker.Docker{Binary: path, Socket: socket}
	if err := client.Available(ctx); err != nil {
		d.warn("docker", "daemon is not answering: %v", err)
		return
	}
	engine, err := client.EngineID(ctx)
	if err != nil {
		d.warn("docker", "daemon answered but its identity is unavailable: %v", err)
		return
	}
	label := engine
	if len(label) > 12 {
		label = label[:12]
	}
	d.ok("docker", "daemon %s is available", label)
}

func checkWarnings(d *doctor, cfg *config.Config) {
	if cfg.SecretKey == "" {
		d.warn("secrets", "%s is empty; repository secrets are disabled", config.EnvSecretKey)
	} else {
		d.ok("secrets", "%s is configured", config.EnvSecretKey)
	}
	public, err := url.Parse(cfg.PublicURL)
	if err == nil {
		host := strings.ToLower(public.Hostname())
		if host == "localhost" || host == "127.0.0.1" || host == "::1" {
			d.warn("public URL", "%s names localhost; set %s to the address people and Git use", cfg.PublicURL, config.EnvPublicURL)
		} else if public.Scheme == "http" {
			d.warn("public URL", "%s is plain HTTP; put HTTPS in front before exposing it", cfg.PublicURL)
		} else {
			d.ok("public URL", "%s", cfg.PublicURL)
		}
	}
	if len(cfg.TrustedProxies) == 0 {
		d.warn("trusted proxies", "%s is empty; set it to the reverse proxy's address if one is in front", config.EnvTrustedProxies)
	} else {
		d.ok("trusted proxies", "%d configured", len(cfg.TrustedProxies))
	}
}
