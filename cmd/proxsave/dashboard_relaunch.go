package main

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safeexec"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

var (
	dashboardRelaunchCommandContext = safeexec.TrustedCommandContext
	dashboardRelaunchAfterUpgrade   = relaunchDashboardAfterUpgrade
)

func relaunchInstalledDashboard(ctx context.Context, execPath string) error {
	execPath = strings.TrimSpace(execPath)
	if execPath == "" {
		return errors.New("installed executable path is empty")
	}
	cmd, err := dashboardRelaunchCommandContext(ctx, execPath)
	if err != nil {
		return err
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func relaunchDashboardAfterUpgrade(ctx context.Context, execPath string, bootstrap *logging.BootstrapLogger) {
	done := logging.DebugStartBootstrap(bootstrap, "dashboard relaunch", "exec=%s", execPath)
	err := relaunchInstalledDashboard(ctx, execPath)
	if err != nil && bootstrap != nil {
		bootstrap.Error("Dashboard reload failed after upgrade: %v", err)
	}
	done(err)
}

func closeDashboardAndRelaunch(ctx context.Context, session *shell.Session, execPath string, bootstrap *logging.BootstrapLogger) {
	_ = session.Close()
	dashboardRelaunchAfterUpgrade(ctx, execPath, bootstrap)
}
