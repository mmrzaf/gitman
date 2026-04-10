package git

import (
	"fmt"
	"os"
	"os/exec"
)

// InitBareRepo creates a physical bare git repository on disk
func InitBareRepo(fullPath string) error {
	// 0700 ensures only the user running gitman can access the files
	if err := os.MkdirAll(fullPath, 0o700); err != nil {
		return fmt.Errorf("failed to create repo directory: %w", err)
	}

	cmd := exec.Command("git", "init", "--bare", fullPath)
	if err := cmd.Run(); err != nil {
		if err := os.RemoveAll(fullPath); err != nil {
			return err
		}
		return fmt.Errorf("failed to initialize bare repo: %w", err)
	}
	return nil
}

func DeleteRepo(fullPath string) error {
	// Optional but recommended: check if it's actually a git dir before nuking it
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(fullPath)
}
