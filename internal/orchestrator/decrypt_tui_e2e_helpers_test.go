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
	"strings"
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

type notifyingSimulationScreen struct {
	tcell.SimulationScreen
	notify func()
}

func (s *notifyingSimulationScreen) Show() {
	s.SimulationScreen.Show()
	if s.notify != nil {
		s.notify()
	}
}

func (s *notifyingSimulationScreen) Sync() {
	s.SimulationScreen.Sync()
	if s.notify != nil {
		s.notify()
	}
}

type timedSimKey struct {
	Key         tcell.Key
	R           rune
	Mod         tcell.ModMask
	Wait        time.Duration
	WaitForText string
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

func withTimedSimAppSequence(t *testing.T, keys []timedSimKey) {
	t.Helper()

	decryptTUIE2EMu.Lock()
	orig := newTUIApp
	done := make(chan struct{})
	var injectWG sync.WaitGroup
	t.Cleanup(func() {
		close(done)
		injectWG.Wait()
		newTUIApp = orig
		decryptTUIE2EMu.Unlock()
	})

	baseScreen := tcell.NewSimulationScreen("UTF-8")
	if err := baseScreen.Init(); err != nil {
		t.Fatalf("screen.Init: %v", err)
	}
	baseScreen.SetSize(120, 40)

	type timedSimScreenState struct {
		signature string
		text      string
	}

	screenStateCh := make(chan struct{}, 1)
	var appMu sync.RWMutex
	var currentApp *tui.App
	screen := &notifyingSimulationScreen{
		SimulationScreen: baseScreen,
		notify: func() {
			select {
			case screenStateCh <- struct{}{}:
			default:
			}
		},
	}

	var once sync.Once
	newTUIApp = func() *tui.App {
		app := tui.NewApp()
		appMu.Lock()
		currentApp = app
		appMu.Unlock()
		app.SetScreen(screen)

		once.Do(func() {
			injectWG.Add(1)
			go func() {
				defer injectWG.Done()
				var lastInjectedState string

				currentScreenState := func() timedSimScreenState {
					appMu.RLock()
					app := currentApp
					appMu.RUnlock()

					var focus any
					if app != nil {
						focus = app.GetFocus()
					}

					return timedSimScreenState{
						signature: timedSimScreenStateSignature(screen, focus),
						text:      timedSimScreenText(screen),
					}
				}

				waitForScreenText := func(expected string) bool {
					expected = strings.TrimSpace(expected)
					for {
						current := currentScreenState()
						if current.signature != "" {
							if (expected == "" || strings.Contains(current.text, expected)) &&
								(lastInjectedState == "" || current.signature != lastInjectedState) {
								return true
							}
						}

						select {
						case <-done:
							return false
						case <-screenStateCh:
						}
					}
				}

				for _, k := range keys {
					if k.Wait > 0 {
						if !waitForScreenText(k.WaitForText) {
							return
						}
					}
					current := currentScreenState()
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
					lastInjectedState = current.signature
				}
			}()
		})

		return app
	}
}

func timedSimScreenStateSignature(screen tcell.SimulationScreen, focus any) string {
	cells, width, height := screen.GetContents()
	cursorX, cursorY, cursorVisible := screen.GetCursor()

	sum := sha256.New()
	fmt.Fprintf(sum, "size:%d:%d cursor:%d:%d:%t focus:%T:%p\n", width, height, cursorX, cursorY, cursorVisible, focus, focus)
	for _, cell := range cells {
		fg, bg, attr := cell.Style.Decompose()
		fmt.Fprintf(sum, "%x/%d/%d/%d;", cell.Bytes, fg, bg, attr)
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func timedSimScreenText(screen tcell.SimulationScreen) string {
	cells, width, height := screen.GetContents()
	if width <= 0 || height <= 0 || len(cells) < width*height {
		return ""
	}

	var b strings.Builder
	for y := 0; y < height; y++ {
		row := make([]byte, 0, width)
		for x := 0; x < width; x++ {
			cell := cells[y*width+x]
			if len(cell.Bytes) == 0 {
				row = append(row, ' ')
				continue
			}
			row = append(row, cell.Bytes...)
		}
		b.WriteString(strings.TrimRight(string(row), " "))
		b.WriteByte('\n')
	}
	return b.String()
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
		{Key: tcell.KeyEnter, Wait: 1 * time.Second, WaitForText: "Select backup source"},
		{Key: tcell.KeyEnter, Wait: 750 * time.Millisecond, WaitForText: "Select backup"},
	}

	for _, r := range secret {
		keys = append(keys, timedSimKey{
			Key:         tcell.KeyRune,
			R:           r,
			Wait:        35 * time.Millisecond,
			WaitForText: "Decrypt key",
		})
	}

	keys = append(keys,
		timedSimKey{Key: tcell.KeyTab, Wait: 150 * time.Millisecond, WaitForText: "Decrypt key"},
		timedSimKey{Key: tcell.KeyEnter, Wait: 100 * time.Millisecond, WaitForText: "Decrypt key"},
		timedSimKey{Key: tcell.KeyTab, Wait: 500 * time.Millisecond, WaitForText: "Destination directory"},
		timedSimKey{Key: tcell.KeyEnter, Wait: 100 * time.Millisecond, WaitForText: "Destination directory"},
	)

	return keys
}

func abortDecryptTUISequence() []timedSimKey {
	return []timedSimKey{
		{Key: tcell.KeyEnter, Wait: 1 * time.Second, WaitForText: "Select backup source"},
		{Key: tcell.KeyEnter, Wait: 750 * time.Millisecond, WaitForText: "Select backup"},
		{Key: tcell.KeyRune, R: '0', Wait: 500 * time.Millisecond, WaitForText: "Decrypt key"},
		{Key: tcell.KeyTab, Wait: 150 * time.Millisecond, WaitForText: "Decrypt key"},
		{Key: tcell.KeyEnter, Wait: 100 * time.Millisecond, WaitForText: "Decrypt key"},
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

	waitTimeout := 30 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		waitTimeout = time.Until(deadline) + 2*time.Second
		if waitTimeout < 2*time.Second {
			waitTimeout = 2 * time.Second
		}
	}
	timer := time.NewTimer(waitTimeout)
	defer timer.Stop()

	select {
	case err := <-errCh:
		return err
	case <-timer.C:
		if err := ctx.Err(); err != nil {
			t.Fatalf("RunDecryptWorkflowTUI did not return within %s (context state: %v)", waitTimeout, err)
			return nil
		}
		t.Fatalf("RunDecryptWorkflowTUI did not return within %s", waitTimeout)
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
