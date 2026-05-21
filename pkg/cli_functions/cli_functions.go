// Package cli_functions provides shared business logic callable from multiple TYA commands.
package cli_functions

import (
	"fmt"
	"os"
	"os/exec"

	"go.uber.org/zap"
)

// CheckDocker verifies that Docker is installed and the daemon is reachable.
func CheckDocker(log *zap.Logger) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker not found in PATH: %w", err)
	}
	cmd := exec.Command("docker", "info")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker daemon not reachable (is it running?): %w", err)
	}
	log.Info("docker check passed")
	return nil
}

// CheckJava verifies that a JRE/JDK is available (required for openapi-generator-cli).
func CheckJava(log *zap.Logger) error {
	if _, err := exec.LookPath("java"); err != nil {
		return fmt.Errorf("java not found in PATH (required for openapi-generator-cli): %w", err)
	}
	log.Info("java check passed")
	return nil
}

// EnsureDir creates a directory (and all parents) if it does not already exist.
func EnsureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}

// WriteFile writes data to path, creating parent directories as needed.
func WriteFile(path string, data []byte) error {
	if err := EnsureDir(dirOf(path)); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// dirOf returns the directory component of a file path.
func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[:i]
		}
	}
	return "."
}

// FileExists returns true if path exists and is a regular file.
func FileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// DirExists returns true if path exists and is a directory.
func DirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
