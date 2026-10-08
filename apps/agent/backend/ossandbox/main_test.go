package ossandbox

import (
	"os"
	"testing"
)

// Under bubblewrap, NetworkAllowlist commands start the test binary as the
// network bridge.
func TestMain(m *testing.M) {
	MaybeRunNetBridge()
	os.Exit(m.Run())
}
