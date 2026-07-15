// Package main contains the proxsave command entrypoint.
package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

// captureDefaultInfo swaps the default logger for a buffer-backed one, runs fn, and
// returns everything logged. It restores the previous default logger via t.Cleanup.
func captureDefaultInfo(t *testing.T, fn func()) string {
	t.Helper()
	prev := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(prev) })

	var buf bytes.Buffer
	def := logging.New(types.LogLevelInfo, false)
	def.SetOutput(&buf)
	logging.SetDefaultLogger(def)

	fn()
	return buf.String()
}

// TestLogMonitoringPortalLink pins the NEW sole display boundary for the portal
// magic-link: a valid link is sanitized and printed with the exact prior wording; a
// hostile link is stripped by serverbot.SanitizeLoginURL and prints nothing; nil stats
// and an empty link print nothing. The line never registers the link as a log secret.
func TestLogMonitoringPortalLink(t *testing.T) {
	const wording = "Healthchecks Portal:"

	t.Run("valid link is sanitized and displayed", func(t *testing.T) {
		link := "https://hc/accounts/check_token/u/CAP/"
		out := captureDefaultInfo(t, func() {
			logMonitoringPortalLink(&orchestrator.BackupStats{HealthcheckLink: link})
		})
		if !strings.Contains(out, wording) {
			t.Fatalf("want portal wording, out=%q", out)
		}
		if !strings.Contains(out, link) {
			t.Fatalf("want the sanitized link, out=%q", out)
		}
	})

	hostile := []struct {
		name string
		link string
	}{
		{"raw space", "https://hc/ x"},
		{"javascript scheme", "javascript:alert(1)"},
		{"control char", "https://hc/\x07evil"},
	}
	for _, tc := range hostile {
		t.Run("hostile link stripped: "+tc.name, func(t *testing.T) {
			out := captureDefaultInfo(t, func() {
				logMonitoringPortalLink(&orchestrator.BackupStats{HealthcheckLink: tc.link})
			})
			if strings.Contains(out, wording) || strings.Contains(out, tc.link) {
				t.Fatalf("hostile link must be sanitized away (no output), out=%q", out)
			}
		})
	}

	t.Run("nil stats prints nothing", func(t *testing.T) {
		out := captureDefaultInfo(t, func() { logMonitoringPortalLink(nil) })
		if strings.Contains(out, wording) {
			t.Fatalf("nil stats must print nothing, out=%q", out)
		}
	})

	t.Run("empty link prints nothing", func(t *testing.T) {
		out := captureDefaultInfo(t, func() {
			logMonitoringPortalLink(&orchestrator.BackupStats{HealthcheckLink: ""})
		})
		if strings.Contains(out, wording) {
			t.Fatalf("empty link must print nothing, out=%q", out)
		}
	})
}

// TestMonitoringPortalLinkFollowsMacInEpilogue pins the ordering the user asked for: in
// runConfiguredBackup the portal link is printed RIGHT AFTER the Server MAC Address line.
// A source-level check keeps the assertion cheap and robust (no full backup run needed).
func TestMonitoringPortalLinkFollowsMacInEpilogue(t *testing.T) {
	src, err := os.ReadFile("backup_execution.go")
	if err != nil {
		t.Fatalf("read backup_execution.go: %v", err)
	}
	body := string(src)
	idIdx := strings.Index(body, "logServerIdentityValues(opts.serverIDValue, opts.serverMACValue)")
	if idIdx < 0 {
		t.Fatalf("could not find the server identity call site in the epilogue")
	}
	linkIdx := strings.Index(body, "logMonitoringPortalLink(stats)")
	if linkIdx < 0 {
		t.Fatalf("could not find the monitoring portal link call site in the epilogue")
	}
	if linkIdx <= idIdx {
		t.Fatalf("logMonitoringPortalLink must be called AFTER logServerIdentityValues (idIdx=%d linkIdx=%d)", idIdx, linkIdx)
	}
	heapIdx := strings.Index(body, "Heap profiling saved")
	if heapIdx >= 0 && linkIdx >= heapIdx {
		t.Fatalf("logMonitoringPortalLink must come before the heap-profile line")
	}
}
