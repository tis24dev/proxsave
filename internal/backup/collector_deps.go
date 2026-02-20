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

// CollectorDeps allows injecting external dependencies for the Collector.
type CollectorDeps struct {
	LookPath          func(string) (string, error)
	RunCommandWithEnv func(context.Context, []string, string, ...string) ([]byte, error)
	RunCommand        func(context.Context, string, ...string) ([]byte, error)
	Stat              func(string) (os.FileInfo, error)
}

func defaultCollectorDeps() CollectorDeps {
	return CollectorDeps{
		LookPath: func(name string) (string, error) {
			return execLookPath(name)
		},
		RunCommandWithEnv: func(ctx context.Context, extraEnv []string, name string, args ...string) ([]byte, error) {
			return runCommandWithEnv(ctx, extraEnv, name, args...)
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return runCommand(ctx, name, args...)
		},
		Stat: func(path string) (os.FileInfo, error) {
			return statFunc(path)
		},
	}
}
