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

const (
	timedSimScreenWaitTimeout = 10 * time.Second
	timedSimCompletionTimeout = 15 * time.Second
	timedSimDefaultSettle     = 15 * time.Millisecond
	timedSimKeyDelay          = 15 * time.Millisecond
)

type notifyingSimulationScreen struct {
	tcell.SimulationScreen
	mu       sync.Mutex
	snapshot timedSimScreenSnapshot
	notify   func()
}

type timedSimScreenSnapshot struct {
	cells         []tcell.SimCell
	width         int
	height        int
	cursorX       int
	cursorY       int
	cursorVisible bool
	ready         bool
}

func (s *notifyingSimulationScreen) Show() {
	s.mu.Lock()
	s.SimulationScreen.Show()
	s.captureLocked()
	s.mu.Unlock()
	s.notifyChange()
}

func (s *notifyingSimulationScreen) Sync() {
	s.mu.Lock()
	s.SimulationScreen.Sync()
	s.captureLocked()
	s.mu.Unlock()
	s.notifyChange()
}

func (s *notifyingSimulationScreen) snapshotState() timedSimScreenSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneTimedSimScreenSnapshot(s.snapshot)
}

func (s *notifyingSimulationScreen) captureLocked() {
	cells, width, height := s.SimulationScreen.GetContents()
	cursorX, cursorY, cursorVisible := s.SimulationScreen.GetCursor()
	s.snapshot = timedSimScreenSnapshot{
		cells:         cloneSimCells(cells),
		width:         width,
		height:        height,
		cursorX:       cursorX,
		cursorY:       cursorY,
		cursorVisible: cursorVisible,
		ready:         true,
	}
}

func (s *notifyingSimulationScreen) notifyChange() {
	if s.notify != nil {
		s.notify()
	}
}

func cloneTimedSimScreenSnapshot(snapshot timedSimScreenSnapshot) timedSimScreenSnapshot {
	snapshot.cells = cloneSimCells(snapshot.cells)
	return snapshot
}

func cloneSimCells(cells []tcell.SimCell) []tcell.SimCell {
	if len(cells) == 0 {
		return nil
	}
	cloned := make([]tcell.SimCell, len(cells))
	for i, cell := range cells {
		cloned[i] = cell
		if cell.Bytes != nil {
			cloned[i].Bytes = append([]byte(nil), cell.Bytes...)
		}
		if cell.Runes != nil {
			cloned[i].Runes = append([]rune(nil), cell.Runes...)
		}
	}
	return cloned
}

type timedSimKey struct {
	Key              tcell.Key
	R                rune
	Mod              tcell.ModMask
	WaitForText      string
	Wait             time.Duration
	RequireNewApp    bool
	SettleAfterMatch time.Duration
}

type timedSimHarness struct {
	t             *testing.T
	done          chan struct{}
	closeDoneOnce sync.Once
	injectWG      sync.WaitGroup
	screenStateCh chan struct{}
	runCompleted  chan struct{}
	closeRunOnce  sync.Once

	appMu   sync.RWMutex
	apps    []*tui.App
	current *timedSimAppState
}

type timedSimAppState struct {
	generation int
	app        *tui.App
	screen     *notifyingSimulationScreen
}

type timedSimScreenState struct {
	generation int
	text       string
	focusType  string
	ready      bool
	screen     *notifyingSimulationScreen
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

func withTimedSimAppSequence(t *testing.T, keys []timedSimKey) *timedSimHarness {
	t.Helper()

	decryptTUIE2EMu.Lock()
	orig := newTUIApp
	h := &timedSimHarness{
		t:             t,
		done:          make(chan struct{}),
		screenStateCh: make(chan struct{}, 1),
		runCompleted:  make(chan struct{}),
	}

	t.Cleanup(func() {
		h.stop()
		newTUIApp = orig
		decryptTUIE2EMu.Unlock()
	})

	newTUIApp = func() *tui.App {
		app := tui.NewApp()

		baseScreen := tcell.NewSimulationScreen("UTF-8")
		if err := baseScreen.Init(); err != nil {
			t.Fatalf("screen.Init: %v", err)
		}
		baseScreen.SetSize(120, 40)

		screen := &notifyingSimulationScreen{
			SimulationScreen: baseScreen,
			notify:           h.notifyScreenStateChanged,
		}

		h.appMu.Lock()
		state := &timedSimAppState{
			generation: len(h.apps) + 1,
			app:        app,
			screen:     screen,
		}
		h.apps = append(h.apps, app)
		h.current = state
		h.appMu.Unlock()

		app.SetScreen(screen)
		h.notifyScreenStateChanged()
		return app
	}

	h.injectWG.Add(1)
	go h.run(keys)

	return h
}

func (h *timedSimHarness) notifyScreenStateChanged() {
	select {
	case h.screenStateCh <- struct{}{}:
	default:
	}
}

func (h *timedSimHarness) markRunCompleted() {
	if h == nil {
		return
	}
	if h.runCompleted == nil {
		return
	}
	h.closeRunOnce.Do(func() {
		close(h.runCompleted)
	})
}

func (h *timedSimHarness) stop() {
	if h == nil {
		return
	}
	h.closeDoneOnce.Do(func() {
		close(h.done)
	})
	h.StopAll()
	h.injectWG.Wait()
}

func (h *timedSimHarness) StopAll() {
	if h == nil {
		return
	}
	h.appMu.RLock()
	apps := append([]*tui.App(nil), h.apps...)
	h.appMu.RUnlock()
	for i := len(apps) - 1; i >= 0; i-- {
		apps[i].Stop()
	}
}

func (h *timedSimHarness) run(keys []timedSimKey) {
	defer h.injectWG.Done()

	generation := 0
	for idx, key := range keys {
		minGeneration := generation
		if minGeneration == 0 || key.RequireNewApp {
			minGeneration++
		}

		state, ok := h.waitForScreenText(idx, key, minGeneration)
		if !ok {
			return
		}
		generation = state.generation
		if key.Wait > 0 && strings.TrimSpace(key.WaitForText) == "" {
			if !h.sleepOrDone(key.Wait) {
				return
			}
		}

		settle := key.SettleAfterMatch
		if settle <= 0 {
			settle = timedSimDefaultSettle
		}
		if !h.sleepOrDone(settle) {
			return
		}

		mod := key.Mod
		if mod == 0 {
			mod = tcell.ModNone
		}
		state.screen.InjectKey(key.Key, key.R, mod)
		if !h.sleepOrDone(timedSimKeyDelay) {
			return
		}
	}

	timer := time.NewTimer(timedSimCompletionTimeout)
	defer timer.Stop()
	select {
	case <-h.runCompleted:
	case <-h.done:
	case <-timer.C:
		h.t.Errorf("TUI simulation did not finish within %s after injecting %d key(s)\n%s", timedSimCompletionTimeout, len(keys), h.describeCurrentState())
		h.StopAll()
	}
}

func (h *timedSimHarness) waitForScreenText(index int, key timedSimKey, minGeneration int) (timedSimScreenState, bool) {
	expected := strings.TrimSpace(key.WaitForText)
	timeout := timedSimScreenWaitTimeout
	if key.Wait > 0 {
		timeout = key.Wait
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		state := h.currentScreenState()
		if state.ready && state.generation >= minGeneration && (expected == "" || strings.Contains(state.text, expected)) {
			return state, true
		}

		select {
		case <-h.done:
			return timedSimScreenState{}, false
		case <-h.screenStateCh:
		case <-timer.C:
			h.t.Errorf(
				"TUI simulation timed out at action %d waiting for text %q within %s (min generation=%d, current generation=%d, focus=%s)\nCurrent screen:\n%s",
				index,
				expected,
				timeout,
				minGeneration,
				state.generation,
				state.focusType,
				state.text,
			)
			h.StopAll()
			return state, false
		}
	}
}

func (h *timedSimHarness) currentScreenState() timedSimScreenState {
	h.appMu.RLock()
	current := h.current
	h.appMu.RUnlock()
	if current == nil || current.screen == nil {
		return timedSimScreenState{}
	}

	focusType := "<nil>"
	if current.app != nil {
		if focus := current.app.GetFocus(); focus != nil {
			focusType = fmt.Sprintf("%T", focus)
		}
	}
	snapshot := current.screen.snapshotState()
	return timedSimScreenState{
		generation: current.generation,
		text:       timedSimScreenText(snapshot),
		focusType:  focusType,
		ready:      snapshot.ready,
		screen:     current.screen,
	}
}

func (h *timedSimHarness) describeCurrentState() string {
	state := h.currentScreenState()
	return fmt.Sprintf("current generation=%d focus=%s ready=%t\nCurrent screen:\n%s", state.generation, state.focusType, state.ready, state.text)
}

func (h *timedSimHarness) sleepOrDone(d time.Duration) bool {
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-h.done:
		return false
	case <-timer.C:
		return true
	}
}

func timedSimScreenText(snapshot timedSimScreenSnapshot) string {
	if !snapshot.ready || snapshot.width <= 0 || snapshot.height <= 0 || len(snapshot.cells) < snapshot.width*snapshot.height {
		return ""
	}

	var b strings.Builder
	for y := 0; y < snapshot.height; y++ {
		row := make([]byte, 0, snapshot.width)
		for x := 0; x < snapshot.width; x++ {
			cell := snapshot.cells[y*snapshot.width+x]
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
		{Key: tcell.KeyEnter, WaitForText: "Select backup source", RequireNewApp: true},
		{Key: tcell.KeyEnter, WaitForText: "Select backup", RequireNewApp: true},
	}

	for idx, r := range secret {
		keys = append(keys, timedSimKey{
			Key:              tcell.KeyRune,
			R:                r,
			WaitForText:      "Decrypt key",
			RequireNewApp:    idx == 0,
			SettleAfterMatch: 5 * time.Millisecond,
		})
	}

	keys = append(keys,
		timedSimKey{Key: tcell.KeyTab, WaitForText: "Decrypt key"},
		timedSimKey{Key: tcell.KeyEnter, WaitForText: "Decrypt key"},
		timedSimKey{Key: tcell.KeyTab, WaitForText: "Destination directory", RequireNewApp: true},
		timedSimKey{Key: tcell.KeyEnter, WaitForText: "Destination directory"},
	)

	return keys
}

func abortDecryptTUISequence() []timedSimKey {
	return []timedSimKey{
		{Key: tcell.KeyEnter, WaitForText: "Select backup source", RequireNewApp: true},
		{Key: tcell.KeyEnter, WaitForText: "Select backup", RequireNewApp: true},
		{Key: tcell.KeyRune, R: '0', WaitForText: "Decrypt key", RequireNewApp: true},
		{Key: tcell.KeyTab, WaitForText: "Decrypt key"},
		{Key: tcell.KeyEnter, WaitForText: "Decrypt key"},
	}
}

func runDecryptWorkflowTUIForTest(t *testing.T, sim *timedSimHarness, ctx context.Context, cfg *config.Config, configPath string) error {
	t.Helper()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	logger := logging.New(types.LogLevelError, false)
	logger.SetOutput(io.Discard)

	errCh := make(chan error, 1)
	go func() {
		err := RunDecryptWorkflowTUI(runCtx, cfg, logger, "1.0.0", configPath, "test-build")
		if sim != nil {
			sim.markRunCompleted()
		}
		errCh <- err
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
		cancel()
		if sim != nil {
			sim.StopAll()
		}

		shutdownTimer := time.NewTimer(2 * time.Second)
		defer shutdownTimer.Stop()
		select {
		case err := <-errCh:
			return err
		case <-shutdownTimer.C:
		}

		if err := runCtx.Err(); err != nil {
			if sim != nil {
				t.Fatalf("RunDecryptWorkflowTUI did not return within %s (context state: %v)\n%s", waitTimeout, err, sim.describeCurrentState())
			}
			t.Fatalf("RunDecryptWorkflowTUI did not return within %s (context state: %v)", waitTimeout, err)
			return nil
		}
		if sim != nil {
			t.Fatalf("RunDecryptWorkflowTUI did not return within %s\n%s", waitTimeout, sim.describeCurrentState())
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
