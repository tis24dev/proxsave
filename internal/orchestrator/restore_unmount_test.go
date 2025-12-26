package orchestrator

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestUnmountEtcPVESkipsWhenNotMounted(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"umount /etc/pve": []byte("umount: /etc/pve: not mounted"),
		},
		Errors: map[string]error{
			"umount /etc/pve": errors.New("exit status 32"),
		},
	}
	restoreCmd = fake

	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)

	if err := unmountEtcPVE(context.Background(), logger); err != nil {
		t.Fatalf("unmountEtcPVE error=%v; want nil", err)
	}
}

func TestUnmountEtcPVEReturnsMessageOnFailure(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"umount /etc/pve": []byte("permission denied"),
		},
		Errors: map[string]error{
			"umount /etc/pve": errors.New("exit status 1"),
		},
	}
	restoreCmd = fake

	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)

	err := unmountEtcPVE(context.Background(), logger)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("error=%q; want to contain %q", err.Error(), "permission denied")
	}
}

func TestUnmountEtcPVESuccessReturnsNil(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"umount /etc/pve": []byte(""),
		},
	}
	restoreCmd = fake

	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)

	if err := unmountEtcPVE(context.Background(), logger); err != nil {
		t.Fatalf("unmountEtcPVE error=%v; want nil", err)
	}
}
