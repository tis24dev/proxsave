package cli

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/tis24dev/proxsave/internal/types"
)

const (
	defaultConfigPath   = "./configs/backup.env"
	configSourceDefault = "default path"
	configSourceFlag    = "specified via --config/-c flag"
)

// Args holds the parsed command-line arguments
type Args struct {
	ConfigPath        string
	ConfigPathSource  string
	LogLevel          types.LogLevel
	DryRun            bool
	ForceCLI          bool
	Support           bool
	SupportGitHubUser string
	SupportIssueID    string
	ShowVersion       bool
	ShowHelp          bool
	Upgrade           bool
	ForceNewKey       bool
	Decrypt           bool
	Restore           bool
	Install           bool
	NewInstall        bool
	UpgradeConfig     bool
	UpgradeConfigDry  bool
	EnvMigration      bool
	EnvMigrationDry   bool
	LegacyEnvPath     string
}

// Parse parses command-line arguments and returns Args struct
func Parse() *Args {
	args := &Args{}

	configFlag := newStringFlag(defaultConfigPath)

	// Define flags
	flag.Var(configFlag, "config", "Path to configuration file")
	flag.Var(configFlag, "c", "Path to configuration file (shorthand)")

	var logLevelStr string
	flag.StringVar(&logLevelStr, "log-level", "",
		"Log level (debug|info|warning|error|critical)")
	flag.StringVar(&logLevelStr, "l", "",
		"Log level (shorthand)")

	flag.BoolVar(&args.DryRun, "dry-run", false,
		"Perform a dry run without making actual changes")
	flag.BoolVar(&args.DryRun, "n", false,
		"Perform a dry run (shorthand)")

	flag.BoolVar(&args.Support, "support", false,
		"Run backup in support mode (force debug log level and send a support email with the attached log to github-support@tis24.it)")
	flag.BoolVar(&args.ForceCLI, "cli", false,
		"Use CLI prompts instead of TUI for interactive workflows (works with --install/--new-install/--newkey/--decrypt/--restore)")

	flag.BoolVar(&args.ShowVersion, "version", false,
		"Show version information")
	flag.BoolVar(&args.ShowVersion, "v", false,
		"Show version information (shorthand)")

	flag.BoolVar(&args.ShowHelp, "help", false,
		"Show help message")
	flag.BoolVar(&args.ShowHelp, "h", false,
		"Show help message (shorthand)")

	flag.BoolVar(&args.ForceNewKey, "newkey", false,
		"Reset AGE recipients and run the interactive setup (interactive mode only)")
	flag.BoolVar(&args.ForceNewKey, "age-newkey", false,
		"Alias for --newkey")

	flag.BoolVar(&args.Decrypt, "decrypt", false,
		"Run the decrypt workflow (converts encrypted bundles into plaintext bundles)")
	flag.BoolVar(&args.Restore, "restore", false,
		"Run the restore workflow (select bundle, optionally decrypt, apply to system)")
	flag.BoolVar(&args.Install, "install", false,
		"Run the interactive installer (generate/configure backup.env)")
	flag.BoolVar(&args.NewInstall, "new-install", false,
		"Reset the installation directory (preserving env/identity) and launch the interactive installer")
	flag.BoolVar(&args.Upgrade, "upgrade", false,
		"Download and install the latest ProxSave binary (without modifying backup.env)")
	flag.BoolVar(&args.EnvMigration, "env-migration", false,
		"Run the installer and migrate a legacy Bash backup.env to the Go template")
	flag.BoolVar(&args.EnvMigrationDry, "env-migration-dry-run", false,
		"Preview the installer + legacy env migration without writing files")
	flag.StringVar(&args.LegacyEnvPath, "old-env", "",
		"Path to the legacy Bash backup.env used during --env-migration")

	flag.BoolVar(&args.UpgradeConfig, "upgrade-config", false,
		"Upgrade configuration file using the embedded template (adds missing keys, preserves existing and custom keys)")

	flag.BoolVar(&args.UpgradeConfigDry, "upgrade-config-dry-run", false,
		"Plan configuration upgrade using the embedded template without modifying the file (reports missing and custom keys)")

	// Custom usage message
	flag.Usage = func() {
		printHelp(os.Stderr, os.Args[0])
	}

	// Parse flags
	flag.Parse()

	args.ConfigPath = configFlag.value
	if configFlag.set {
		args.ConfigPathSource = configSourceFlag
	} else {
		args.ConfigPathSource = configSourceDefault
	}

	// Parse log level if provided
	if logLevelStr != "" {
		args.LogLevel = parseLogLevel(logLevelStr)
	} else {
		args.LogLevel = types.LogLevelNone // Will be overridden by config
	}

	return args
}

// parseLogLevel converts string to LogLevel
func parseLogLevel(s string) types.LogLevel {
	switch s {
	case "debug", "5":
		return types.LogLevelDebug
	case "info", "4":
		return types.LogLevelInfo
	case "warning", "3":
		return types.LogLevelWarning
	case "error", "2":
		return types.LogLevelError
	case "critical", "1":
		return types.LogLevelCritical
	case "none", "0":
		return types.LogLevelNone
	default:
		return types.LogLevelInfo
	}
}

// ShowHelp displays help message and exits
func ShowHelp() {
	printHelp(os.Stderr, os.Args[0])
	os.Exit(0)
}

// ShowVersion displays version information and exits
func ShowVersion() {
	printVersion(os.Stdout)
	os.Exit(0)
}

func printHelp(w io.Writer, argv0 string) {
	fmt.Fprintf(w, "Usage: %s [options]\n\n", argv0)
	fmt.Fprintln(w, "ProxSave")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Options:")
	flag.PrintDefaults()
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintf(w, "  %s -c /path/to/config.env\n", argv0)
	fmt.Fprintf(w, "  %s --dry-run --log-level debug\n", argv0)
	fmt.Fprintf(w, "  %s --version\n", argv0)
}

func printVersion(w io.Writer) {
	fmt.Fprintln(w, "ProxSave")
	fmt.Fprintln(w, "Version: 0.2.0-dev")
	fmt.Fprintln(w, "Build: development")
	fmt.Fprintln(w, "Author: tis24dev")
}

type stringFlag struct {
	value string
	set   bool
}

func newStringFlag(defaultValue string) *stringFlag {
	return &stringFlag{value: defaultValue}
}

func (s *stringFlag) String() string {
	return s.value
}

func (s *stringFlag) Set(val string) error {
	s.value = val
	s.set = true
	return nil
}
