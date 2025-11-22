package backup

import (
	"context"
	"os"
	"os/exec"
)

var (
	execLookPath = exec.LookPath

	runCommandWithEnv = func(ctx context.Context, extraEnv []string, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		if len(extraEnv) > 0 {
			cmd.Env = append(os.Environ(), extraEnv...)
		}
		return cmd.CombinedOutput()
	}

	runCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return runCommandWithEnv(ctx, nil, name, args...)
	}

	statFunc = os.Stat
)
