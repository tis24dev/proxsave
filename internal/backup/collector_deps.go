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

	// runCommandCaptured keeps stdout and stderr apart. Collected artifacts must
	// carry stdout alone: tools like `proxmox-backup-manager disk list --output-format=json`
	// write diagnostics to stderr (smartctl failures, for instance) while still emitting
	// valid JSON on stdout, and merging the streams produces an unparsable file.
	runCommandCaptured = func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
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
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		return stdout.Bytes(), stderr.Bytes(), runErr
	}

	runCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		stdout, stderr, err := runCommandCaptured(ctx, name, args...)
		if len(stderr) == 0 {
			return stdout, err
		}
		return []byte(mergeCommandStreams(stdout, stderr)), err
	}

	statFunc = os.Stat
)

// CollectorDeps allows injecting external dependencies for the Collector.
//
// RunCommandCaptured returns stdout and stderr separately and is what the collection
// paths use. RunCommand returns the two streams concatenated; it remains for callers
// and tests that only care about the merged text, and a Collector configured with just
// that keeps working (stderr is then folded into stdout, the pre-split behaviour).
type CollectorDeps struct {
	LookPath                    func(string) (string, error)
	RunCommandCaptured          func(context.Context, string, ...string) ([]byte, []byte, error)
	RunCommand                  func(context.Context, string, ...string) ([]byte, error)
	Stat                        func(string) (os.FileInfo, error)
	ReadFile                    func(string) ([]byte, error)
	DetectUnprivilegedContainer func() (bool, string)
}
