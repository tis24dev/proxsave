package backup

import (
	"bytes"
	"context"
	"os"
	"os/exec"

	"github.com/tis24dev/proxsave/internal/safeexec"
)

var (
	execLookPath = exec.LookPath

	// runCommandCapturedWithEnv keeps stdout and stderr apart. Collected artifacts must
	// carry stdout alone: tools like `proxmox-backup-manager disk list --output-format=json`
	// write diagnostics to stderr (smartctl failures, for instance) while still emitting
	// valid JSON on stdout, and merging the streams produces an unparsable file.
	runCommandCapturedWithEnv = func(ctx context.Context, extraEnv []string, name string, args ...string) ([]byte, []byte, error) {
		cmd, err := safeexec.CommandContext(ctx, name, args...)
		if err != nil {
			return nil, nil, err
		}
		// This runner executes more commands per run than every other call site in the
		// product combined, and it captures into two in-memory buffers, so the drain
		// budget cannot cost it output: a buffer never blocks, and what the budget
		// interrupts is the wait for an EOF a surviving descendant is withholding.
		// Without it one collected tool that leaves a background child holding stdout
		// stalls the whole collection phase for that child's lifetime.
		safeexec.ApplyWaitDelay(cmd)
		if len(extraEnv) > 0 {
			cmd.Env = append(os.Environ(), extraEnv...)
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		return stdout.Bytes(), stderr.Bytes(), runErr
	}

	runCommandWithEnv = func(ctx context.Context, extraEnv []string, name string, args ...string) ([]byte, error) {
		stdout, stderr, err := runCommandCapturedWithEnv(ctx, extraEnv, name, args...)
		if len(stderr) == 0 {
			return stdout, err
		}
		return []byte(mergeCommandStreams(stdout, stderr)), err
	}

	runCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return runCommandWithEnv(ctx, nil, name, args...)
	}

	statFunc = os.Stat
)

// CollectorDeps allows injecting external dependencies for the Collector.
//
// RunCommandCaptured returns stdout and stderr separately and is what the collection
// paths use. RunCommand and RunCommandWithEnv return the two streams concatenated; they
// remain for callers and tests that only care about the merged text, and a Collector
// configured with just those keeps working (stderr is then folded into stdout, the
// pre-split behaviour).
type CollectorDeps struct {
	LookPath                    func(string) (string, error)
	RunCommandCaptured          func(context.Context, []string, string, ...string) ([]byte, []byte, error)
	RunCommandWithEnv           func(context.Context, []string, string, ...string) ([]byte, error)
	RunCommand                  func(context.Context, string, ...string) ([]byte, error)
	Stat                        func(string) (os.FileInfo, error)
	DetectUnprivilegedContainer func() (bool, string)
}

// defaultCollectorDeps leaves the command hooks nil on purpose. The Collector already
// falls back to the package-level runners when a hook is unset, so nil here means the
// split-stream runner is used by default while a caller that overrides only RunCommand
// (or RunCommandWithEnv) still has its override honoured.
func defaultCollectorDeps() CollectorDeps {
	return CollectorDeps{
		LookPath: func(name string) (string, error) {
			return execLookPath(name)
		},
		Stat: func(path string) (os.FileInfo, error) {
			return statFunc(path)
		},
	}
}
