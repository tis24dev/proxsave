package main

import (
	"errors"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

// Regression for cfg-upgrade-err-zero-exit (2026-06-09 audit): after a successful
// binary install, a failed upgradeConfigWithBinary (cfgUpgradeErr) was logged and
// shown as "Configuration: ERROR" in the footer, but runUpgrade's terminal return
// inspected only upgradeErr, so it still returned ExitSuccess (0). The exit-code
// decision now lives in upgradeExitCode and accounts for both errors.
func TestUpgradeExitCode(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name                      string
		upgradeErr, cfgUpgradeErr error
		want                      int
	}{
		{"both ok", nil, nil, types.ExitSuccess.Int()},
		{"binary install failed", boom, nil, types.ExitGenericError.Int()},
		{"config upgrade failed after good install", nil, boom, types.ExitGenericError.Int()},
		{"both failed", boom, boom, types.ExitGenericError.Int()},
	}
	for _, c := range cases {
		if got := upgradeExitCode(c.upgradeErr, c.cfgUpgradeErr); got != c.want {
			t.Errorf("%s: upgradeExitCode = %d, want %d", c.name, got, c.want)
		}
	}
}
