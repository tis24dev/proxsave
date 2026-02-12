package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func parseLogLevel(raw string) types.LogLevel {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return types.LogLevelDebug
	case "info", "":
		return types.LogLevelInfo
	case "warning", "warn":
		return types.LogLevelWarning
	case "error":
		return types.LogLevelError
	default:
		return types.LogLevelInfo
	}
}

func main() {
	var (
		root       string
		maxSize    int64
		levelLabel string
	)

	flag.StringVar(&root, "root", "/tmp/test_prefilter", "Root directory to run prefilter on")
	flag.Int64Var(&maxSize, "max-size", 8*1024*1024, "Max file size (bytes) to prefilter")
	flag.StringVar(&levelLabel, "log-level", "info", "Log level: debug|info|warn|error")
	flag.Parse()

	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || root == "." {
		root = string(os.PathSeparator)
	}

	logger := logging.New(parseLogLevel(levelLabel), false)
	logger.SetOutput(os.Stdout)

	cfg := backup.OptimizationConfig{
		EnablePrefilter:           true,
		PrefilterMaxFileSizeBytes: maxSize,
	}

	if err := backup.ApplyOptimizations(context.Background(), logger, root, cfg); err != nil {
		logger.Error("Prefilter failed: %v", err)
		os.Exit(1)
	}
}
