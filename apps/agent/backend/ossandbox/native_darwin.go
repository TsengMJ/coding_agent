//go:build darwin

package ossandbox

import (
	"fmt"
	"os"
)

// Native returns the platform sandbox, or an error saying why it cannot run.
func Native() (Sandbox, error) {
	if _, err := os.Stat(SandboxExecPath); err != nil {
		return nil, fmt.Errorf("sandbox: %s not found: %w", SandboxExecPath, err)
	}
	return Seatbelt{}, nil
}
