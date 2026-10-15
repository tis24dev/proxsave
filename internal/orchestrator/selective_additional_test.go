package orchestrator

import (
	"context"
	"os"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestConfirmRestoreOperation(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	old := os.Stdin
	defer func() { os.Stdin = old }()

	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"restore", "RESTORE\n", true},
		{"cancel", "cancel\n", false},
		{"zero", "0\n", false},
		{"invalid then restore", "nope\nRESTORE\n", true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatalf("os.Pipe: %v", err)
			}
			if _, err := w.WriteString(tt.input); err != nil {
				_ = r.Close()
				_ = w.Close()
				t.Fatalf("WriteString: %v", err)
			}
			_ = w.Close()
			os.Stdin = r
			defer func() { _ = r.Close() }()

			got, err := ConfirmRestoreOperation(context.Background(), logger)
			if err != nil {
				t.Fatalf("ConfirmRestoreOperation returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("ConfirmRestoreOperation() = %v; want %v", got, tt.want)
			}
		})
	}
}
