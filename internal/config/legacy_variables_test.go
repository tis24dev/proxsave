package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateLegacyLinesKeepsTheValueTheLoaderRead(t *testing.T) {
	cases := map[string]struct {
		in      string
		want    string
		renamed []bool
	}{
		"old name alone is renamed in place": {
			in:      "A=1\nLOCAL_LOG_PATH=/y\nB=2",
			want:    "A=1\nLOG_PATH=/y\nB=2",
			renamed: []bool{true},
		},
		"old name read first takes the current name's place": {
			in:      "BACKUP_PATH=/current\nX=1\nLOCAL_BACKUP_PATH=/legacy",
			want:    "BACKUP_PATH=/legacy\nX=1",
			renamed: []bool{true},
		},
		"empty old string had no effect and is dropped": {
			in:      "BACKUP_PATH=/current\nLOCAL_BACKUP_PATH=",
			want:    "BACKUP_PATH=/current",
			renamed: []bool{false},
		},
		"old name read second loses to the current one": {
			in:      "EMAIL_ENABLED=false\nEMAIL_ENABLE=true",
			want:    "EMAIL_ENABLED=false",
			renamed: []bool{false},
		},
		"old name read second is renamed when it is the only one": {
			in:      "EMAIL_FALLBACK_PMF=false",
			want:    "EMAIL_FALLBACK_SENDMAIL=false",
			renamed: []bool{true},
		},
		"an unreadable current integer let the old one through": {
			in:      "RCLONE_TIMEOUT_CONNECTION=abc\nCLOUD_CONNECTIVITY_TIMEOUT=60",
			want:    "RCLONE_TIMEOUT_CONNECTION=60",
			renamed: []bool{true},
		},
		"an empty current list let the old one through": {
			in:      "AGE_RECIPIENT=\nAGE_RECIPIENTS=age1abc",
			want:    "AGE_RECIPIENT=age1abc",
			renamed: []bool{true},
		},
		"export prefix, spacing and comment survive the rename": {
			in:      "export RCLONE_REMOTE=remote # mine",
			want:    "export CLOUD_REMOTE=remote # mine",
			renamed: []bool{true},
		},
		"only the last of a repeated old name was read": {
			in:      "LOCAL_LOG_PATH=/first\nLOCAL_LOG_PATH=/second",
			want:    "LOG_PATH=/second",
			renamed: []bool{true},
		},
		"commented old names are left alone": {
			in:      "# LOCAL_LOG_PATH=/x\nLOG_PATH=/y",
			want:    "# LOCAL_LOG_PATH=/x\nLOG_PATH=/y",
			renamed: nil,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			out, changes := migrateLegacyLines(strings.Split(tc.in, "\n"))
			if got := strings.Join(out, "\n"); got != tc.want {
				t.Fatalf("lines:\n%s\nwant:\n%s", got, tc.want)
			}
			if len(changes) != len(tc.renamed) {
				t.Fatalf("changes = %+v, want %d", changes, len(tc.renamed))
			}
			for i, change := range changes {
				if change.Renamed != tc.renamed[i] {
					t.Fatalf("change %s Renamed = %v, want %v", change.Name, change.Renamed, tc.renamed[i])
				}
			}
		})
	}
}

func TestMigrateLegacyLinesSkipsBlockBodies(t *testing.T) {
	in := "CUSTOM_BACKUP_PATHS=\"\nLOCAL_LOG_PATH=/not/an/assignment\n\"\nLOG_PATH=/y"
	out, changes := migrateLegacyLines(strings.Split(in, "\n"))
	if len(changes) != 0 || strings.Join(out, "\n") != in {
		t.Fatalf("a line inside a block was read as an assignment: %+v", changes)
	}
}

func TestMigrateLegacyVariablesKeepsCRLF(t *testing.T) {
	out, changes := MigrateLegacyVariables("A=1\r\nLOCAL_LOG_PATH=/y\r\n")
	if out != "A=1\r\nLOG_PATH=/y\r\n" || len(changes) != 1 || changes[0].Lines[0] != 2 {
		t.Fatalf("out=%q changes=%+v", out, changes)
	}
	same := "A=1\nLOG_PATH=/y\n"
	if out, changes := MigrateLegacyVariables(same); out != same || changes != nil {
		t.Fatalf("a file without old names changed: %q %+v", out, changes)
	}
}

// The upgrade renames before it merges, so the current name is not added a second time
// from the template, and the file it writes loads with the value the old file had.
func TestUpgradeConfigFileRenamesLegacyVariables(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backup.env")
	body := DefaultEnvTemplate() + "\nLOCAL_LOG_PATH=" + filepath.Join(dir, "legacy-log") + "\nEMAIL_ENABLE=true\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := UpgradeConfigFileWithBaseDir(path, dir)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if !result.Changed || len(result.Legacy) != 2 {
		t.Fatalf("Changed=%v Legacy=%+v", result.Changed, result.Legacy)
	}
	if !result.Legacy[0].Renamed || result.Legacy[0].Name != "LOCAL_LOG_PATH" ||
		result.Legacy[1].Renamed || result.Legacy[1].Name != "EMAIL_ENABLE" {
		t.Fatalf("Legacy = %+v", result.Legacy)
	}
	for _, key := range result.MissingKeys {
		if key == "LOG_PATH" {
			t.Fatal("LOG_PATH was added again from the template")
		}
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(written), "LOCAL_LOG_PATH") || strings.Contains(string(written), "EMAIL_ENABLE=") {
		t.Fatalf("an old name is still in the file:\n%s", written)
	}
	if n := strings.Count(string(written), "\nLOG_PATH="); n != 1 {
		t.Fatalf("LOG_PATH assigned %d times", n)
	}
	cfg, err := LoadConfigWithBaseDir(path, dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.LogPath != filepath.Join(dir, "legacy-log") {
		t.Fatalf("LogPath = %q, want the value LOCAL_LOG_PATH had", cfg.LogPath)
	}
	if cfg.EmailEnabled {
		t.Fatal("EmailEnabled = true, but EMAIL_ENABLED=false was the line in effect")
	}
}
