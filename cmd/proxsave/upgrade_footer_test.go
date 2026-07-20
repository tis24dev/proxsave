package main

import (
	"errors"
	"strings"
	"testing"
)

const (
	ansiGreen = "\033[32m"
	ansiRed   = "\033[31m"
)

// upgradeFooterBody paints the banner from the combined install/config outcome.
// A config-only failure still exits non-zero and prints "Configuration: ERROR",
// so the banner must render red "failed" to agree, never green "completed".
func TestUpgradeFooterBody_ConfigOnlyFailureIsRed(t *testing.T) {
	out := captureStdout(t, func() {
		upgradeFooterBody(nil, "1.2.3", "/etc/proxsave/backup.env", "/opt/proxsave",
			"", "", "", nil, errors.New("bad key in template"), nil)
	})

	if !strings.Contains(strings.ToLower(out), "failed") {
		t.Errorf("config-only failure banner must say \"failed\"; got:\n%s", out)
	}
	if !strings.Contains(out, ansiRed) {
		t.Errorf("config-only failure banner must use red %q; got:\n%s", ansiRed, out)
	}
	if strings.Contains(out, ansiGreen) {
		t.Errorf("config-only failure banner must NOT use green %q; got:\n%s", ansiGreen, out)
	}
}

// A clean run (both nil) stays green "Upgrade completed".
func TestUpgradeFooterBody_SuccessIsGreen(t *testing.T) {
	out := captureStdout(t, func() {
		upgradeFooterBody(nil, "1.2.3", "/etc/proxsave/backup.env", "/opt/proxsave",
			"", "", "", nil, nil, nil)
	})

	if !strings.Contains(out, "Upgrade completed") {
		t.Errorf("clean run banner must say \"Upgrade completed\"; got:\n%s", out)
	}
	if !strings.Contains(out, ansiGreen) {
		t.Errorf("clean run banner must use green %q; got:\n%s", ansiGreen, out)
	}
	if strings.Contains(out, ansiRed) {
		t.Errorf("clean run banner must NOT use red %q; got:\n%s", ansiRed, out)
	}
}
