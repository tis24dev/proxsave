package main

import (
	"reflect"
	"testing"

	"github.com/tis24dev/proxsave/internal/cli"
)

// operationModeSetters names every flag that selects what the run does, independently
// of operationModes, so a mode dropped from that list fails here.
var operationModeSetters = map[string]func(*cli.Args){
	"--backup":                 func(a *cli.Args) { a.Backup = true },
	"--restore":                func(a *cli.Args) { a.Restore = true },
	"--decrypt":                func(a *cli.Args) { a.Decrypt = true },
	"--install":                func(a *cli.Args) { a.Install = true },
	"--new-install":            func(a *cli.Args) { a.NewInstall = true },
	"--upgrade":                func(a *cli.Args) { a.Upgrade = true },
	"--newkey":                 func(a *cli.Args) { a.ForceNewKey = true },
	"--upgrade-config":         func(a *cli.Args) { a.UpgradeConfig = true },
	"--upgrade-config-dry-run": func(a *cli.Args) { a.UpgradeConfigDry = true },
	"--upgrade-config-json":    func(a *cli.Args) { a.UpgradeConfigJSON = true },
	"--show-whatsnew":          func(a *cli.Args) { a.ShowWhatsnew = true },
	"--upgrade-finalize":       func(a *cli.Args) { a.UpgradeFinalize = true },
	"--cleanup-guards":         func(a *cli.Args) { a.CleanupGuards = true },
	"--daemon":                 func(a *cli.Args) { a.Daemon = true },
	"--daemon-setup":           func(a *cli.Args) { a.DaemonSetup = true },
	"--daemon-remove":          func(a *cli.Args) { a.DaemonRemove = true },
	"--daemon-status":          func(a *cli.Args) { a.DaemonStatus = true },
}

func TestValidateModeCompatibilityRefusesEveryPairOfModes(t *testing.T) {
	for first, setFirst := range operationModeSetters {
		alone := &cli.Args{}
		setFirst(alone)
		if got := validateModeCompatibility(alone); got != nil {
			t.Errorf("%s alone refused: %#v", first, got)
		}
		for second, setSecond := range operationModeSetters {
			if first == second {
				continue
			}
			args := &cli.Args{}
			setFirst(args)
			setSecond(args)
			if got := validateModeCompatibility(args); len(got) == 0 {
				t.Errorf("%s %s accepted", first, second)
			}
		}
	}
}

func TestValidateModeCompatibilitySingleModeLine(t *testing.T) {
	supportOnly := []string{"--support is only available for the standard backup run or --restore."}
	tests := []struct {
		name string
		args *cli.Args
		want []string
	}{
		{
			name: "two modes",
			args: &cli.Args{Restore: true, Decrypt: true},
			want: []string{"--restore cannot be combined with: --decrypt"},
		},
		{
			name: "first in mode order leads",
			args: &cli.Args{Upgrade: true, Restore: true, Backup: true},
			want: []string{"--backup cannot be combined with: --restore, --upgrade"},
		},
		{
			name: "backup with show whatsnew",
			args: &cli.Args{Backup: true, ShowWhatsnew: true},
			want: []string{"--backup cannot be combined with: --show-whatsnew"},
		},
		{
			name: "config utility modes",
			args: &cli.Args{UpgradeConfig: true, UpgradeConfigDry: true},
			want: []string{"--upgrade-config cannot be combined with: --upgrade-config-dry-run"},
		},
		{
			name: "a rule that already refused keeps its own line alone",
			args: &cli.Args{Install: true, NewInstall: true},
			want: []string{"Cannot use --install and --new-install together. Choose one installation mode."},
		},
		{
			name: "support with upgrade",
			args: &cli.Args{Support: true, Upgrade: true},
			want: append([]string{"Support mode cannot be combined with: --upgrade"}, supportOnly...),
		},
		{
			name: "support with upgrade finalize",
			args: &cli.Args{Support: true, UpgradeFinalize: true},
			want: append([]string{"Support mode cannot be combined with: --upgrade-finalize"}, supportOnly...),
		},
		{
			name: "support with show whatsnew",
			args: &cli.Args{Support: true, ShowWhatsnew: true},
			want: append([]string{"Support mode cannot be combined with: --show-whatsnew"}, supportOnly...),
		},
		{name: "support with backup", args: &cli.Args{Support: true, Backup: true}},
		{name: "support with restore", args: &cli.Args{Support: true, Restore: true}},
		{name: "restore in cli mode", args: &cli.Args{Restore: true, ForceCLI: true}},
		{name: "upgrade from local file with auto yes", args: &cli.Args{Upgrade: true, LocalFile: true, UpgradeAutoYes: true}},
		{name: "backup dry run", args: &cli.Args{Backup: true, DryRun: true}},
		{
			name: "upgrade finalize with its values",
			args: &cli.Args{UpgradeFinalize: true, UpgradeFinalizeVersion: "0.44.1", UpgradeFinalizeSkipWhatsnew: true, UpgradeFinalizeSkipDaemonRestart: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validateModeCompatibility(tt.args); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("validateModeCompatibility() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
