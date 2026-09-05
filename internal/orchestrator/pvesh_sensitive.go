package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

// runPveshSensitive runs pvesh with the given args, redacting the flags named in
// redactFlags before the command ever appears in an error message.
//
// It captures STDOUT ONLY. The single caller that keeps the output parses it as
// JSON (applyPVEClusterMapping, pve_safe_apply_mappings.go), and a merged capture
// makes that parse fail on a byte pvesh never meant as data. Measured on a live
// PVE 9.1.9 node (2026-09-05): with LC_ALL set to a locale the host does not have,
// `pvesh get /cluster/mapping/pci --output-format=json` exits 0, writes valid JSON
// to stdout and 542 bytes of "perl: warning: Setting locale failed." to stderr;
// merged, the parse fails. The stock sshd_config on that node carries
// `AcceptEnv LANG LC_*`, so an operator sshing in with a locale the node lacks is
// enough to reach it. The other two callers discard the output entirely, so
// nothing is lost by not merging: on failure runCommandStdout folds stderr into
// the error, which is where these callers read the reason from anyway.
func runPveshSensitive(ctx context.Context, _ *logging.Logger, args []string, redactFlags ...string) ([]byte, error) {
	output, err := runCommandStdout(ctx, "pvesh", args...)
	if err != nil {
		redacted := redactCLIArgs(args, redactFlags)
		return output, fmt.Errorf("pvesh %s failed: %w", strings.Join(redacted, " "), err)
	}
	return output, nil
}

func redactCLIArgs(args []string, redactFlags []string) []string {
	if len(args) == 0 || len(redactFlags) == 0 {
		return append([]string(nil), args...)
	}
	redact := make(map[string]struct{}, len(redactFlags))
	for _, flag := range redactFlags {
		redact[strings.TrimSpace(flag)] = struct{}{}
	}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		out = append(out, arg)
		if _, ok := redact[arg]; ok && i+1 < len(args) {
			i++
			out = append(out, "<redacted>")
		}
	}
	return out
}
