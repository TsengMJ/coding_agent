//go:build !darwin && !linux

package ossandbox

import (
	"fmt"
	"runtime"
)

// Native returns the platform sandbox, or an error saying why it cannot run.
func Native() (Sandbox, error) {
	return nil, fmt.Errorf("sandbox: not supported on %s", runtime.GOOS)
}
