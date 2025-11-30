package orchestrator

import (
	"bufio"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
)

func TestConfirmRestoreAction_Abort(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("0\n"))
	cand := &decryptCandidate{
		DisplayBase: "test",
		Manifest:    &backup.Manifest{CreatedAt: time.Now()},
	}

	err := confirmRestoreAction(context.Background(), reader, cand, "/")
	if err == nil {
		t.Fatalf("expected abort error")
	}
}

func TestConfirmRestoreAction_Proceed(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("RESTORE\n"))
	cand := &decryptCandidate{
		DisplayBase: "test",
		Manifest:    &backup.Manifest{CreatedAt: time.Now()},
	}

	if err := confirmRestoreAction(context.Background(), reader, cand, "/"); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}
