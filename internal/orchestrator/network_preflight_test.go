package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunNetworkPreflightValidationPrefersIfup(t *testing.T) {
	fake := &fakeCommandRunner{
		outputs: map[string][]byte{
			"ifup -n -a": []byte("ok\n"),
		},
	}

	available := func(name string) bool {
		return name == "ifup"
	}

	result := runNetworkPreflightValidationWithDeps(context.Background(), 100*time.Millisecond, nil, available, fake.Run)
	if !result.Ok() {
		t.Fatalf("expected ok, got %s\n%s", result.Summary(), result.Details())
	}
	if result.Tool != "ifup" {
		t.Fatalf("tool=%q want %q", result.Tool, "ifup")
	}
	if len(result.Args) == 0 || result.Args[0] != "-n" {
		t.Fatalf("args=%v want [-n -a]", result.Args)
	}
}

func TestRunNetworkPreflightValidationFallsBackWhenFlagsUnsupported(t *testing.T) {
	fake := &fakeCommandRunner{
		outputs: map[string][]byte{
			"ifup -n -a":       []byte("ifup: unknown option -n\n"),
			"ifup --no-act -a": []byte("ok\n"),
		},
		errs: map[string]error{
			"ifup -n -a": errors.New("exit status 2"),
		},
	}

	available := func(name string) bool {
		return name == "ifup"
	}

	result := runNetworkPreflightValidationWithDeps(context.Background(), 100*time.Millisecond, nil, available, fake.Run)
	if !result.Ok() {
		t.Fatalf("expected ok, got %s\n%s", result.Summary(), result.Details())
	}
	if result.Tool != "ifup" {
		t.Fatalf("tool=%q want %q", result.Tool, "ifup")
	}
	if len(result.Args) == 0 || result.Args[0] != "--no-act" {
		t.Fatalf("args=%v want [--no-act -a]", result.Args)
	}
}

func TestRunNetworkPreflightValidationSkippedWhenNoValidators(t *testing.T) {
	fake := &fakeCommandRunner{}
	result := runNetworkPreflightValidationWithDeps(context.Background(), 50*time.Millisecond, nil, func(string) bool { return false }, fake.Run)
	if !result.Skipped {
		t.Fatalf("expected skipped=true, got %v", result.Skipped)
	}
	if result.Ok() {
		t.Fatalf("expected ok=false when skipped")
	}
}
