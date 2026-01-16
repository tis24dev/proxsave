package orchestrator

import (
	"context"
	"os"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestShowRestoreModeMenu_ParsesChoicesAndRetries(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)

	oldIn := os.Stdin
	oldOut := os.Stdout
	t.Cleanup(func() {
		os.Stdin = oldIn
		os.Stdout = oldOut
	})

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	out, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0o666)
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		t.Fatalf("OpenFile(%s): %v", os.DevNull, err)
	}
	os.Stdin = inR
	os.Stdout = out
	t.Cleanup(func() {
		_ = inR.Close()
		_ = inW.Close()
		_ = out.Close()
	})

	if _, err := inW.WriteString("99\n2\n"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	_ = inW.Close()

	got, err := ShowRestoreModeMenu(context.Background(), logger, SystemTypePVE)
	if err != nil {
		t.Fatalf("ShowRestoreModeMenu error: %v", err)
	}
	if got != RestoreModeStorage {
		t.Fatalf("got=%q want=%q", got, RestoreModeStorage)
	}
}

func TestShowRestoreModeMenu_CancelReturnsErrRestoreAborted(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)

	oldIn := os.Stdin
	oldOut := os.Stdout
	t.Cleanup(func() {
		os.Stdin = oldIn
		os.Stdout = oldOut
	})

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	out, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0o666)
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		t.Fatalf("OpenFile(%s): %v", os.DevNull, err)
	}
	os.Stdin = inR
	os.Stdout = out
	t.Cleanup(func() {
		_ = inR.Close()
		_ = inW.Close()
		_ = out.Close()
	})

	if _, err := inW.WriteString("0\n"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	_ = inW.Close()

	_, err = ShowRestoreModeMenu(context.Background(), logger, SystemTypePVE)
	if err != ErrRestoreAborted {
		t.Fatalf("err=%v want=%v", err, ErrRestoreAborted)
	}
}

func TestShowRestoreModeMenu_ContextCanceledReturnsErrRestoreAborted(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	oldIn := os.Stdin
	oldOut := os.Stdout
	t.Cleanup(func() { os.Stdout = oldOut })
	t.Cleanup(func() { os.Stdin = oldIn })

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	_ = inW.Close()
	os.Stdin = inR
	t.Cleanup(func() { _ = inR.Close() })

	out, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0o666)
	if err != nil {
		t.Fatalf("OpenFile(%s): %v", os.DevNull, err)
	}
	os.Stdout = out
	t.Cleanup(func() { _ = out.Close() })

	_, err = ShowRestoreModeMenu(ctx, logger, SystemTypePVE)
	if err != ErrRestoreAborted {
		t.Fatalf("err=%v want=%v", err, ErrRestoreAborted)
	}
}
