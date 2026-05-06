package main

import (
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/identity"
)

func TestInitializeServerIdentityKeepsConfiguredServerID(t *testing.T) {
	origDetector := runtimeServerIdentityDetector
	t.Cleanup(func() { runtimeServerIdentityDetector = origDetector })

	called := false
	runtimeServerIdentityDetector = func(*appRuntime) *identity.Info {
		called = true
		return &identity.Info{ServerID: "detected", PrimaryMAC: "00:11:22:33:44:55"}
	}

	rt := &appRuntime{cfg: &config.Config{ServerID: " configured "}}
	initializeServerIdentity(rt)

	if called {
		t.Fatal("detector should not run when ServerID is explicitly configured")
	}
	if rt.serverIDValue != "configured" {
		t.Fatalf("serverIDValue=%q; want configured", rt.serverIDValue)
	}
	if rt.cfg.ServerID != "configured" {
		t.Fatalf("cfg.ServerID=%q; want trimmed configured", rt.cfg.ServerID)
	}
}

func TestInitializeServerIdentityStoresDetectedServerID(t *testing.T) {
	origDetector := runtimeServerIdentityDetector
	t.Cleanup(func() { runtimeServerIdentityDetector = origDetector })

	runtimeServerIdentityDetector = func(*appRuntime) *identity.Info {
		return &identity.Info{ServerID: "detected", PrimaryMAC: "00:11:22:33:44:55"}
	}

	rt := &appRuntime{cfg: &config.Config{}}
	initializeServerIdentity(rt)

	if rt.serverIDValue != "detected" {
		t.Fatalf("serverIDValue=%q; want detected", rt.serverIDValue)
	}
	if rt.cfg.ServerID != "detected" {
		t.Fatalf("cfg.ServerID=%q; want detected", rt.cfg.ServerID)
	}
	if rt.serverMACValue != "00:11:22:33:44:55" {
		t.Fatalf("serverMACValue=%q; want detected MAC", rt.serverMACValue)
	}
}
