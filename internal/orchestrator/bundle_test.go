package orchestrator

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
)

type trackingBundleFS struct {
	FS
	createdFiles []*os.File
	createdPaths []string
	openErr      map[string]error
	renameErr    error
}

func (f *trackingBundleFS) recordCreatedFile(file *os.File) {
	if file == nil {
		return
	}
	f.createdFiles = append(f.createdFiles, file)
	f.createdPaths = append(f.createdPaths, filepath.Clean(file.Name()))
}

func (f *trackingBundleFS) Create(name string) (*os.File, error) {
	file, err := f.FS.Create(name)
	if err == nil {
		f.recordCreatedFile(file)
	}
	return file, err
}

func (f *trackingBundleFS) CreateTemp(dir, pattern string) (*os.File, error) {
	file, err := f.FS.CreateTemp(dir, pattern)
	if err == nil {
		f.recordCreatedFile(file)
	}
	return file, err
}

func (f *trackingBundleFS) Open(path string) (*os.File, error) {
	if err, ok := f.openErr[filepath.Clean(path)]; ok {
		return nil, err
	}
	return f.FS.Open(path)
}

func (f *trackingBundleFS) Rename(oldpath, newpath string) error {
	if f.renameErr != nil {
		return f.renameErr
	}
	return f.FS.Rename(oldpath, newpath)
}

func assertPathAbsent(t *testing.T, path string) {
	t.Helper()

	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected %s to be absent, got %v", path, err)
	}
}

func assertTrackedFilesClosed(t *testing.T, files []*os.File) {
	t.Helper()

	if len(files) == 0 {
		t.Fatalf("expected tracked bundle file")
	}
	for _, file := range files {
		if err := file.Close(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("bundle file %s close after createBundle = %v, want ErrClosed", file.Name(), err)
		}
	}
}

func assertTrackedPathsAbsent(t *testing.T, paths []string) {
	t.Helper()

	if len(paths) == 0 {
		t.Fatalf("expected tracked bundle file path")
	}
	for _, path := range paths {
		assertPathAbsent(t, path)
	}
}

func TestCreateBundle_CreatesValidTarArchive(t *testing.T) {
	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	tempDir := t.TempDir()
	archive := filepath.Join(tempDir, "backup.tar")

	// Create test files with specific content
	testData := map[string]string{
		"":                 "archive-content",
		".sha256":          "checksum1",
		".metadata":        "metadata-json",
		".metadata.sha256": "checksum2",
	}

	for suffix, content := range testData {
		if err := os.WriteFile(archive+suffix, []byte(content), 0o640); err != nil {
			t.Fatalf("write %s: %v", suffix, err)
		}
	}

	bundleFS := &trackingBundleFS{FS: osFS{}}
	o := &Orchestrator{
		logger: logger,
		fs:     bundleFS,
	}

	bundlePath, err := o.createBundle(context.Background(), archive)
	if err != nil {
		t.Fatalf("createBundle: %v", err)
	}

	expectedPath := archive + ".bundle.tar"
	if bundlePath != expectedPath {
		t.Fatalf("bundle path = %s, want %s", bundlePath, expectedPath)
	}
	if len(bundleFS.createdPaths) == 0 {
		t.Fatalf("expected tracked bundle file path")
	}
	for _, path := range bundleFS.createdPaths {
		if path == expectedPath {
			t.Fatalf("expected bundle to be written via temp file, got %s", path)
		}
	}
	assertTrackedPathsAbsent(t, bundleFS.createdPaths)
	assertTrackedFilesClosed(t, bundleFS.createdFiles)

	// Verify bundle file exists
	bundleInfo, err := os.Stat(bundlePath)
	if err != nil {
		t.Fatalf("expected bundle file, got %v", err)
	}
	if bundleInfo.Size() == 0 {
		t.Fatalf("bundle file is empty")
	}

	// Verify tar contents
	bundleFile, err := os.Open(bundlePath)
	if err != nil {
		t.Fatalf("open bundle: %v", err)
	}
	defer bundleFile.Close()

	tr := tar.NewReader(bundleFile)
	foundFiles := make(map[string]bool)
	order := make([]string, 0, len(testData))
	expectedFiles := []string{
		"backup.tar",
		"backup.tar.sha256",
		"backup.tar.metadata",
		"backup.tar.metadata.sha256",
	}

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar header: %v", err)
		}

		foundFiles[header.Name] = true
		order = append(order, header.Name)

		// Verify file content matches
		content, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read file %s from tar: %v", header.Name, err)
		}

		expectedContent := testData[""]
		switch header.Name {
		case "backup.tar.sha256":
			expectedContent = testData[".sha256"]
		case "backup.tar.metadata":
			expectedContent = testData[".metadata"]
		case "backup.tar.metadata.sha256":
			expectedContent = testData[".metadata.sha256"]
		}

		if string(content) != expectedContent {
			t.Errorf("file %s content = %q, want %q", header.Name, content, expectedContent)
		}
	}

	if len(order) == 0 || order[0] != "backup.tar.metadata" {
		t.Fatalf("first bundle entry=%q; want %q", func() string {
			if len(order) == 0 {
				return ""
			}
			return order[0]
		}(), "backup.tar.metadata")
	}

	// Verify all expected files are present
	for _, expected := range expectedFiles {
		if !foundFiles[expected] {
			t.Errorf("expected file %s not found in tar", expected)
		}
	}
}

func TestCreateBundle_ClosesBundleFileOnInputOpenError(t *testing.T) {
	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	tempDir := t.TempDir()
	archive := filepath.Join(tempDir, "backup.tar")

	testData := map[string]string{
		"":          "archive-content",
		".sha256":   "checksum1",
		".metadata": "metadata-json",
	}
	for suffix, content := range testData {
		if err := os.WriteFile(archive+suffix, []byte(content), 0o640); err != nil {
			t.Fatalf("write %s: %v", suffix, err)
		}
	}

	forcedErr := errors.New("forced open failure")
	bundleFS := &trackingBundleFS{
		FS: osFS{},
		openErr: map[string]error{
			filepath.Clean(archive + ".sha256"): forcedErr,
		},
	}
	o := &Orchestrator{
		logger: logger,
		fs:     bundleFS,
	}

	_, err := o.createBundle(context.Background(), archive)
	if !errors.Is(err, forcedErr) {
		t.Fatalf("createBundle error = %v, want wrapped %v", err, forcedErr)
	}
	assertTrackedFilesClosed(t, bundleFS.createdFiles)
	assertPathAbsent(t, archive+".bundle.tar")
	assertTrackedPathsAbsent(t, bundleFS.createdPaths)
}

func TestCreateBundle_RemovesTempFileOnRenameError(t *testing.T) {
	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	tempDir := t.TempDir()
	archive := filepath.Join(tempDir, "backup.tar")

	testData := map[string]string{
		"":          "archive-content",
		".sha256":   "checksum1",
		".metadata": "metadata-json",
	}
	for suffix, content := range testData {
		if err := os.WriteFile(archive+suffix, []byte(content), 0o640); err != nil {
			t.Fatalf("write %s: %v", suffix, err)
		}
	}

	forcedErr := errors.New("forced rename failure")
	bundleFS := &trackingBundleFS{
		FS:        osFS{},
		renameErr: forcedErr,
	}
	o := &Orchestrator{
		logger: logger,
		fs:     bundleFS,
	}

	_, err := o.createBundle(context.Background(), archive)
	if !errors.Is(err, forcedErr) {
		t.Fatalf("createBundle error = %v, want wrapped %v", err, forcedErr)
	}
	assertTrackedFilesClosed(t, bundleFS.createdFiles)
	assertPathAbsent(t, archive+".bundle.tar")
	assertTrackedPathsAbsent(t, bundleFS.createdPaths)
}

func TestCreateBundle_RemovesFinalBundleOnDirectoryOpenErrorDuringSync(t *testing.T) {
	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	tempDir := t.TempDir()
	archive := filepath.Join(tempDir, "backup.tar")

	testData := map[string]string{
		"":          "archive-content",
		".sha256":   "checksum1",
		".metadata": "metadata-json",
	}
	for suffix, content := range testData {
		if err := os.WriteFile(archive+suffix, []byte(content), 0o640); err != nil {
			t.Fatalf("write %s: %v", suffix, err)
		}
	}

	forcedErr := errors.New("forced directory open failure during sync")
	bundleFS := &trackingBundleFS{
		FS: osFS{},
		openErr: map[string]error{
			filepath.Clean(tempDir): forcedErr,
		},
	}
	o := &Orchestrator{
		logger: logger,
		fs:     bundleFS,
	}

	_, err := o.createBundle(context.Background(), archive)
	if !errors.Is(err, forcedErr) {
		t.Fatalf("createBundle error = %v, want wrapped %v", err, forcedErr)
	}
	assertTrackedFilesClosed(t, bundleFS.createdFiles)
	assertPathAbsent(t, archive+".bundle.tar")
	assertTrackedPathsAbsent(t, bundleFS.createdPaths)
}

func TestRemoveAssociatedFiles_RemovesAll(t *testing.T) {
	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	tempDir := t.TempDir()
	archive := filepath.Join(tempDir, "backup.tar")
	files := []string{
		archive,
		archive + ".sha256",
		archive + ".metadata",
		archive + ".metadata.sha256",
		archive + ".manifest.json",
	}
	for _, f := range files {
		if err := os.WriteFile(f, []byte("x"), 0o640); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}

	o := &Orchestrator{
		logger: logger,
		fs:     osFS{},
	}
	if err := o.removeAssociatedFiles(archive); err != nil {
		t.Fatalf("removeAssociatedFiles: %v", err)
	}
	for _, f := range files {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, got %v", f, err)
		}
	}
}
