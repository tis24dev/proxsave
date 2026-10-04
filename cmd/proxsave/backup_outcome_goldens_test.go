package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// Characterization lock for the dashboard outcome screen (buildBackupOutcomePrompt) with
// its colours. The inputs are the ones the notification goldens are built from
// (internal/orchestrator TestNotificationGoldens): the stats the run ends with and the
// exit code and LOG_FILE the screen reads. Every colour sequence is written into the
// golden as the colour it sets, so the file says which colour each status word and the
// banner get.
//
// The styles render the same bytes whatever the terminal says (lipgloss v2 only adapts
// colours in its writers, never in Style.Render); each case is rendered again under
// TERM=dumb, NO_COLOR=1, CI=true and without COLORTERM and must not change.
//
// These goldens record what the code does TODAY. Regenerate deliberately, after the
// inputs: go test ./cmd/proxsave/ -run TestBackupOutcomeGoldens -update
const (
	backupOutcomeGoldenInputDir = "../../internal/orchestrator/testdata/notification_goldens"
	backupOutcomeGoldenDir      = "testdata/backup_outcome_goldens"
)

// backupOutcomeGoldenInput mirrors outcome.json.
type backupOutcomeGoldenInput struct {
	ExitCode  int    `json:"exit_code"`
	LogFile   string `json:"log_file"`
	WithStats bool   `json:"with_stats"`
}

// backupOutcomeGoldenEnvs are the environments the output must not depend on. A nil
// value unsets the variable.
var backupOutcomeGoldenEnvs = []map[string]*string{
	{"TERM": strPtr("dumb")},
	{"NO_COLOR": strPtr("1")},
	{"CI": strPtr("true")},
	{"COLORTERM": nil},
	{"TERM": strPtr("dumb"), "NO_COLOR": strPtr("1"), "CI": strPtr("true"), "COLORTERM": nil},
	{"TERM": strPtr("xterm-256color"), "COLORTERM": strPtr("truecolor")},
}

func strPtr(s string) *string { return &s }

func readBackupOutcomeGoldenJSON(t *testing.T, path string, v interface{}) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	again, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("re-encode %s: %v", path, err)
	}
	if string(append(again, '\n')) != string(raw) {
		t.Fatalf("%s does not survive a decode/encode round trip", path)
	}
}

// loadBackupOutcomeGoldenCase builds the backupModeResult the screen is rendered from.
func loadBackupOutcomeGoldenCase(t *testing.T, dir string) (backupModeResult, string) {
	t.Helper()
	var in backupOutcomeGoldenInput
	readBackupOutcomeGoldenJSON(t, filepath.Join(dir, "outcome.json"), &in)
	res := backupModeResult{exitCode: in.ExitCode}
	if in.WithStats {
		var stats orchestrator.BackupStats
		readBackupOutcomeGoldenJSON(t, filepath.Join(dir, "stats.json"), &stats)
		res.supportStats = &stats
	}
	return res, in.LogFile
}

// renderBackupOutcomeGolden renders the screen with a clean default logger (no issue
// lines: the run's WARNING lines are not part of the inputs) and LOG_FILE set the way
// the runtime exports it.
func renderBackupOutcomeGolden(t *testing.T, res backupModeResult, logFile string) string {
	t.Helper()
	prev := logging.GetDefaultLogger()
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)
	logging.SetDefaultLogger(logger)
	defer logging.SetDefaultLogger(prev)
	t.Setenv("LOG_FILE", logFile)
	return buildBackupOutcomePrompt(res)
}

var backupOutcomeSGRRe = regexp.MustCompile("\x1b\\[([0-9;]*)m")

// backupOutcomeColorNames names the theme palette, so the golden reads "Green #22C55E".
func backupOutcomeColorNames() map[string]string {
	names := map[string]string{}
	for name, c := range map[string]color.Color{
		"Orange": theme.Orange, "Dark": theme.Dark, "Gray": theme.Gray, "Light": theme.Light,
		"Green": theme.Green, "Red": theme.Red, "Yellow": theme.Yellow, "Blue": theme.Blue,
		"Magenta": theme.Magenta, "White": theme.White, "Background": theme.Background, "Surface": theme.Surface,
	} {
		r, g, b, _ := c.RGBA()
		names[fmt.Sprintf("#%02X%02X%02X", r>>8, g>>8, b>>8)] = name
	}
	return names
}

// describeBackupOutcomeSGR writes every SGR sequence as the attributes it sets:
// "{bold fg Red #EF4444}" for "\x1b[1;38;2;239;68;68m", "{/}" for the reset. Text
// between sequences is kept byte for byte; any other escape is left as it is.
func describeBackupOutcomeSGR(s string) string {
	names := backupOutcomeColorNames()
	return backupOutcomeSGRRe.ReplaceAllStringFunc(s, func(seq string) string {
		params := backupOutcomeSGRRe.FindStringSubmatch(seq)[1]
		if params == "" || params == "0" {
			return "{/}"
		}
		parts := strings.Split(params, ";")
		var attrs []string
		for i := 0; i < len(parts); i++ {
			switch p := parts[i]; {
			case p == "1":
				attrs = append(attrs, "bold")
			case (p == "38" || p == "48") && i+4 < len(parts) && parts[i+1] == "2":
				var rgb [3]int
				for k := 0; k < 3; k++ {
					rgb[k], _ = strconv.Atoi(parts[i+2+k])
				}
				hex := fmt.Sprintf("#%02X%02X%02X", rgb[0], rgb[1], rgb[2])
				kind := "fg"
				if p == "48" {
					kind = "bg"
				}
				if name := names[hex]; name != "" {
					attrs = append(attrs, kind+" "+name+" "+hex)
				} else {
					attrs = append(attrs, kind+" "+hex)
				}
				i += 4
			default:
				attrs = append(attrs, "sgr "+p)
			}
		}
		return "{" + strings.Join(attrs, " ") + "}"
	})
}

func setBackupOutcomeGoldenEnv(t *testing.T, env map[string]*string) {
	t.Helper()
	for name, value := range env {
		t.Setenv(name, "")
		if value == nil {
			if err := os.Unsetenv(name); err != nil {
				t.Fatalf("unset %s: %v", name, err)
			}
			continue
		}
		t.Setenv(name, *value)
	}
}

// TestBackupOutcomeGoldens pins the outcome screen of every notification case.
func TestBackupOutcomeGoldens(t *testing.T) {
	entries, err := os.ReadDir(backupOutcomeGoldenInputDir)
	if err != nil {
		t.Fatalf("read inputs %s (run the orchestrator goldens first): %v", backupOutcomeGoldenInputDir, err)
	}
	known := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		known[name+".golden"] = true
		t.Run(name, func(t *testing.T) {
			res, logFile := loadBackupOutcomeGoldenCase(t, filepath.Join(backupOutcomeGoldenInputDir, name))
			raw := renderBackupOutcomeGolden(t, res, logFile)
			for i, env := range backupOutcomeGoldenEnvs {
				t.Run(fmt.Sprintf("env%d", i), func(t *testing.T) {
					setBackupOutcomeGoldenEnv(t, env)
					if got := renderBackupOutcomeGolden(t, res, logFile); got != raw {
						t.Fatalf("outcome depends on the environment %v:\n%q\nwant\n%q", env, got, raw)
					}
				})
			}
			assertBackupOutcomeGolden(t, name+".golden", []byte(describeBackupOutcomeSGR(raw)+"\n"))
		})
	}
	if len(known) == 0 {
		t.Fatalf("no cases under %s", backupOutcomeGoldenInputDir)
	}
	goldens, err := os.ReadDir(backupOutcomeGoldenDir)
	if err != nil {
		t.Fatalf("read %s: %v", backupOutcomeGoldenDir, err)
	}
	for _, g := range goldens {
		if known[g.Name()] {
			continue
		}
		if *updateGoldens {
			if err := os.Remove(filepath.Join(backupOutcomeGoldenDir, g.Name())); err != nil {
				t.Fatalf("remove stale %s: %v", g.Name(), err)
			}
			continue
		}
		t.Fatalf("%s/%s has no input case", backupOutcomeGoldenDir, g.Name())
	}
}

func assertBackupOutcomeGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join(backupOutcomeGoldenDir, name)
	if *updateGoldens {
		if err := os.MkdirAll(backupOutcomeGoldenDir, 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", name, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update to create): %v", name, err)
	}
	if !bytes.Equal(want, got) {
		t.Fatalf("golden mismatch for %s\n--- want ---\n%s\n--- got ---\n%s", name, want, got)
	}
}
