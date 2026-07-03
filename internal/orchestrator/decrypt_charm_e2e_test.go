package orchestrator

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// End-to-end coverage of the Charm decrypt flow: RunDecryptWorkflowTUI is
// driven through a renderless Session whose output is observed to know when
// each screen is up, replacing the old tcell SimulationScreen harness.

var decryptCharmE2EMu sync.Mutex

// Generous: the age passphrase KDF (scrypt) runs 10-20x slower under the
// race detector.
const charmE2EWaitTimeout = 90 * time.Second

type decryptTUIFixture struct {
	Config              *config.Config
	ConfigPath          string
	BackupDir           string
	BaseDir             string
	DestinationDir      string
	ArchivePlaintext    []byte
	Secret              string
	EncryptedArchive    string
	ExpectedBundlePath  string
	ExpectedArchiveName string
	ExpectedChecksum    string
}

func createDecryptTUIEncryptedFixture(t *testing.T) *decryptTUIFixture {
	t.Helper()

	backupDir := t.TempDir()
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "backup.env")
	if err := os.WriteFile(configPath, []byte("BACKUP_PATH="+backupDir+"\nBASE_DIR="+baseDir+"\n"), 0o600); err != nil {
		t.Fatalf("write config placeholder: %v", err)
	}

	passphrase := "Decrypt123!"
	recipientStr, err := deriveDeterministicRecipientFromPassphrase(passphrase)
	if err != nil {
		t.Fatalf("deriveDeterministicRecipientFromPassphrase: %v", err)
	}
	recipient, err := age.ParseX25519Recipient(recipientStr)
	if err != nil {
		t.Fatalf("age.ParseX25519Recipient: %v", err)
	}

	plaintext := []byte("proxsave decrypt tui e2e plaintext\n")
	archivePath := filepath.Join(backupDir, "backup.tar.xz.age")
	archiveFile, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create encrypted archive: %v", err)
	}

	encWriter, err := age.Encrypt(archiveFile, recipient)
	if err != nil {
		_ = archiveFile.Close()
		t.Fatalf("age.Encrypt: %v", err)
	}
	if _, err := encWriter.Write(plaintext); err != nil {
		_ = encWriter.Close()
		_ = archiveFile.Close()
		t.Fatalf("write plaintext to age writer: %v", err)
	}
	if err := encWriter.Close(); err != nil {
		_ = archiveFile.Close()
		t.Fatalf("close age writer: %v", err)
	}
	if err := archiveFile.Close(); err != nil {
		t.Fatalf("close encrypted archive: %v", err)
	}

	encryptedBytes, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read encrypted archive: %v", err)
	}

	createdAt := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	manifest := &backup.Manifest{
		ArchivePath:    archivePath,
		CreatedAt:      createdAt,
		Hostname:       "node1",
		EncryptionMode: "age",
		ProxmoxType:    "pve",
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(archivePath+".metadata", manifestData, 0o640); err != nil {
		t.Fatalf("write manifest sidecar: %v", err)
	}
	if err := os.WriteFile(archivePath+".sha256", checksumLineForBytes(filepath.Base(archivePath), encryptedBytes), 0o640); err != nil {
		t.Fatalf("write checksum sidecar: %v", err)
	}

	checksum := sha256.Sum256(plaintext)
	expectedArchiveName := "backup.tar.xz"
	destinationDir := filepath.Join(baseDir, "decrypt")

	return &decryptTUIFixture{
		Config: &config.Config{
			BackupPath:       backupDir,
			BaseDir:          baseDir,
			SecondaryEnabled: false,
			CloudEnabled:     false,
		},
		ConfigPath:          configPath,
		BackupDir:           backupDir,
		BaseDir:             baseDir,
		DestinationDir:      destinationDir,
		ArchivePlaintext:    plaintext,
		Secret:              passphrase,
		EncryptedArchive:    archivePath,
		ExpectedBundlePath:  filepath.Join(destinationDir, expectedArchiveName+".decrypted.bundle.tar"),
		ExpectedArchiveName: expectedArchiveName,
		ExpectedChecksum:    hex.EncodeToString(checksum[:]),
	}
}

// charmUIDriver scripts a Session: it waits for deterministic screen-push
// events before sending keys, and polls the rendered output for in-screen
// content (validation errors, list rows).
type charmUIDriver struct {
	t       *testing.T
	buf     *shell.SyncBuffer
	pushes  chan string
	session *shell.Session
}

func newCharmUIDriver(t *testing.T) *charmUIDriver {
	t.Helper()
	return &charmUIDriver{
		t:      t,
		buf:    &shell.SyncBuffer{},
		pushes: make(chan string, 64),
	}
}

func (d *charmUIDriver) start(ctx context.Context, cfg shell.Config) *shell.Session {
	d.session = shell.StartObservedForTest(ctx, cfg, d.buf, func(title string) {
		d.pushes <- title
	})
	return d.session
}

// installCharmUIDriver swaps the newUISession seam so the workflow under test
// runs against an observable, renderless Session.
func installCharmUIDriver(t *testing.T) *charmUIDriver {
	t.Helper()
	d := newCharmUIDriver(t)
	orig := newUISession
	newUISession = func(ctx context.Context, cfg shell.Config) *shell.Session {
		return d.start(ctx, cfg)
	}
	t.Cleanup(func() { newUISession = orig })
	return d
}

// waitScreen blocks until a screen with the given title is pushed, skipping
// intermediate screens (e.g. transient task screens). Keys sent afterwards
// are guaranteed to be processed after the push.
func (d *charmUIDriver) waitScreen(title string) {
	d.t.Helper()
	deadline := time.After(charmE2EWaitTimeout)
	for {
		select {
		case got := <-d.pushes:
			if got == title {
				return
			}
		case <-deadline:
			d.t.Fatalf("timed out waiting for screen %q; output tail:\n%s", title, tailOf(ansi.Strip(d.buf.String()), 2000))
		}
	}
}

// waitOutput polls the cumulative rendered output for text unique to the
// current step (use waitScreen for screen transitions).
func (d *charmUIDriver) waitOutput(text string) {
	d.t.Helper()
	deadline := time.Now().Add(charmE2EWaitTimeout)
	for {
		if strings.Contains(ansi.Strip(d.buf.String()), text) {
			return
		}
		if time.Now().After(deadline) {
			d.t.Fatalf("timed out waiting for %q in UI output; tail:\n%s", text, tailOf(ansi.Strip(d.buf.String()), 2000))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (d *charmUIDriver) keys(script string) {
	d.t.Helper()
	for _, msg := range shell.Keys(script) {
		d.session.Send(msg)
	}
}

func (d *charmUIDriver) typeText(s string) {
	d.t.Helper()
	for _, r := range s {
		d.session.Send(shell.KeyMsg(string(r)))
	}
}

func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func readTarEntries(t *testing.T, tarPath string) map[string][]byte {
	t.Helper()

	file, err := os.Open(tarPath)
	if err != nil {
		t.Fatalf("open tar %s: %v", tarPath, err)
	}
	defer func() { _ = file.Close() }()

	tr := tar.NewReader(file)
	entries := make(map[string][]byte)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar header from %s: %v", tarPath, err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read tar entry %s: %v", hdr.Name, err)
		}
		entries[hdr.Name] = data
	}
	return entries
}

func checksumLineForArchiveHex(filename, checksumHex string) string {
	return fmt.Sprintf("%s  %s\n", checksumHex, filename)
}

func runDecryptWorkflowTUIForTest(t *testing.T, ctx context.Context, cfg *config.Config, configPath string) <-chan error {
	t.Helper()
	logger := logging.New(types.LogLevelError, false)
	logger.SetOutput(io.Discard)

	errCh := make(chan error, 1)
	go func() {
		errCh <- RunDecryptWorkflowTUI(ctx, cfg, logger, "1.0.0", configPath, "test-build")
	}()
	return errCh
}

func waitDecryptResult(t *testing.T, errCh <-chan error) error {
	t.Helper()
	select {
	case err := <-errCh:
		return err
	case <-time.After(3 * time.Minute):
		t.Fatal("decrypt workflow did not finish")
		return nil
	}
}

func TestRunDecryptWorkflowTUICharm_SuccessLocalEncrypted(t *testing.T) {
	decryptCharmE2EMu.Lock()
	defer decryptCharmE2EMu.Unlock()
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	fixture := createDecryptTUIEncryptedFixture(t)
	driver := installCharmUIDriver(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	errCh := runDecryptWorkflowTUIForTest(t, ctx, fixture.Config, fixture.ConfigPath)

	driver.waitScreen("Select backup source")
	driver.keys("enter")
	driver.waitScreen("Select backup")
	driver.waitOutput("node1")
	driver.keys("enter")
	driver.waitScreen("Decrypt key")
	driver.typeText(fixture.Secret)
	driver.keys("enter")
	driver.waitScreen("Destination directory")
	driver.keys("enter")
	driver.waitScreen("Decrypt complete")
	// The full path may hard-wrap inside the notice box; the on-disk
	// assertions below cover the exact path.
	driver.waitOutput("Decrypted bundle created")
	driver.keys("enter")

	if err := waitDecryptResult(t, errCh); err != nil {
		t.Fatalf("RunDecryptWorkflowTUI error: %v", err)
	}

	if _, err := os.Stat(fixture.ExpectedBundlePath); err != nil {
		t.Fatalf("expected decrypted bundle at %s: %v", fixture.ExpectedBundlePath, err)
	}

	entries := readTarEntries(t, fixture.ExpectedBundlePath)

	archiveData, ok := entries[fixture.ExpectedArchiveName]
	if !ok {
		t.Fatalf("bundle missing archive entry %s", fixture.ExpectedArchiveName)
	}
	if string(archiveData) != string(fixture.ArchivePlaintext) {
		t.Fatalf("archive entry content mismatch: got %q want %q", string(archiveData), string(fixture.ArchivePlaintext))
	}

	metadataName := fixture.ExpectedArchiveName + ".metadata"
	metadataData, ok := entries[metadataName]
	if !ok {
		t.Fatalf("bundle missing metadata entry %s", metadataName)
	}

	var manifest backup.Manifest
	if err := json.Unmarshal(metadataData, &manifest); err != nil {
		t.Fatalf("unmarshal metadata entry %s: %v", metadataName, err)
	}
	if manifest.EncryptionMode != "none" {
		t.Fatalf("metadata EncryptionMode=%q; want %q", manifest.EncryptionMode, "none")
	}
	expectedArchivePath := filepath.Join(fixture.DestinationDir, fixture.ExpectedArchiveName)
	if manifest.ArchivePath != expectedArchivePath {
		t.Fatalf("metadata ArchivePath=%q; want %q", manifest.ArchivePath, expectedArchivePath)
	}
	if manifest.SHA256 != fixture.ExpectedChecksum {
		t.Fatalf("metadata SHA256=%q; want %q", manifest.SHA256, fixture.ExpectedChecksum)
	}

	checksumName := fixture.ExpectedArchiveName + ".sha256"
	checksumData, ok := entries[checksumName]
	if !ok {
		t.Fatalf("bundle missing checksum entry %s", checksumName)
	}
	expectedChecksumLine := checksumLineForArchiveHex(fixture.ExpectedArchiveName, fixture.ExpectedChecksum)
	if string(checksumData) != expectedChecksumLine {
		t.Fatalf("checksum entry=%q; want %q", string(checksumData), expectedChecksumLine)
	}
}

func TestRunDecryptWorkflowTUICharm_AbortAtSecretPrompt(t *testing.T) {
	decryptCharmE2EMu.Lock()
	defer decryptCharmE2EMu.Unlock()
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	fixture := createDecryptTUIEncryptedFixture(t)
	driver := installCharmUIDriver(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	errCh := runDecryptWorkflowTUIForTest(t, ctx, fixture.Config, fixture.ConfigPath)

	driver.waitScreen("Select backup source")
	driver.keys("enter")
	driver.waitScreen("Select backup")
	driver.keys("enter")
	driver.waitScreen("Decrypt key")
	driver.typeText("0")
	driver.keys("enter")

	err := waitDecryptResult(t, errCh)
	if !errors.Is(err, ErrDecryptAborted) {
		t.Fatalf("expected ErrDecryptAborted, got %v", err)
	}
	if _, statErr := os.Stat(fixture.ExpectedBundlePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("no bundle must be produced on abort, stat err=%v", statErr)
	}
}

func TestRunDecryptWorkflowTUICharm_WrongSecretThenRetry(t *testing.T) {
	decryptCharmE2EMu.Lock()
	defer decryptCharmE2EMu.Unlock()
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	fixture := createDecryptTUIEncryptedFixture(t)
	driver := installCharmUIDriver(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	errCh := runDecryptWorkflowTUIForTest(t, ctx, fixture.Config, fixture.ConfigPath)

	driver.waitScreen("Select backup source")
	driver.keys("enter")
	driver.waitScreen("Select backup")
	driver.keys("enter")
	driver.waitScreen("Decrypt key")
	driver.typeText("wrong-secret")
	driver.keys("enter")
	// The retry prompt must surface the failure from the previous attempt.
	driver.waitScreen("Decrypt key")
	driver.waitOutput("does not match")
	driver.typeText(fixture.Secret)
	driver.keys("enter")
	driver.waitScreen("Destination directory")
	driver.keys("enter")
	driver.waitScreen("Decrypt complete")
	driver.keys("enter")

	if err := waitDecryptResult(t, errCh); err != nil {
		t.Fatalf("RunDecryptWorkflowTUI error after retry: %v", err)
	}
	if _, err := os.Stat(fixture.ExpectedBundlePath); err != nil {
		t.Fatalf("expected decrypted bundle after retry at %s: %v", fixture.ExpectedBundlePath, err)
	}
}

func TestRunDecryptWorkflowTUICharm_EscOnSourceAborts(t *testing.T) {
	decryptCharmE2EMu.Lock()
	defer decryptCharmE2EMu.Unlock()
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	fixture := createDecryptTUIEncryptedFixture(t)
	driver := installCharmUIDriver(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	errCh := runDecryptWorkflowTUIForTest(t, ctx, fixture.Config, fixture.ConfigPath)

	driver.waitScreen("Select backup source")
	driver.keys("esc")

	err := waitDecryptResult(t, errCh)
	if !errors.Is(err, ErrDecryptAborted) {
		t.Fatalf("expected ErrDecryptAborted on esc, got %v", err)
	}
}
