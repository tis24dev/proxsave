package orchestrator

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/backup"
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
	orig := compatFS
	defer func() { compatFS = orig }()

	fake := NewFakeFS()
	defer func() { _ = os.RemoveAll(fake.Root) }()
	compatFS = fake
	if err := os.MkdirAll(fake.onDisk("/etc/pve"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

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

func TestDetectCurrentSystem_Unknown(t *testing.T) {
	orig := compatFS
	defer func() { compatFS = orig }()
	fake := NewFakeFS()
	defer func() { _ = os.RemoveAll(fake.Root) }()
	compatFS = fake

	if got := DetectCurrentSystem(); got != SystemTypeUnknown {
		t.Fatalf("expected unknown system, got %s", got)
	}
}

func TestDetectCurrentSystem_Branches(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, fake *FakeFS)
		want  SystemType
	}{
		{
			name: "pve only",
			setup: func(t *testing.T, fake *FakeFS) {
				t.Helper()
				if err := fake.AddDir("/etc/pve"); err != nil {
					t.Fatalf("add dir: %v", err)
				}
			},
			want: SystemTypePVE,
		},
		{
			name: "pbs only",
			setup: func(t *testing.T, fake *FakeFS) {
				t.Helper()
				if err := fake.AddDir("/etc/proxmox-backup"); err != nil {
					t.Fatalf("add dir: %v", err)
				}
			},
			want: SystemTypePBS,
		},
		{
			name: "dual",
			setup: func(t *testing.T, fake *FakeFS) {
				t.Helper()
				if err := fake.AddDir("/etc/pve"); err != nil {
					t.Fatalf("add pve dir: %v", err)
				}
				if err := fake.AddDir("/etc/proxmox-backup"); err != nil {
					t.Fatalf("add pbs dir: %v", err)
				}
			},
			want: SystemTypeDual,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := compatFS
			defer func() { compatFS = orig }()
			fake := NewFakeFS()
			defer func() { _ = os.RemoveAll(fake.Root) }()
			compatFS = fake
			tt.setup(t, fake)

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

func TestGetSystemInfoDetectsPVE(t *testing.T) {
	orig := compatFS
	defer func() { compatFS = orig }()

	fake := NewFakeFS()
	defer func() { _ = os.RemoveAll(fake.Root) }()
	compatFS = fake
	if err := fake.AddDir("/etc/pve"); err != nil {
		t.Fatalf("add dir: %v", err)
	}
	if err := fake.WriteFile("/etc/pve-release", []byte("Proxmox VE 8.1\n"), 0o644); err != nil {
		t.Fatalf("write release: %v", err)
	}
	if err := fake.WriteFile("/etc/hostname", []byte("pve-node\n"), 0o644); err != nil {
		t.Fatalf("write hostname: %v", err)
	}

	info := GetSystemInfo()
	if info["type"] != string(SystemTypePVE) {
		t.Fatalf("unexpected type: %s", info["type"])
	}
	if info["type_name"] != GetSystemTypeString(SystemTypePVE) {
		t.Fatalf("unexpected type name: %s", info["type_name"])
	}
	if info["version"] != "Proxmox VE 8.1" {
		t.Fatalf("unexpected version: %s", info["version"])
	}
	if info["hostname"] != "pve-node" {
		t.Fatalf("unexpected hostname: %s", info["hostname"])
	}
}

func TestGetSystemInfoDetectsPBS(t *testing.T) {
	orig := compatFS
	defer func() { compatFS = orig }()

	fake := NewFakeFS()
	defer func() { _ = os.RemoveAll(fake.Root) }()
	compatFS = fake
	if err := fake.AddDir("/etc/proxmox-backup"); err != nil {
		t.Fatalf("add dir: %v", err)
	}
	if err := fake.WriteFile("/etc/proxmox-backup-release", []byte("Proxmox Backup Server 3.0\n"), 0o644); err != nil {
		t.Fatalf("write release: %v", err)
	}
	if err := fake.WriteFile("/etc/hostname", []byte("pbs-node\n"), 0o644); err != nil {
		t.Fatalf("write hostname: %v", err)
	}

	info := GetSystemInfo()
	if info["type"] != string(SystemTypePBS) {
		t.Fatalf("unexpected type: %s", info["type"])
	}
	if info["version"] != "Proxmox Backup Server 3.0" {
		t.Fatalf("unexpected version: %s", info["version"])
	}
	if info["hostname"] != "pbs-node" {
		t.Fatalf("unexpected hostname: %s", info["hostname"])
	}
}

func TestGetSystemInfo_DualAndUnknown(t *testing.T) {
	t.Run("dual", func(t *testing.T) {
		orig := compatFS
		defer func() { compatFS = orig }()

		fake := NewFakeFS()
		defer func() { _ = os.RemoveAll(fake.Root) }()
		compatFS = fake
		if err := fake.AddDir("/etc/pve"); err != nil {
			t.Fatalf("add pve dir: %v", err)
		}
		if err := fake.AddDir("/etc/proxmox-backup"); err != nil {
			t.Fatalf("add pbs dir: %v", err)
		}
		if err := fake.WriteFile("/etc/pve-release", []byte("Proxmox VE 8.2\n"), 0o644); err != nil {
			t.Fatalf("write pve release: %v", err)
		}
		if err := fake.WriteFile("/etc/proxmox-backup-release", []byte("Proxmox Backup Server 3.4\n"), 0o644); err != nil {
			t.Fatalf("write pbs release: %v", err)
		}
		if err := fake.WriteFile("/etc/hostname", []byte("dual-node\n"), 0o644); err != nil {
			t.Fatalf("write hostname: %v", err)
		}

		info := GetSystemInfo()
		if info["type"] != string(SystemTypeDual) {
			t.Fatalf("unexpected type: %s", info["type"])
		}
		if info["pve_version"] != "Proxmox VE 8.2" {
			t.Fatalf("unexpected pve_version: %s", info["pve_version"])
		}
		if info["pbs_version"] != "Proxmox Backup Server 3.4" {
			t.Fatalf("unexpected pbs_version: %s", info["pbs_version"])
		}
		if _, ok := info["version"]; ok {
			t.Fatalf("dual info should not expose single version field: %v", info["version"])
		}
	})

	t.Run("unknown", func(t *testing.T) {
		orig := compatFS
		defer func() { compatFS = orig }()

		fake := NewFakeFS()
		defer func() { _ = os.RemoveAll(fake.Root) }()
		compatFS = fake
		if err := fake.WriteFile("/etc/hostname", []byte("generic-node\n"), 0o644); err != nil {
			t.Fatalf("write hostname: %v", err)
		}

		info := GetSystemInfo()
		if info["type"] != string(SystemTypeUnknown) {
			t.Fatalf("unexpected type: %s", info["type"])
		}
		if info["type_name"] != GetSystemTypeString(SystemTypeUnknown) {
			t.Fatalf("unexpected type name: %s", info["type_name"])
		}
		if info["hostname"] != "generic-node" {
			t.Fatalf("unexpected hostname: %s", info["hostname"])
		}
		if _, ok := info["version"]; ok {
			t.Fatalf("unknown info should not expose version field: %v", info["version"])
		}
	})
}

func TestCheckSystemRequirements(t *testing.T) {
	t.Run("partial compatibility", func(t *testing.T) {
		orig := compatFS
		defer func() { compatFS = orig }()

		fake := NewFakeFS()
		defer func() { _ = os.RemoveAll(fake.Root) }()
		compatFS = fake
		for _, dir := range []string{"/etc", "/var", "/usr", "/etc/pve", "/etc/proxmox-backup"} {
			if err := fake.AddDir(dir); err != nil {
				t.Fatalf("add dir %s: %v", dir, err)
			}
		}

		warnings := CheckSystemRequirements(&backup.Manifest{ProxmoxTargets: []string{"pve"}})
		if len(warnings) != 1 {
			t.Fatalf("CheckSystemRequirements() returned %d warnings; want 1 (%v)", len(warnings), warnings)
		}
		if !strings.Contains(warnings[0], "Partial system type match") {
			t.Fatalf("unexpected warning: %v", warnings)
		}
	})

	t.Run("mismatch and missing directories", func(t *testing.T) {
		orig := compatFS
		defer func() { compatFS = orig }()

		fake := NewFakeFS()
		defer func() { _ = os.RemoveAll(fake.Root) }()
		compatFS = fake
		if err := fake.AddDir("/etc/proxmox-backup"); err != nil {
			t.Fatalf("add dir: %v", err)
		}
		fake.StatErr["/"] = os.ErrPermission

		warnings := CheckSystemRequirements(&backup.Manifest{ProxmoxTargets: []string{"pve"}})
		joined := strings.Join(warnings, "\n")
		for _, needle := range []string{
			"System type mismatch",
			"Required directory missing: /var",
			"Required directory missing: /usr",
			"Cannot access root filesystem",
		} {
			if !strings.Contains(joined, needle) {
				t.Fatalf("warnings %v do not contain %q", warnings, needle)
			}
		}
	})
}
