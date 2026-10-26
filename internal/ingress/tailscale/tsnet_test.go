package tailscale

import (
	"os"
	"sync"
	"testing"

	"tailscale.com/net/tstun"
)

// withMTUEnv sets TS_DEBUG_MTU for the test (unset when value is nil) and
// restores what was there. The knob is read from the environment on every
// use, so no cache needs resetting.
func withMTUEnv(t *testing.T, value *string) {
	t.Helper()
	prev, had := os.LookupEnv("TS_DEBUG_MTU")
	t.Cleanup(func() {
		if had {
			os.Setenv("TS_DEBUG_MTU", prev)
		} else {
			os.Unsetenv("TS_DEBUG_MTU")
		}
	})
	if value == nil {
		os.Unsetenv("TS_DEBUG_MTU")
	} else {
		os.Setenv("TS_DEBUG_MTU", *value)
	}
}

func TestTunnelMTUKeepsTheOwnersSetting(t *testing.T) {
	owner := "1100"
	withMTUEnv(t, &owner)
	setTunnelMTU()
	if got := tstun.DefaultTUNMTU(); got != 1100 {
		t.Fatalf("MTU %d, want the owner's 1100", got)
	}
}

// A node made by Iwato gets the lowered MTU before tsnet starts (newTSNode
// only builds the server; nothing connects).
func TestNewTSNodeLowersTheMTU(t *testing.T) {
	withMTUEnv(t, nil)
	lowerMTU = sync.Once{}
	t.Cleanup(func() { lowerMTU = sync.Once{} })
	newTSNode(NodeConfig{Hostname: "iwato-test", Dir: t.TempDir()})
	if got := tstun.DefaultTUNMTU(); got != 1200 {
		t.Fatalf("MTU %d, want 1200", got)
	}
}
