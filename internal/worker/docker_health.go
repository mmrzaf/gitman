package worker

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

func probeDockerDaemon(ctx context.Context) error {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "docker", "info", "--format", "{{.ServerVersion}}")
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if probeCtx.Err() != nil {
		return fmt.Errorf("%w: Docker health probe: %v", errDockerUnavailable, probeCtx.Err())
	}
	return fmt.Errorf("%w: docker info: %v%s", errDockerUnavailable, err, commandOutputSuffix(output))
}
