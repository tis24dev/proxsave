package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestPrefilterSkipsStructuredConfigs(t *testing.T) {
	tmp := t.TempDir()
	
	// Create structured config (should be skipped)
	pbsDir := filepath.Join(tmp, "etc", "proxmox-backup")
	if err := os.MkdirAll(pbsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	
	pbsCfg := filepath.Join(pbsDir, "datastore.cfg")
	pbsContent := "datastore: Test\n\tpath /mnt/test\n\tcomment Test DS\n"
	if err := os.WriteFile(pbsCfg, []byte(pbsContent), 0o640); err != nil {
		t.Fatalf("write pbs config: %v", err)
	}
	
	// Create normal config with CRLF (should be normalized)
	normalCfg := filepath.Join(tmp, "etc", "normal.cfg")
	normalContent := "option1\r\noption2\r\n"
	if err := os.WriteFile(normalCfg, []byte(normalContent), 0o640); err != nil {
		t.Fatalf("write normal config: %v", err)
	}
	
	// Create log file with CRLF (should be normalized)
	logDir := filepath.Join(tmp, "var", "log")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir log: %v", err)
	}
	logFile := filepath.Join(logDir, "test.log")
	logContent := "line1\r\nline2\r\n"
	if err := os.WriteFile(logFile, []byte(logContent), 0o640); err != nil {
		t.Fatalf("write log: %v", err)
	}
	
	// Run prefilter
	logger := logging.New(types.LogLevelError, false)
	if err := prefilterFiles(context.Background(), logger, tmp, 8*1024*1024); err != nil {
		t.Fatalf("prefilterFiles: %v", err)
	}
	
	// Verify PBS config unchanged (TABs preserved)
	pbsAfter, _ := os.ReadFile(pbsCfg)
	if string(pbsAfter) != pbsContent {
		t.Fatalf("PBS config was modified!\nExpected: %q\nGot: %q", pbsContent, string(pbsAfter))
	}
	if !strings.Contains(string(pbsAfter), "\t") {
		t.Fatalf("PBS config lost TAB indentation")
	}
	
	// Verify normal config normalized (CRLF removed)
	normalAfter, _ := os.ReadFile(normalCfg)
	if strings.Contains(string(normalAfter), "\r") {
		t.Fatalf("Normal config still has CRLF: %q", normalAfter)
	}
	expectedNormal := strings.ReplaceAll(normalContent, "\r", "")
	if string(normalAfter) != expectedNormal {
		t.Fatalf("Normal config not normalized correctly\nExpected: %q\nGot: %q", expectedNormal, string(normalAfter))
	}
	
	// Verify log normalized (CRLF removed)
	logAfter, _ := os.ReadFile(logFile)
	if strings.Contains(string(logAfter), "\r") {
		t.Fatalf("Log file still has CRLF: %q", logAfter)
	}
}
