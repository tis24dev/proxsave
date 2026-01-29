package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

func runPveshSensitive(ctx context.Context, _ *logging.Logger, args []string, redactFlags ...string) ([]byte, error) {
	output, err := restoreCmd.Run(ctx, "pvesh", args...)
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
