package orchestrator

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/gdamore/tcell/v2"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/tui"
	"github.com/tis24dev/proxsave/internal/types"
)

var decryptTUIE2EMu sync.Mutex

type timedSimKey struct {
	Key  tcell.Key
	R    rune
	Mod  tcell.ModMask
	Wait time.Duration
}

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

func lockDecryptTUIE2E(t *testing.T) {
	t.Helper()

	decryptTUIE2EMu.Lock()
	t.Cleanup(decryptTUIE2EMu.Unlock)
}

func withTimedSimAppSequence(t *testing.T, keys []timedSimKey) {
	t.Helper()

	orig := newTUIApp
	done := make(chan struct{})
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen.Init: %v", err)
	}
	screen.SetSize(120, 40)

	var once sync.Once
	var injectWG sync.WaitGroup
	newTUIApp = func() *tui.App {
		app := tui.NewApp()
		app.SetScreen(screen)

		once.Do(func() {
			injectWG.Add(1)
			go func() {
				defer injectWG.Done()

				for _, k := range keys {
					if k.Wait > 0 {
						timer := time.NewTimer(k.Wait)
						select {
						case <-done:
							if !timer.Stop() {
								<-timer.C
							}
							return
						case <-timer.C:
						}
					}
					mod := k.Mod
					if mod == 0 {
						mod = tcell.ModNone
					}
					select {
					case <-done:
						return
					default:
					}
					screen.InjectKey(k.Key, k.R, mod)
				}
			}()
		})

		return app
	}

	t.Cleanup(func() {
		close(done)
		injectWG.Wait()
		newTUIApp = orig
	})
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

func successDecryptTUISequence(secret string) []timedSimKey {
	keys := []timedSimKey{
		{Key: tcell.KeyEnter, Wait: 1 * time.Second},
		{Key: tcell.KeyEnter, Wait: 750 * time.Millisecond},
	}

	for _, r := range secret {
		keys = append(keys, timedSimKey{
			Key:  tcell.KeyRune,
			R:    r,
			Wait: 35 * time.Millisecond,
		})
	}

	keys = append(keys,
		timedSimKey{Key: tcell.KeyTab, Wait: 150 * time.Millisecond},
		timedSimKey{Key: tcell.KeyEnter, Wait: 100 * time.Millisecond},
		timedSimKey{Key: tcell.KeyTab, Wait: 500 * time.Millisecond},
		timedSimKey{Key: tcell.KeyEnter, Wait: 100 * time.Millisecond},
	)

	return keys
}

func abortDecryptTUISequence() []timedSimKey {
	return []timedSimKey{
		{Key: tcell.KeyEnter, Wait: 250 * time.Millisecond},
		{Key: tcell.KeyEnter, Wait: 500 * time.Millisecond},
		{Key: tcell.KeyRune, R: '0', Wait: 500 * time.Millisecond},
		{Key: tcell.KeyTab, Wait: 150 * time.Millisecond},
		{Key: tcell.KeyEnter, Wait: 100 * time.Millisecond},
	}
}

func runDecryptWorkflowTUIForTest(t *testing.T, ctx context.Context, cfg *config.Config, configPath string) error {
	t.Helper()

	logger := logging.New(types.LogLevelError, false)
	logger.SetOutput(io.Discard)

	errCh := make(chan error, 1)
	go func() {
		errCh <- RunDecryptWorkflowTUI(ctx, cfg, logger, "1.0.0", configPath, "test-build")
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		t.Fatalf("RunDecryptWorkflowTUI context expired: %v", ctx.Err())
		return nil
	case <-time.After(20 * time.Second):
		t.Fatalf("RunDecryptWorkflowTUI did not complete within 20s")
		return nil
	}
}

func readTarEntries(t *testing.T, tarPath string) map[string][]byte {
	t.Helper()

	file, err := os.Open(tarPath)
	if err != nil {
		t.Fatalf("open tar %s: %v", tarPath, err)
	}
	defer file.Close()

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
