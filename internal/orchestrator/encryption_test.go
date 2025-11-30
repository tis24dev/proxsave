package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func newEncryptionTestOrchestrator(cfg *config.Config) *Orchestrator {
	logger := logging.New(types.LogLevelError, false)
	return &Orchestrator{logger: logger, cfg: cfg}
}

func TestValidatePassphraseStrength(t *testing.T) {
	tests := []struct {
		name    string
		pass    string
		wantErr bool
	}{
		{"strong", "Str0ng!Passphrase", false},
		{"too short", "Short1!", true},
		{"missing classes", "alllowercasepassword", true},
		{"common password", "Password", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePassphraseStrength([]byte(tt.pass))
			if tt.wantErr && err == nil {
				t.Fatalf("expected error for %q", tt.pass)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error for %q: %v", tt.pass, err)
			}
		})
	}
}

func TestCollectRecipientStringsMergesSources(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "recipients.txt")
	content := strings.Join([]string{
		"age1fromfile",
		"# comment",
		"",
		"ssh-ed25519 AAAAB3Nza",
	}, "\n")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatalf("write recipient file: %v", err)
	}
	cfg := &config.Config{
		EncryptArchive:   true,
		AgeRecipients:    []string{"  age1fromcfg  ", "age1fromcfg"},
		AgeRecipientFile: file,
	}
	o := newEncryptionTestOrchestrator(cfg)

	recs, candidate, err := o.collectRecipientStrings()
	if err != nil {
		t.Fatalf("collectRecipientStrings() error = %v", err)
	}
	if candidate != file {
		t.Fatalf("candidate path = %s, want %s", candidate, file)
	}
	want := []string{"age1fromcfg", "age1fromfile", "ssh-ed25519 AAAAB3Nza"}
	if len(recs) != len(want) {
		t.Fatalf("deduped recipients = %v, want %v", recs, want)
	}
	for i := range want {
		if recs[i] != want[i] {
			t.Fatalf("recipient[%d] = %s, want %s", i, recs[i], want[i])
		}
	}
}

func TestPrepareAgeRecipientsUsesCache(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	cfg := &config.Config{
		EncryptArchive: true,
		AgeRecipients:  []string{id.Recipient().String()},
	}
	o := newEncryptionTestOrchestrator(cfg)

	ctx := context.Background()
	first, err := o.prepareAgeRecipients(ctx)
	if err != nil {
		t.Fatalf("prepareAgeRecipients initial: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("expected 1 recipient, got %d", len(first))
	}

	// Clear config; cached recipients should still be returned.
	o.cfg.AgeRecipients = nil
	second, err := o.prepareAgeRecipients(ctx)
	if err != nil {
		t.Fatalf("prepareAgeRecipients cached: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("expected cached recipient, got %d", len(second))
	}
	if fmt.Sprint(second[0]) != fmt.Sprint(first[0]) {
		t.Fatalf("cached recipient mismatch")
	}
}

func TestWriteAndReadRecipientFileRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "recipients.txt")
	values := []string{"age1alpha", "ssh-ed25519 AAAAB3Nza"}

	if err := writeRecipientFile(path, values); err != nil {
		t.Fatalf("writeRecipientFile: %v", err)
	}

	read, err := readRecipientFile(path)
	if err != nil {
		t.Fatalf("readRecipientFile: %v", err)
	}
	if len(read) != len(values) {
		t.Fatalf("read %d recipients, want %d", len(read), len(values))
	}
	for i := range values {
		if read[i] != values[i] {
			t.Fatalf("value[%d] = %s, want %s", i, read[i], values[i])
		}
	}
}

func TestDedupeRecipientStrings(t *testing.T) {
	input := []string{"  age1alpha  ", "", "age1alpha", "ssh-ed25519 AAA", "ssh-ed25519 AAA"}
	want := []string{"age1alpha", "ssh-ed25519 AAA"}
	got := dedupeRecipientStrings(input)
	if len(got) != len(want) {
		t.Fatalf("dedupe returned %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dedupe[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestParseRecipientStringsRejectsInvalid(t *testing.T) {
	if _, err := parseRecipientStrings([]string{"not-valid"}); err == nil {
		t.Fatal("expected parseRecipientStrings to fail for invalid input")
	}
}

func TestBackupExistingRecipientFileCreatesBackup(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "age.txt")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	if err := backupExistingRecipientFile(path); err != nil {
		t.Fatalf("backupExistingRecipientFile: %v", err)
	}
	matches, err := filepath.Glob(path + ".bak-*")
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected backup file, got %v err=%v", matches, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("original path should have been moved, stat err=%v", err)
	}
}
