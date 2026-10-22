package orchestrator

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestSystemTypeCapabilities(t *testing.T) {
	tests := []struct {
		name        string
		systemType  SystemType
		wantPVE     bool
		wantPBS     bool
		wantTargets []string
	}{
		{
			name:        "pve",
			systemType:  SystemTypePVE,
			wantPVE:     true,
			wantPBS:     false,
			wantTargets: []string{"pve"},
		},
		{
			name:        "pbs",
			systemType:  SystemTypePBS,
			wantPVE:     false,
			wantPBS:     true,
			wantTargets: []string{"pbs"},
		},
		{
			name:        "dual",
			systemType:  SystemTypeDual,
			wantPVE:     true,
			wantPBS:     true,
			wantTargets: []string{"pve", "pbs"},
		},
		{
			name:        "unknown",
			systemType:  SystemTypeUnknown,
			wantPVE:     false,
			wantPBS:     false,
			wantTargets: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.systemType.SupportsPVE(); got != tt.wantPVE {
				t.Fatalf("SupportsPVE() = %v; want %v", got, tt.wantPVE)
			}
			if got := tt.systemType.SupportsPBS(); got != tt.wantPBS {
				t.Fatalf("SupportsPBS() = %v; want %v", got, tt.wantPBS)
			}
			if got := tt.systemType.Targets(); !reflect.DeepEqual(got, tt.wantTargets) {
				t.Fatalf("Targets() = %v; want %v", got, tt.wantTargets)
			}
		})
	}
}

func TestSystemTypeOverlaps(t *testing.T) {
	tests := []struct {
		name  string
		left  SystemType
		right SystemType
		want  bool
	}{
		{name: "same pve", left: SystemTypePVE, right: SystemTypePVE, want: true},
		{name: "same pbs", left: SystemTypePBS, right: SystemTypePBS, want: true},
		{name: "dual with pve", left: SystemTypeDual, right: SystemTypePVE, want: true},
		{name: "dual with pbs", left: SystemTypeDual, right: SystemTypePBS, want: true},
		{name: "pve with pbs", left: SystemTypePVE, right: SystemTypePBS, want: false},
		{name: "unknown with pve", left: SystemTypeUnknown, right: SystemTypePVE, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.left.Overlaps(tt.right); got != tt.want {
				t.Fatalf("Overlaps(%v, %v) = %v; want %v", tt.left, tt.right, got, tt.want)
			}
		})
	}
}

func TestValidateCompatibility_Mismatch(t *testing.T) {
	if err := ValidateCompatibility(SystemTypePVE, SystemTypePBS); err == nil {
		t.Fatalf("expected incompatibility error")
	}
}

func TestValidateCompatibility_Branches(t *testing.T) {
	tests := []struct {
		name        string
		current     SystemType
		backup      SystemType
		wantNil     bool
		wantContain string
	}{
		{
			name:        "unknown current warns",
			current:     SystemTypeUnknown,
			backup:      SystemTypePVE,
			wantContain: "cannot detect current system type",
		},
		{
			name:    "unknown backup allowed",
			current: SystemTypePVE,
			backup:  SystemTypeUnknown,
			wantNil: true,
		},
		{
			name:    "exact match dual",
			current: SystemTypeDual,
			backup:  SystemTypeDual,
			wantNil: true,
		},
		{
			name:        "partial overlap",
			current:     SystemTypePVE,
			backup:      SystemTypeDual,
			wantContain: "partial compatibility",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCompatibility(tt.current, tt.backup)
			if tt.wantNil {
				if err != nil {
					t.Fatalf("ValidateCompatibility(%v, %v) = %v; want nil", tt.current, tt.backup, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateCompatibility(%v, %v) = nil; want error containing %q", tt.current, tt.backup, tt.wantContain)
			}
			if !strings.Contains(err.Error(), tt.wantContain) {
				t.Fatalf("ValidateCompatibility(%v, %v) = %q; want substring %q", tt.current, tt.backup, err.Error(), tt.wantContain)
			}
		})
	}
}

// stubDetection points DetectCurrentSystem at a fixed verdict. The rule itself lives
// in internal/environment and is exercised there; what this file has to prove is that
// restore asks that rule rather than keeping a second one of its own.
func stubDetection(t *testing.T, info *environment.EnvironmentInfo, err error) {
	t.Helper()
	orig := detectEnvironment
	t.Cleanup(func() { detectEnvironment = orig })
	detectEnvironment = func() (*environment.EnvironmentInfo, error) { return info, err }
}

func TestDetectCurrentSystem_Unknown(t *testing.T) {
	stubDetection(t, &environment.EnvironmentInfo{Type: types.ProxmoxUnknown}, errors.New("nothing detected"))

	if got := DetectCurrentSystem(); got != SystemTypeUnknown {
		t.Fatalf("expected unknown system, got %s", got)
	}
}

// TestDetectCurrentSystemIgnoresLeftoverPBSFiles is the restore half of issue #315.
// The rule this replaced read /etc/proxmox-backup directly, so a host that had PBS
// removed was restored onto as if it were still a backup server: PBS categories were
// offered and NeedsPBSServices asked systemd to stop services the host does not have.
func TestDetectCurrentSystemIgnoresLeftoverPBSFiles(t *testing.T) {
	stubDetection(t, &environment.EnvironmentInfo{
		Type:        types.ProxmoxVE,
		PBSResidual: "directory (/etc/proxmox-backup)",
	}, nil)

	if got := DetectCurrentSystem(); got != SystemTypePVE {
		t.Fatalf("DetectCurrentSystem() = %s, want %s", got, SystemTypePVE)
	}
}

// TestDetectCurrentSystemSurvivesANilVerdict: detection never returns nil today, and
// a nil here must not become a PVE restore by accident.
func TestDetectCurrentSystemSurvivesANilVerdict(t *testing.T) {
	stubDetection(t, nil, errors.New("boom"))

	if got := DetectCurrentSystem(); got != SystemTypeUnknown {
		t.Fatalf("DetectCurrentSystem() = %s, want %s", got, SystemTypeUnknown)
	}
}

func TestDetectCurrentSystem_Branches(t *testing.T) {
	tests := []struct {
		name     string
		detected types.ProxmoxType
		want     SystemType
	}{
		{name: "pve only", detected: types.ProxmoxVE, want: SystemTypePVE},
		{name: "pbs only", detected: types.ProxmoxBS, want: SystemTypePBS},
		{name: "dual", detected: types.ProxmoxDual, want: SystemTypeDual},
		{name: "unknown", detected: types.ProxmoxUnknown, want: SystemTypeUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubDetection(t, &environment.EnvironmentInfo{Type: tt.detected}, nil)

			if got := DetectCurrentSystem(); got != tt.want {
				t.Fatalf("DetectCurrentSystem() = %s; want %s", got, tt.want)
			}
		})
	}
}

func TestParseSystemTypeString_AcceptsFullNames(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  SystemType
	}{
		{
			name:  "pve full name with space",
			input: "Proxmox VE",
			want:  SystemTypePVE,
		},
		{
			name:  "pbs generic full name",
			input: "Proxmox Backup",
			want:  SystemTypePBS,
		},
		{
			name:  "pbs full server name",
			input: "Proxmox Backup Server",
			want:  SystemTypePBS,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseSystemTypeString(tt.input); got != tt.want {
				t.Fatalf("parseSystemTypeString(%q) = %v; want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseSystemTypeString_DualAndUnknown(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  SystemType
	}{
		{name: "dual literal", input: "dual", want: SystemTypeDual},
		{name: "pve then pbs", input: "pve,pbs", want: SystemTypeDual},
		{name: "pbs then pve", input: "pbs,pve", want: SystemTypeDual},
		{name: "unknown", input: "plain-linux", want: SystemTypeUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseSystemTypeString(tt.input); got != tt.want {
				t.Fatalf("parseSystemTypeString(%q) = %v; want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseSystemTargets(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   SystemType
	}{
		{name: "pve only", values: []string{"pve"}, want: SystemTypePVE},
		{name: "pbs only", values: []string{"pbs"}, want: SystemTypePBS},
		{name: "mixed targets", values: []string{"pve", "proxmox backup server"}, want: SystemTypeDual},
		{name: "explicit dual", values: []string{"dual"}, want: SystemTypeDual},
		{name: "unknown", values: []string{"plain-linux"}, want: SystemTypeUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseSystemTargets(tt.values); got != tt.want {
				t.Fatalf("parseSystemTargets(%v) = %v; want %v", tt.values, got, tt.want)
			}
		})
	}
}

func TestDetectBackupType_UsesTargets(t *testing.T) {
	manifest := &backup.Manifest{
		ProxmoxTargets: []string{"pve", "pbs"},
		ProxmoxType:    "pve",
		Hostname:       "pbs-host",
	}

	if got := DetectBackupType(manifest); got != SystemTypeDual {
		t.Fatalf("DetectBackupType() = %v; want %v", got, SystemTypeDual)
	}
}
