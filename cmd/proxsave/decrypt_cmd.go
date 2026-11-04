package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
)

// runDecryptWorkflowOnly executes the decrypt workflow without initializing the backup orchestrator.
// decryptStdinCheck is ensureInteractiveStdin, a seam for tests.
var decryptStdinCheck = ensureInteractiveStdin

func runDecryptWorkflowOnly(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger, version string, useCLI bool) error {
	configPath := args.ConfigPath
	// Print version/banner header (mirrored later into the decrypt log via bootstrap.Flush).
	bootstrap.Println("===========================================")
	bootstrap.Println("  ProxSave")
	bootstrap.Printf("  Version: %s", version)
	if sig := buildSignature(); strings.TrimSpace(sig) != "" {
		bootstrap.Printf("  Build Signature: %s", sig)
	}
	bootstrap.Println("===========================================")
	bootstrap.Println("")

	if err := ensureConfigExists(configPath, bootstrap); err != nil {
		return err
	}

	if err := decryptStdinCheck(); err != nil {
		return err
	}

	autoBaseDir, _ := detectedBaseDirOrFallback()
	cfg, err := config.LoadConfigWithBaseDir(configPath, autoBaseDir)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	_ = os.Setenv("BASE_DIR", cfg.BaseDir)

	// The backup and restore rule: DEBUG_LEVEL is the base and --log-level overrides it.
	// Decrypt read DEBUG_LEVEL only, so --log-level was ignored here alone.
	logLevel := resolveRunLogLevel(args, cfg, bootstrap)
	logger, sessionLogPath, closeSessionLog, err := logging.StartSessionLogger("decrypt", logLevel, cfg.UseColor)
	if err != nil {
		logger = logging.New(logLevel, cfg.UseColor)
		closeSessionLog = func() {}
	} else {
		bootstrap.Printf("Decrypt log: %s", sessionLogPath)
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
