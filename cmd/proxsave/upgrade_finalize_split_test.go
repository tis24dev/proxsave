package main

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// --upgrade runs in the binary being REPLACED, so every change to the finalize
// policy - which keys the config merge adds, whether the daemon is migrated, what
// the footer says - took effect one upgrade late. The finalize is now handed to the
// binary that was just installed. Verification and the install itself are NOT
// delegated: the downloaded binary is untrusted until the old one has verified it,
// so it cannot be the party that verifies itself.

type recordedFinalizeChild struct {
	calls   [][]string
	makeErr error
	program string
}

func (r *recordedFinalizeChild) install(t *testing.T) {
	t.Helper()
	orig := upgradeFinalizeCommand
	t.Cleanup(func() { upgradeFinalizeCommand = orig })
	upgradeFinalizeCommand = func(ctx context.Context, path string, args ...string) (*exec.Cmd, error) {
		r.calls = append(r.calls, append([]string{path}, args...))
		if r.makeErr != nil {
			return nil, r.makeErr
		}
		program := r.program
		if program == "" {
			program = "/bin/true"
		}
		return exec.CommandContext(ctx, program), nil
	}
}

func finalizeSplitBootstrap() *logging.BootstrapLogger {
	bootstrap := logging.NewBootstrapLogger()
	bootstrap.SetLevel(types.LogLevelDebug)
	return bootstrap
}

func TestUpgradeFinalizeDelegationIsGatedOnTheInstalledVersion(t *testing.T) {
	tests := []struct {
		name          string
		version       string
		wantDelegated bool
	}{
		{"below the floor keeps the old path", "0.35.0", false},
		{"the floor itself delegates", upgradeFinalizeDelegationFloor, true},
		{"above the floor delegates", "1.2.3", true},
		// Fails closed: an unparsable version must never be read as "new enough".
		{"unparsable version keeps the old path", "not-a-version", false},
		{"empty version keeps the old path", "", false},
		// A pre-release of the floor sorts below it. Conservative on purpose.
		{"prerelease of the floor keeps the old path", upgradeFinalizeDelegationFloor + "-beta1", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			child := &recordedFinalizeChild{}
			child.install(t)

			_, _, delegated := delegateUpgradeFinalize(context.Background(), &cli.Args{ConfigPath: "/tmp/backup.env"},
				finalizeSplitBootstrap(), "/usr/local/bin/proxsave", tc.version, upgradeRunOptions{})
			if delegated != tc.wantDelegated {
				t.Fatalf("delegated = %v, want %v", delegated, tc.wantDelegated)
			}
			if !tc.wantDelegated && len(child.calls) != 0 {
				t.Fatalf("a below-floor binary was invoked anyway: %v", child.calls)
			}
		})
	}
}

func TestUpgradeFinalizeChildIsToldWhatItNeeds(t *testing.T) {
	tests := []struct {
		name     string
		args     *cli.Args
		opts     upgradeRunOptions
		wantSkip bool
	}{
		{"prompted upgrade opens the notes", &cli.Args{ConfigPath: "/tmp/backup.env"}, upgradeRunOptions{}, false},
		{"auto-yes suppresses them", &cli.Args{ConfigPath: "/tmp/backup.env", UpgradeAutoYes: true}, upgradeRunOptions{}, true},
		{"the dashboard defers them", &cli.Args{ConfigPath: "/tmp/backup.env"}, upgradeRunOptions{deferWhatsnew: true}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			child := &recordedFinalizeChild{}
			child.install(t)

			code, err, delegated := delegateUpgradeFinalize(context.Background(), tc.args,
				finalizeSplitBootstrap(), "/usr/local/bin/proxsave", "0.36.0", tc.opts)
			if !delegated || err != nil || code != types.ExitSuccess.Int() {
				t.Fatalf("delegated=%v code=%d err=%v", delegated, code, err)
			}
			if len(child.calls) != 1 {
				t.Fatalf("child invocations = %d, want 1", len(child.calls))
			}
			argv := strings.Join(child.calls[0], " ")
			for _, want := range []string{
				"/usr/local/bin/proxsave", "--upgrade-finalize",
				"--upgrade-finalize-version 0.36.0", "--config /tmp/backup.env",
			} {
				if !strings.Contains(argv, want) {
					t.Fatalf("argv %q is missing %q", argv, want)
				}
			}
			if got := strings.Contains(argv, "--upgrade-finalize-skip-whatsnew"); got != tc.wantSkip {
				t.Fatalf("skip-whatsnew present = %v, want %v (argv %q)", got, tc.wantSkip, argv)
			}
		})
	}
}

// A child that never STARTS leaves the host no worse than before the split
// existed, so the caller finalizes in-process. A child that RUNS and fails is a
// finalize that genuinely failed: it is reported, never retried here, because the
// config merge is not something to assume idempotent.
func TestUpgradeFinalizeFallsBackOnlyWhenTheChildCannotRun(t *testing.T) {
	t.Run("command cannot be built", func(t *testing.T) {
		child := &recordedFinalizeChild{makeErr: errors.New("not a trusted path")}
		child.install(t)
		_, err, delegated := delegateUpgradeFinalize(context.Background(), &cli.Args{},
			finalizeSplitBootstrap(), "/usr/local/bin/proxsave", "0.36.0", upgradeRunOptions{})
		if delegated || err != nil {
			t.Fatalf("delegated=%v err=%v, want an in-process fallback", delegated, err)
		}
	})

	t.Run("binary will not start", func(t *testing.T) {
		child := &recordedFinalizeChild{program: "/nonexistent/proxsave"}
		child.install(t)
		_, err, delegated := delegateUpgradeFinalize(context.Background(), &cli.Args{},
			finalizeSplitBootstrap(), "/usr/local/bin/proxsave", "0.36.0", upgradeRunOptions{})
		if delegated || err != nil {
			t.Fatalf("delegated=%v err=%v, want an in-process fallback", delegated, err)
		}
	})

	t.Run("child ran and failed", func(t *testing.T) {
		child := &recordedFinalizeChild{program: "/bin/false"}
		child.install(t)
		code, err, delegated := delegateUpgradeFinalize(context.Background(), &cli.Args{},
			finalizeSplitBootstrap(), "/usr/local/bin/proxsave", "0.36.0", upgradeRunOptions{})
		if !delegated {
			t.Fatal("a finalize that ran and failed must not be retried in-process")
		}
		if err == nil || code == types.ExitSuccess.Int() {
			t.Fatalf("code=%d err=%v, want the failure reported", code, err)
		}
	})
}

// The child must never delegate again: it IS the installed binary.
func TestUpgradeFinalizeChildDoesNotDelegateFurther(t *testing.T) {
	child := &recordedFinalizeChild{}
	child.install(t)

	code, handled := runUpgradeFinalizeMode(context.Background(), &cli.Args{}, finalizeSplitBootstrap(), "0.36.0")
	if handled {
		t.Fatalf("an argument set without --upgrade-finalize was handled anyway (code %d)", code)
	}
	if len(child.calls) != 0 {
		t.Fatalf("the child spawned another child: %v", child.calls)
	}
}

// `--upgrade --log-level debug` is the flag someone reaches for precisely when the
// finalize is what they are trying to watch. Half of it now runs in another
// process, so the level has to travel with it. LogLevelNone means "unset, let the
// config decide" and must not be forwarded as a choice.
func TestUpgradeFinalizeChildInheritsTheLogLevel(t *testing.T) {
	tests := []struct {
		name  string
		level types.LogLevel
		want  string
	}{
		{"debug travels", types.LogLevelDebug, "--log-level debug"},
		{"warning travels", types.LogLevelWarning, "--log-level warning"},
		{"unset does not", types.LogLevelNone, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			child := &recordedFinalizeChild{}
			child.install(t)

			_, _, delegated := delegateUpgradeFinalize(context.Background(),
				&cli.Args{ConfigPath: "/tmp/backup.env", LogLevel: tc.level},
				finalizeSplitBootstrap(), "/usr/local/bin/proxsave", "0.36.0", upgradeRunOptions{})
			if !delegated || len(child.calls) != 1 {
				t.Fatalf("delegated=%v calls=%v", delegated, child.calls)
			}
			argv := strings.Join(child.calls[0], " ")
			if tc.want == "" {
				if strings.Contains(argv, "--log-level") {
					t.Fatalf("an unset level was forwarded as a choice: %q", argv)
				}
				return
			}
			if !strings.Contains(argv, tc.want) {
				t.Fatalf("argv %q is missing %q", argv, tc.want)
			}
		})
	}
}
