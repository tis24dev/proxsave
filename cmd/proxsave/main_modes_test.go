package main

import (
	"reflect"
	"testing"

	"github.com/tis24dev/proxsave/internal/cli"
)

func TestValidateModeCompatibility(t *testing.T) {
	tests := []struct {
		name string
		args *cli.Args
		want []string
	}{
		{
			name: "backup default allowed",
			args: &cli.Args{},
		},
		{
			name: "support restore allowed",
			args: &cli.Args{Support: true, Restore: true},
		},
		{
			name: "cleanup guards rejects support and restore first",
			args: &cli.Args{CleanupGuards: true, Support: true, Restore: true},
			want: []string{"--cleanup-guards cannot be combined with: --support, --restore"},
		},
		{
			name: "support rejects decrypt",
			args: &cli.Args{Support: true, Decrypt: true},
			want: []string{
				"Support mode cannot be combined with: --decrypt",
				"--support is only available for the standard backup run or --restore.",
			},
		},
		{
			name: "support rejects config utility modes",
			args: &cli.Args{Support: true, UpgradeConfigDry: true},
			want: []string{
				"Support mode cannot be combined with: --upgrade-config",
				"--support is only available for the standard backup run or --restore.",
			},
		},
		{
			name: "install new install conflict",
			args: &cli.Args{Install: true, NewInstall: true},
			want: []string{"Cannot use --install and --new-install together. Choose one installation mode."},
		},
		{
			name: "upgrade install conflict",
			args: &cli.Args{Upgrade: true, Install: true},
			want: []string{"Cannot use --upgrade together with --install or --new-install."},
		},
		{
			name: "accumulates all compatibility violations",
			args: &cli.Args{CleanupGuards: true, Support: true, Decrypt: true, Install: true, NewInstall: true, Upgrade: true},
			want: []string{
				"--cleanup-guards cannot be combined with: --support, --decrypt, --install, --new-install, --upgrade",
				"Support mode cannot be combined with: --decrypt, --install, --new-install",
				"--support is only available for the standard backup run or --restore.",
				"Cannot use --install and --new-install together. Choose one installation mode.",
				"Cannot use --upgrade together with --install or --new-install.",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validateModeCompatibility(tt.args)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("validateModeCompatibility() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
