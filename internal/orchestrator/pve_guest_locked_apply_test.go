package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestPVEGuestLockedWriterUsesFixedPerlProgramAndArgv(t *testing.T) {
	orig := runPVEGuestLockHelper
	t.Cleanup(func() { runPVEGuestLockHelper = orig })

	var gotArgs []string
	runPVEGuestLockHelper = func(_ context.Context, args ...string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return nil, nil
	}

	data := []byte("hostname: ct101\n")
	err := writeGuestConfigWithPVELock(
		context.Background(),
		logging.New(types.LogLevelDebug, false),
		"pve",
		vmEntry{VMID: "101", Kind: "lxc", Path: "/var/tmp/staged/101.conf"},
		guestMustBeAbsent,
		data,
	)
	if err != nil {
		t.Fatalf("writeGuestConfigWithPVELock: %v", err)
	}
	if len(gotArgs) != 9 {
		t.Fatalf("helper args=%d, want 9: %#v", len(gotArgs), gotArgs)
	}
	if gotArgs[0] != "-e" || gotArgs[1] != pveGuestLockedApplyPerl || gotArgs[2] != "--" {
		t.Fatalf("helper is not a fixed perl -e invocation: %#v", gotArgs[:3])
	}
	wantDigest := sha256.Sum256(data)
	wantTail := []string{"absent", "lxc", "101", "pve", "/var/tmp/staged/101.conf", hex.EncodeToString(wantDigest[:])}
	for i, want := range wantTail {
		if got := gotArgs[i+3]; got != want {
			t.Fatalf("helper arg %d=%q, want %q", i+3, got, want)
		}
	}
	for _, arg := range gotArgs[3:] {
		if arg == string(data) {
			t.Fatal("configuration bytes were embedded into argv")
		}
	}
}

func TestPVEGuestLockedWriterFailsClosedWithSanitizedBoundedOutput(t *testing.T) {
	orig := runPVEGuestLockHelper
	t.Cleanup(func() { runPVEGuestLockHelper = orig })

	runErr := errors.New("exit status 255")
	runPVEGuestLockHelper = func(context.Context, ...string) ([]byte, error) {
		return []byte("\x1b[31mguest started\x1b[0m\n" + strings.Repeat("x", 700)), runErr
	}
	err := writeGuestConfigWithPVELock(
		context.Background(),
		logging.New(types.LogLevelDebug, false),
		"pve",
		vmEntry{VMID: "100", Kind: "qemu", Path: "/var/tmp/staged/100.conf"},
		guestMustBeStopped,
		[]byte("name: vm100\n"),
	)
	if !errors.Is(err, runErr) {
		t.Fatalf("want helper error retained, got %v", err)
	}
	message := err.Error()
	if strings.Contains(message, "\x1b") || strings.Contains(message, "\n") {
		t.Fatalf("helper output was not reduced to a safe single line: %q", message)
	}
	if !strings.Contains(message, "guest started") || !strings.HasSuffix(message, "…)") {
		t.Fatalf("helper output was not preserved and bounded: %q", message)
	}
}

func TestPVEGuestLockedWriterRejectsInvalidInputsBeforeExecution(t *testing.T) {
	orig := runPVEGuestLockHelper
	t.Cleanup(func() { runPVEGuestLockHelper = orig })
	runPVEGuestLockHelper = func(context.Context, ...string) ([]byte, error) {
		t.Fatal("helper executed for invalid input")
		return nil, nil
	}

	logger := logging.New(types.LogLevelDebug, false)
	tests := []struct {
		name         string
		node         string
		vm           vmEntry
		precondition guestApplyPrecondition
	}{
		{name: "unknown precondition", node: "pve", vm: vmEntry{VMID: "100", Kind: "qemu", Path: "/tmp/100.conf"}, precondition: "unknown"},
		{name: "unknown kind", node: "pve", vm: vmEntry{VMID: "100", Kind: "other", Path: "/tmp/100.conf"}, precondition: guestMustBeAbsent},
		{name: "non-canonical vmid", node: "pve", vm: vmEntry{VMID: "0100", Kind: "qemu", Path: "/tmp/100.conf"}, precondition: guestMustBeAbsent},
		{name: "empty node", node: " ", vm: vmEntry{VMID: "100", Kind: "qemu", Path: "/tmp/100.conf"}, precondition: guestMustBeAbsent},
		{name: "relative source", node: "pve", vm: vmEntry{VMID: "100", Kind: "qemu", Path: "100.conf"}, precondition: guestMustBeAbsent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := writeGuestConfigWithPVELock(context.Background(), logger, tt.node, tt.vm, tt.precondition, []byte("x")); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}
