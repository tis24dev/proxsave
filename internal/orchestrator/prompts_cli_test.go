package orchestrator

import (
	"bufio"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/input"
)

func TestPromptYesNo(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"yes-short", "y\n", true},
		{"yes-long", "yes\n", true},
		{"yes-mixed", " YeS \n", true},
		{"no-default", "\n", false},
		{"no-explicit", "no\n", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := bufio.NewReader(strings.NewReader(tt.in))
			got, err := promptYesNo(context.Background(), reader, "prompt: ")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got=%v want=%v", got, tt.want)
			}
		})
	}
}

func TestPromptYesNo_ContextCanceledReturnsAbortError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	reader := bufio.NewReader(strings.NewReader("y\n"))
	_, err := promptYesNo(ctx, reader, "prompt: ")
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, input.ErrInputAborted) {
		t.Fatalf("err=%v; want %v", err, input.ErrInputAborted)
	}
}
