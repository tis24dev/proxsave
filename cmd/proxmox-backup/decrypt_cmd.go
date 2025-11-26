package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/tis24dev/proxmox-backup/internal/config"
	"github.com/tis24dev/proxmox-backup/internal/logging"
	"github.com/tis24dev/proxmox-backup/internal/orchestrator"
)

// runDecryptWorkflowOnly executes the decrypt workflow without initializing the backup orchestrator.
func runDecryptWorkflowOnly(ctx context.Context, configPath string, bootstrap *logging.BootstrapLogger, version string, useCLI bool) error {
	if err := ensureConfigExists(configPath, bootstrap); err != nil {
		return err
	}

	if err := ensureInteractiveStdin(); err != nil {
		return err
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	autoBaseDir, _ := detectBaseDir()
	if cfg.BaseDir == "" {
		if autoBaseDir == "" {
			autoBaseDir = "/opt/proxmox-backup"
		}
		cfg.BaseDir = autoBaseDir
	}
	_ = os.Setenv("BASE_DIR", cfg.BaseDir)

	logLevel := cfg.DebugLevel
	logger, sessionLogPath, closeSessionLog, err := logging.StartSessionLogger("decrypt", logLevel, cfg.UseColor)
	if err != nil {
		logger = logging.New(logLevel, cfg.UseColor)
		closeSessionLog = func() {}
	} else {
		bootstrap.Info("Decrypt log: %s", sessionLogPath)
	}
	defer closeSessionLog()

	logging.SetDefaultLogger(logger)
	bootstrap.SetLevel(logLevel)
	bootstrap.Flush(logger)

	buildSig := buildSignature()
	if strings.TrimSpace(buildSig) == "" {
		buildSig = "n/a"
	}

	if useCLI {
		logging.Info("Starting decrypt workflow (CLI)")
		return orchestrator.RunDecryptWorkflow(ctx, cfg, logger, version)
	}

	// In TUI decrypt mode we keep console output minimal; this step is logged at debug level only.
	logging.Debug("Starting decrypt workflow (TUI)")
	return orchestrator.RunDecryptWorkflowTUI(ctx, cfg, logger, version, configPath, buildSig)
}
