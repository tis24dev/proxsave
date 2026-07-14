// Package main contains the proxsave command entrypoint.
package main

import (
	"strings"
	"testing"
)

func TestBuildDaemonUnitWithConfig(t *testing.T) {
	u := buildDaemonUnit("/usr/local/bin/proxsave", "/opt/proxsave/configs/backup.env")
	for _, want := range []string{
		"ExecStart=/usr/local/bin/proxsave --daemon --config /opt/proxsave/configs/backup.env",
		"Type=simple",
		"Restart=always",
		"RestartSec=10",
		"WantedBy=multi-user.target",
		"After=network-online.target",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q:\n%s", want, u)
		}
	}
}

func TestBuildDaemonUnitFallbacks(t *testing.T) {
	// Empty exec token -> canonical path; empty config -> no --config.
	u := buildDaemonUnit("", "")
	if !strings.Contains(u, "ExecStart="+daemonExecPath+" --daemon\n") {
		t.Errorf("expected canonical ExecStart without --config:\n%s", u)
	}
	if strings.Contains(u, "--config") {
		t.Errorf("empty config should not emit --config:\n%s", u)
	}
}
