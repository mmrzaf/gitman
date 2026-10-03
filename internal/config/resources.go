package config

import "fmt"

const (
	EnvStepMemoryMiB  = "GITMAN_STEP_MEMORY_MIB"
	EnvStepCPUs       = "GITMAN_STEP_CPUS"
	EnvStepPIDs       = "GITMAN_STEP_PIDS"
	EnvWorkspaceGiB   = "GITMAN_WORKSPACE_GIB"
	EnvDiskReserveGiB = "GITMAN_DISK_RESERVE_GIB"
)

// Resources describes operator-owned limits, applied to every pipeline.
type Resources struct{ MemoryMiB, CPUs, PIDs, WorkspaceGiB, DiskReserveGiB int }

func DefaultResources() Resources { return Resources{2048, 2, 256, 10, 5} }
func (r Resources) WithDefaults() Resources {
	if r == (Resources{}) {
		return DefaultResources()
	}
	return r
}
func (r Resources) Validate() error {
	for _, setting := range []struct {
		name       string
		value, max int
	}{
		{EnvStepMemoryMiB, r.MemoryMiB, 1048576}, {EnvStepCPUs, r.CPUs, 1024},
		{EnvStepPIDs, r.PIDs, 1048576}, {EnvWorkspaceGiB, r.WorkspaceGiB, 1048576},
		{EnvDiskReserveGiB, r.DiskReserveGiB, 1048576},
	} {
		if setting.value < 1 || setting.value > setting.max {
			return fmt.Errorf("%s must be between 1 and %d", setting.name, setting.max)
		}
	}
	return nil
}
