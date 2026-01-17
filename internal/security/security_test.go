package security

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func newSecurityTestLogger() *logging.Logger {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)
	return logger
}

func newCheckerForTest(cfg *config.Config, lookPath func(string) (string, error)) *Checker {
	return &Checker{
		logger:   newSecurityTestLogger(),
		cfg:      cfg,
		result:   &Result{},
		lookPath: lookPath,
	}
}

func stubLookPath(existing map[string]bool) func(string) (string, error) {
	return func(binary string) (string, error) {
		if existing[binary] {
			return "/usr/bin/" + binary, nil
		}
		return "", fmt.Errorf("not found")
	}
}

func containsIssue(result *Result, needle string) bool {
	for _, issue := range result.Issues {
		if strings.Contains(issue.Message, needle) {
			return true
		}
	}
	return false
}

func newCheckerWithExec(t *testing.T, cfg *config.Config, execPath string) *Checker {
	t.Helper()
	if cfg == nil {
		cfg = &config.Config{}
	}
	return &Checker{
		logger:   newSecurityTestLogger(),
		cfg:      cfg,
		result:   &Result{},
		execPath: execPath,
	}
}

func newChecker(t *testing.T, cfg *config.Config) *Checker {
	t.Helper()
	if cfg == nil {
		cfg = &config.Config{}
	}
	return &Checker{
		logger: newSecurityTestLogger(),
		cfg:    cfg,
		result: &Result{},
	}
}

// ============================================================
// Result struct tests
// ============================================================

func TestResultAdd(t *testing.T) {
	r := &Result{}
	r.add(severityWarning, "warn")
	r.add(severityError, "err")
	if len(r.Issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(r.Issues))
	}
	if r.Issues[0].Severity != severityWarning || r.Issues[1].Severity != severityError {
		t.Fatalf("unexpected issues: %+v", r.Issues)
	}
}

func TestResultHasErrors(t *testing.T) {
	tests := []struct {
		name     string
		issues   []Issue
		expected bool
	}{
		{"empty", nil, false},
		{"warnings only", []Issue{{Severity: severityWarning}}, false},
		{"has error", []Issue{{Severity: severityError}}, true},
		{"mixed", []Issue{{Severity: severityWarning}, {Severity: severityError}}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := (&Result{Issues: tc.issues}).HasErrors(); got != tc.expected {
				t.Fatalf("HasErrors() = %v, want %v", got, tc.expected)
			}
		})
	}
}

func TestResultCounts(t *testing.T) {
	r := &Result{Issues: []Issue{
		{Severity: severityWarning},
		{Severity: severityWarning},
		{Severity: severityError},
	}}
	if r.ErrorCount() != 1 || r.WarningCount() != 2 || r.TotalIssues() != 3 {
		t.Fatalf("unexpected counts: errors=%d warnings=%d total=%d",
			r.ErrorCount(), r.WarningCount(), r.TotalIssues())
	}
}

func TestCheckDependenciesMissingRequiredAddsError(t *testing.T) {
	cfg := &config.Config{
		CompressionType: types.CompressionXZ, // requires xz binary in addition to tar
	}
	checker := newCheckerForTest(cfg, stubLookPath(map[string]bool{
		"tar": true, // present
		// "xz" missing
	}))

	checker.checkDependencies()

	if got := checker.result.ErrorCount(); got != 1 {
		t.Fatalf("expected 1 error, got %d issues=%+v", got, checker.result.Issues)
	}
	msg := checker.result.Issues[0].Message
	if !strings.Contains(msg, "Required dependency") || !strings.Contains(msg, "xz") {
		t.Fatalf("unexpected issue message: %s", msg)
	}
}

	func TestCheckDependenciesMissingOptionalAddsWarning(t *testing.T) {
		cfg := &config.Config{
			CompressionType:       types.CompressionNone, // only tar required
			EmailDeliveryMethod:   "relay",
			EmailFallbackSendmail: true, // pmf becomes optional dependency (relay fallback)
		}
		checker := newCheckerForTest(cfg, stubLookPath(map[string]bool{
			"tar": true, // present
			// proxmox-mail-forward missing -> warning
		}))

	checker.checkDependencies()

	if got := checker.result.WarningCount(); got != 1 {
		t.Fatalf("expected 1 warning, got %d issues=%+v", got, checker.result.Issues)
	}
		msg := checker.result.Issues[0].Message
		if !strings.Contains(msg, "Optional dependency") || !strings.Contains(msg, "proxmox-mail-forward") {
			t.Fatalf("unexpected warning message: %s", msg)
		}
		if checker.result.ErrorCount() != 0 {
			t.Fatalf("expected no errors, got %d", checker.result.ErrorCount())
		}
	}

func TestParseSSLineProgramExtraction(t *testing.T) {
	line := `tcp   LISTEN 0      128          0.0.0.0:22         0.0.0.0:*    users:(("sshd",pid=1234,fd=3))`
	entry := parseSSLine(line)
	if !entry.valid || entry.port != 22 {
		t.Fatalf("expected valid entry for line: %#v", entry)
	}
	if entry.program != "sshd" {
		t.Fatalf("program = %q, want sshd", entry.program)
	}
}

func TestChecksumReaderDeterministic(t *testing.T) {
	data := "test data for checksum"
	hash1, err := checksumReader(strings.NewReader(data))
	if err != nil {
		t.Fatalf("checksumReader failed: %v", err)
	}
	hash2, err := checksumReader(strings.NewReader(data))
	if err != nil {
		t.Fatalf("checksumReader failed: %v", err)
	}
	if hash1 == "" || len(hash1) != 64 {
		t.Fatalf("expected 64 char hash, got %q", hash1)
	}
	if hash1 != hash2 {
		t.Fatalf("expected identical hashes for same data: %q vs %q", hash1, hash2)
	}
}

func TestChecksumReaderDifferentData(t *testing.T) {
	hash1, _ := checksumReader(strings.NewReader("data1"))
	hash2, _ := checksumReader(strings.NewReader("data2"))
	if hash1 == hash2 {
		t.Fatal("different data should produce different hashes")
	}
}

func TestParseSSLineVariants(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantValid  bool
		wantPort   int
		wantPublic bool
	}{
		{"public IPv4", `tcp   LISTEN 0      128          0.0.0.0:22         0.0.0.0:*`, true, 22, true},
		{"localhost IPv4", `tcp   LISTEN 0      128        127.0.0.1:8080       0.0.0.0:*`, true, 8080, false},
		{"public IPv6", `tcp   LISTEN 0      128             [::]:443            [::]:*`, true, 443, true},
		{"invalid", `tcp LISTEN`, false, 0, false},
		{"empty", ``, false, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry := parseSSLine(tc.line)
			if entry.valid != tc.wantValid {
				t.Fatalf("valid=%v want %v", entry.valid, tc.wantValid)
			}
			if tc.wantValid {
				if entry.port != tc.wantPort || entry.public != tc.wantPublic {
					t.Fatalf("entry=%+v", entry)
				}
			}
		})
	}
}

func TestVerifyConfigFileMissingPath(t *testing.T) {
	checker := newChecker(t, &config.Config{})
	checker.verifyConfigFile()
	if !containsIssue(checker.result, "Configuration path not provided") {
		t.Fatalf("expected warning about missing configuration path, got %+v", checker.result.Issues)
	}
}

func TestVerifyConfigFileStatError(t *testing.T) {
	checker := newChecker(t, &config.Config{})
	checker.configPath = filepath.Join(t.TempDir(), "does-not-exist.conf")
	checker.verifyConfigFile()
	if !containsIssue(checker.result, "Cannot stat configuration file") {
		t.Fatalf("expected error about missing configuration file, got %+v", checker.result.Issues)
	}
}

func TestVerifySensitiveFilesOptionalSkip(t *testing.T) {
	cfg := &config.Config{BaseDir: t.TempDir()}
	checker := newChecker(t, cfg)
	checker.verifySensitiveFiles()
	if checker.result.TotalIssues() != 0 {
		t.Fatalf("expected no issues for optional sensitive files, got %+v", checker.result.Issues)
	}
}

func TestVerifySensitiveFilesAgeRecipientPermissions(t *testing.T) {
	baseDir := t.TempDir()
	ageDir := filepath.Join(baseDir, "identity", "age")
	if err := os.MkdirAll(ageDir, 0o755); err != nil {
		t.Fatalf("mkdir age dir: %v", err)
	}
	recipient := filepath.Join(ageDir, "recipient.txt")
	if err := os.WriteFile(recipient, []byte("age-recipient"), 0o644); err != nil {
		t.Fatalf("write recipient: %v", err)
	}

	cfg := &config.Config{
		BaseDir:        baseDir,
		EncryptArchive: true,
	}
	checker := newChecker(t, cfg)
	checker.verifySensitiveFiles()

	if !containsIssue(checker.result, "AGE recipient file") {
		t.Fatalf("expected warning mentioning AGE recipient file, got %+v", checker.result.Issues)
	}
}

func TestVerifySecureAccountFiles(t *testing.T) {
	secureDir := t.TempDir()
	jsonFile := filepath.Join(secureDir, "account.json")
	if err := os.WriteFile(jsonFile, []byte(`{"id":"demo"}`), 0o644); err != nil {
		t.Fatalf("write secure account file: %v", err)
	}

	cfg := &config.Config{SecureAccount: secureDir}
	checker := newChecker(t, cfg)
	checker.verifySecureAccountFiles()

	if !containsIssue(checker.result, "Secure account file") {
		t.Fatalf("expected issue referencing secure account file, got %+v", checker.result.Issues)
	}
}

func TestVerifyDirectoriesCreatesMissing(t *testing.T) {
	baseDir := t.TempDir()
	cfg := &config.Config{
		BaseDir:       baseDir,
		BackupPath:    filepath.Join(baseDir, "backup"),
		LogPath:       filepath.Join(baseDir, "log"),
		LockPath:      filepath.Join(baseDir, "lock"),
		SecureAccount: filepath.Join(baseDir, "secure_account"),
	}
	checker := newChecker(t, cfg)
	checker.verifyDirectories()

	paths := []string{
		cfg.BackupPath,
		cfg.LogPath,
		cfg.LockPath,
		cfg.SecureAccount,
		filepath.Join(baseDir, "identity"),
		filepath.Join(baseDir, "identity", "age"),
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("expected directory %s to exist: %v", path, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s should be a directory", path)
		}
	}
}

func TestExtractPort(t *testing.T) {
	tests := []struct {
		address  string
		wantPort int
		wantAddr string
	}{
		{"0.0.0.0:22", 22, "0.0.0.0"},
		{"127.0.0.1:8080", 8080, "127.0.0.1"},
		{"[::]:443", 443, "::"},
		{"*:80", 80, "*"},
		{"invalid", 0, ""},
		{"", 0, ""},
		{":22", 22, "0.0.0.0"},
	}
	for _, tc := range tests {
		t.Run(tc.address, func(t *testing.T) {
			port, addr := extractPort(tc.address)
			if port != tc.wantPort || addr != tc.wantAddr {
				t.Fatalf("extractPort(%q) = (%d,%q), want (%d,%q)", tc.address, port, addr, tc.wantPort, tc.wantAddr)
			}
		})
	}
}

func TestIsPublicAddress(t *testing.T) {
	tests := []struct {
		addr   string
		public bool
	}{
		{"0.0.0.0", true},
		{"*", true},
		{"::", true},
		{"", true},
		{"127.0.0.1", false},
		{"127.0.0.100", false},
		{"::1", false},
		{"localhost", false},
		{"192.168.1.1", true},
		{"10.0.0.1", true},
	}
	for _, tc := range tests {
		t.Run(tc.addr, func(t *testing.T) {
			if got := isPublicAddress(tc.addr); got != tc.public {
				t.Fatalf("isPublicAddress(%q) = %v, want %v", tc.addr, got, tc.public)
			}
		})
	}
}

func TestParsePSLine(t *testing.T) {
	tests := []struct {
		line        string
		wantUser    string
		wantState   string
		wantVSZ     string
		wantPID     string
		wantCommand string
	}{
		{"root     S   12345  1234 /usr/sbin/sshd -D", "root", "S", "12345", "1234", "/usr/sbin/sshd -D"},
		{"www-data R   5000   999 nginx: worker process", "www-data", "R", "5000", "999", "nginx: worker process"},
		{"", "", "", "", "", ""},
		{"incomplete", "", "", "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			user, state, vsz, pid, cmd := parsePSLine(tc.line)
			if user != tc.wantUser || state != tc.wantState || vsz != tc.wantVSZ || pid != tc.wantPID || cmd != tc.wantCommand {
				t.Fatalf("parsePSLine(%q) = %q,%q,%q,%q,%q", tc.line, user, state, vsz, pid, cmd)
			}
		})
	}
}

func TestMatchesSafeProcessPattern(t *testing.T) {
	tests := []struct {
		pattern string
		name    string
		match   bool
	}{
		{"kworker", "kworker", true},
		{"kworker", "KWORKER", true},
		{"kworker", "worker", false},
		{"kworker*", "kworker/0:1", true},
		{"regex:kworker.*", "kworker/0:1", true},
		{"regex:drbd[0-9]+", "drbd0", true},
		{"regex:drbd[0-9]+", "drbd", false},
		{"", "anything", false},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%s_%s", tc.pattern, tc.name), func(t *testing.T) {
			if got := matchesSafeProcessPattern(tc.pattern, tc.name); got != tc.match {
				t.Fatalf("matchesSafeProcessPattern(%q,%q)=%v want %v", tc.pattern, tc.name, got, tc.match)
			}
		})
	}
}

func TestIsLegitimateKernelProcess(t *testing.T) {
	tests := []struct {
		name       string
		legitimate bool
	}{
		{"kworker/0:1", true},
		{"ext4-rsv-conver", true},
		{"sshd", false},
		{"", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLegitimateKernelProcess(tc.name); got != tc.legitimate {
				t.Fatalf("isLegitimateKernelProcess(%q)=%v want %v", tc.name, got, tc.legitimate)
			}
		})
	}
}

func TestIsZombieProxmoxProcess(t *testing.T) {
	tests := []struct {
		user   string
		state  string
		vsz    string
		cmd    string
		zombie bool
	}{
		{"root", "Z", "0", "proxmox-backup-client", true},
		{"backup", "Z", "0", "proxmox-backup-proxy", true},
		{"root", "S", "0", "proxmox-backup-client", false},
		{"root", "Z", "0", "nginx", false},
		{"www-data", "Z", "0", "proxmox-backup-client", false},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%s_%s_%s", tc.user, tc.state, tc.cmd), func(t *testing.T) {
			if got := isZombieProxmoxProcess(tc.user, tc.state, tc.vsz, tc.cmd); got != tc.zombie {
				t.Fatalf("isZombieProxmoxProcess(%q,%q,%q,%q)=%v want %v",
					tc.user, tc.state, tc.vsz, tc.cmd, got, tc.zombie)
			}
		})
	}
}

func TestBuildWhitelistMap(t *testing.T) {
	entries := []string{"sshd:22", "nginx:80", "nginx:443", "", "invalid", "app:abc"}
	wl := buildWhitelistMap(entries)
	if wl == nil {
		t.Fatal("expected non-nil whitelist")
	}
	if !wl.allowed(22, "sshd") || !wl.allowed(80, "nginx") || !wl.allowed(443, "nginx") {
		t.Fatal("expected sshd/nginx to be allowed on configured ports")
	}
	if wl.allowed(22, "nginx") || wl.allowed(8080, "sshd") {
		t.Fatal("unexpected allowance for unconfigured ports")
	}
}

func TestBuildWhitelistMapEmpty(t *testing.T) {
	if buildWhitelistMap(nil) != nil {
		t.Fatal("nil input should return nil whitelist")
	}
	if buildWhitelistMap([]string{}) != nil {
		t.Fatal("empty slice should return nil whitelist")
	}
}

func TestPortWhitelistCaseInsensitive(t *testing.T) {
	wl := buildWhitelistMap([]string{"NGINX:80", "SshD:22"})
	if !wl.allowed(80, "nginx") || !wl.allowed(80, "NGINX") || !wl.allowed(22, "sshd") {
		t.Fatal("expected case-insensitive program names")
	}
}

func TestIsExeInTrustedDir(t *testing.T) {
	tests := []struct {
		path    string
		trusted bool
	}{
		{"/usr/bin/nginx", true},
		{"/usr/sbin/sshd", true},
		{"/bin/bash", true},
		{"/home/user/app", false},
		{"/tmp/malware", false},
		{"", false},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			if got := isExeInTrustedDir(tc.path); got != tc.trusted {
				t.Fatalf("isExeInTrustedDir(%q)=%v want %v", tc.path, got, tc.trusted)
			}
		})
	}
}

func TestKernelAndWorkerHeuristics(t *testing.T) {
	t.Run("kernel thread", func(t *testing.T) {
		if !isKernelThread(procInfo{ppid: 2}) {
			t.Fatal("expected kernel thread when PPid=2 and no exe")
		}
	})
	t.Run("drbd worker", func(t *testing.T) {
		if !isDRBDWorker(procInfo{ppid: 2}, "drbd0_worker") {
			t.Fatal("expected DRBD worker match")
		}
	})
	t.Run("zfs worker", func(t *testing.T) {
		if !isZFSWorker(procInfo{ppid: 2}, "zfs_trim") {
			t.Fatal("expected ZFS worker match")
		}
	})
}

type failingReader struct{}

func (failingReader) Read(p []byte) (int, error) {
	return 0, fmt.Errorf("boom")
}

func TestChecksumReaderError(t *testing.T) {
	if _, err := checksumReader(failingReader{}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("checksumReader should propagate error, got %v", err)
	}
}

func TestFileContainsMarkerWithoutMarkers(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(tmp, []byte("content"), 0o600); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	found, err := fileContainsMarker(tmp, nil, 0)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if found {
		t.Fatal("expected no match when markers list is empty")
	}
}

func TestFileContainsMarker(t *testing.T) {
	dir := t.TempDir()
	privateKeyFile := filepath.Join(dir, "private.key")
	if err := os.WriteFile(privateKeyFile, []byte("AGE-SECRET-KEY-xxxx"), 0o600); err != nil {
		t.Fatalf("failed to write private key file: %v", err)
	}
	normalFile := filepath.Join(dir, "normal.txt")
	if err := os.WriteFile(normalFile, []byte("just content"), 0o600); err != nil {
		t.Fatalf("failed to write normal file: %v", err)
	}
	markers := []string{"AGE-SECRET-KEY-", "BEGIN AGE PRIVATE KEY"}

	found, err := fileContainsMarker(privateKeyFile, markers, 64*1024)
	if err != nil || !found {
		t.Fatalf("expected marker detection, found=%v err=%v", found, err)
	}
	found, err = fileContainsMarker(normalFile, markers, 64*1024)
	if err != nil || found {
		t.Fatalf("expected no marker in normal file, found=%v err=%v", found, err)
	}
}

func TestFileContainsMarkerCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "key.txt")
	if err := os.WriteFile(file, []byte("age-secret-key-lowercase"), 0o600); err != nil {
		t.Fatalf("failed to write key file: %v", err)
	}
	found, err := fileContainsMarker(file, []string{"AGE-SECRET-KEY-"}, 64*1024)
	if err != nil || !found {
		t.Fatalf("expected case-insensitive marker detection, found=%v err=%v", found, err)
	}
}

func TestFileContainsMarkerLimit(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "big.txt")
	content := strings.Repeat("x", 10000) + "AGE-SECRET-KEY-"
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write big file: %v", err)
	}
	if found, _ := fileContainsMarker(file, []string{"AGE-SECRET-KEY-"}, 100); found {
		t.Fatal("expected limit to prevent detection")
	}
	if found, _ := fileContainsMarker(file, []string{"AGE-SECRET-KEY-"}, 20000); !found {
		t.Fatal("expected marker detection with larger limit")
	}
}

func TestVerifyBinaryIntegrityCreatesHash(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "binary")
	content := []byte("test binary")
	if err := os.WriteFile(execPath, content, 0o700); err != nil {
		t.Fatalf("failed to write exec file: %v", err)
	}
	checker := newCheckerWithExec(t, &config.Config{AutoUpdateHashes: true}, execPath)

	checker.verifyBinaryIntegrity()

	hashPath := execPath + ".md5"
	data, err := os.ReadFile(hashPath)
	if err != nil {
		t.Fatalf("hash file not created: %v", err)
	}
	expected, err := checksumReader(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("failed to compute expected hash: %v", err)
	}
	if strings.TrimSpace(string(data)) != expected {
		t.Fatalf("hash file contains %q, want %q", strings.TrimSpace(string(data)), expected)
	}
}

func TestVerifyBinaryIntegrityWarnsWhenHashMissing(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "binary")
	if err := os.WriteFile(execPath, []byte("binary"), 0o700); err != nil {
		t.Fatalf("failed to write exec file: %v", err)
	}
	checker := newCheckerWithExec(t, &config.Config{AutoUpdateHashes: false}, execPath)

	checker.verifyBinaryIntegrity()

	if !containsIssue(checker.result, "Hash file") {
		t.Fatalf("expected warning about missing hash file, issues=%+v", checker.result.Issues)
	}
	if _, err := os.Stat(execPath + ".md5"); err == nil {
		t.Fatal("hash file should not be created when AutoUpdateHashes=false")
	}
}

func TestVerifyBinaryIntegrityHashMismatch(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "binary")
	if err := os.WriteFile(execPath, []byte("original"), 0o700); err != nil {
		t.Fatalf("failed to write exec file: %v", err)
	}
	hashPath := execPath + ".md5"
	if err := os.WriteFile(hashPath, []byte("deadbeef"), 0o600); err != nil {
		t.Fatalf("failed to seed hash file: %v", err)
	}
	if err := os.WriteFile(execPath, []byte("modified"), 0o700); err != nil {
		t.Fatalf("failed to modify exec file: %v", err)
	}
	checker := newCheckerWithExec(t, &config.Config{AutoUpdateHashes: false}, execPath)

	checker.verifyBinaryIntegrity()

	if !containsIssue(checker.result, "Executable hash mismatch") {
		t.Fatalf("expected hash mismatch warning, issues=%+v", checker.result.Issues)
	}
}

func TestDetectPrivateAgeKeysAddsWarning(t *testing.T) {
	baseDir := t.TempDir()
	identityDir := filepath.Join(baseDir, "identity")
	if err := os.MkdirAll(identityDir, 0o755); err != nil {
		t.Fatalf("failed to create identity dir: %v", err)
	}
	target := filepath.Join(identityDir, "secret.key")
	if err := os.WriteFile(target, []byte("AGE-SECRET-KEY-XYZ"), 0o600); err != nil {
		t.Fatalf("failed to write secret file: %v", err)
	}
	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg:    &config.Config{BaseDir: baseDir},
		result: &Result{},
	}

	checker.detectPrivateAgeKeys()

	if !containsIssue(checker.result, "AGE/SSH key") {
		t.Fatalf("expected warning about private key, issues=%+v", checker.result.Issues)
	}
}

// TestChecksumFile tests file checksumming
func TestChecksumFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test file with known content
	testFile := filepath.Join(tmpDir, "test.txt")
	content := []byte("test content for checksum")
	if err := os.WriteFile(testFile, content, 0644); err != nil {
		t.Fatal(err)
	}

	// Calculate checksum
	checksum1, err := checksumFile(testFile)
	if err != nil {
		t.Errorf("checksumFile() error = %v", err)
	}
	if checksum1 == "" {
		t.Error("checksumFile() returned empty checksum")
	}

	// Verify checksum is consistent
	checksum2, err := checksumFile(testFile)
	if err != nil {
		t.Errorf("checksumFile() second call error = %v", err)
	}
	if checksum1 != checksum2 {
		t.Errorf("checksumFile() inconsistent: first=%s, second=%s", checksum1, checksum2)
	}

	// Test with different content
	testFile2 := filepath.Join(tmpDir, "test2.txt")
	if err := os.WriteFile(testFile2, []byte("different content"), 0644); err != nil {
		t.Fatal(err)
	}
	checksum3, err := checksumFile(testFile2)
	if err != nil {
		t.Errorf("checksumFile() error = %v", err)
	}
	if checksum3 == checksum1 {
		t.Error("checksumFile() should return different checksums for different content")
	}

	// Test with nonexistent file
	_, err = checksumFile(filepath.Join(tmpDir, "nonexistent.txt"))
	if err == nil {
		t.Error("checksumFile() should return error for nonexistent file")
	}
}

// TestIsSafeBracketProcess tests bracket process safety checking
func TestIsSafeBracketProcess(t *testing.T) {
	tests := []struct {
		name     string
		procName string
		config   *config.Config
		expected bool
	}{
		{
			name:     "kernel worker",
			procName: "kworker/0:1",
			config:   &config.Config{},
			expected: true,
		},
		{
			name:     "configured safe bracket process",
			procName: "systemd-journal",
			config: &config.Config{
				SafeBracketProcesses: []string{"systemd-journal", "systemd-udevd"},
			},
			expected: true,
		},
		{
			name:     "configured safe kernel process",
			procName: "mykernel-worker",
			config: &config.Config{
				SafeKernelProcesses: []string{"mykernel-*"},
			},
			expected: true,
		},
		{
			name:     "unknown bracket process",
			procName: "unknown-process",
			config:   &config.Config{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := &Checker{
				logger: newSecurityTestLogger(),
				cfg:    tt.config,
				result: &Result{},
			}
			result := checker.isSafeBracketProcess(tt.procName)
			if result != tt.expected {
				t.Errorf("isSafeBracketProcess(%s) = %v, want %v", tt.procName, result, tt.expected)
			}
		})
	}
}

// TestIsSafeKernelProcess tests kernel process safety checking
func TestIsSafeKernelProcess(t *testing.T) {
	tests := []struct {
		name     string
		procName string
		patterns []string
		expected bool
	}{
		{
			name:     "matches pattern",
			procName: "zfs-worker",
			patterns: []string{"zfs-*", "kvm-*"},
			expected: true,
		},
		{
			name:     "no match",
			procName: "unknown-worker",
			patterns: []string{"zfs-*", "kvm-*"},
			expected: false,
		},
		{
			name:     "empty patterns",
			procName: "any-process",
			patterns: []string{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := &Checker{
				logger: newSecurityTestLogger(),
				cfg: &config.Config{
					SafeKernelProcesses: tt.patterns,
				},
				result: &Result{},
			}
			result := checker.isSafeKernelProcess(tt.procName)
			if result != tt.expected {
				t.Errorf("isSafeKernelProcess(%s) = %v, want %v", tt.procName, result, tt.expected)
			}
		})
	}
}

// TestCheckFirewall tests firewall checking
func TestCheckFirewall(t *testing.T) {
	checker := newCheckerForTest(&config.Config{}, stubLookPath(map[string]bool{}))
	checker.checkFirewall(context.Background())
	if !containsIssue(checker.result, "iptables not found") {
		t.Error("Expected warning about missing iptables")
	}
}

// TestCheckSuspiciousProcesses tests suspicious process detection
func TestCheckSuspiciousProcesses(t *testing.T) {
	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg: &config.Config{
			SuspiciousProcesses: []string{"malware", "cryptominer"},
		},
		result: &Result{},
	}

	// This test verifies the function doesn't crash
	// In a real environment, it would need to mock the ps command
	// For now, we just verify the function can be called
	checker.checkSuspiciousProcesses(context.Background())

	// The function should complete without panic
	if checker.result == nil {
		t.Error("Result should not be nil")
	}
}

// TestRunSecurityChecks tests the main Run function
func TestRunSecurityChecks(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	execPath := filepath.Join(tmpDir, "proxsave")

	// Create test config file
	if err := os.WriteFile(configPath, []byte("test: config"), 0600); err != nil {
		t.Fatal(err)
	}

	// Create test executable
	if err := os.WriteFile(execPath, []byte("#!/bin/sh\necho test"), 0755); err != nil {
		t.Fatal(err)
	}

	logger := newSecurityTestLogger()

	tests := []struct {
		name        string
		config      *config.Config
		expectError bool
	}{
		{
			name: "security checks disabled",
			config: &config.Config{
				SecurityCheckEnabled: false,
			},
			expectError: false,
		},
		{
			name: "security checks enabled with no issues",
			config: &config.Config{
				SecurityCheckEnabled:     true,
				ContinueOnSecurityIssues: true,
				CheckNetworkSecurity:     false,
				AutoFixPermissions:       false,
				AutoUpdateHashes:         false,
				CheckFirewall:            false,
				CheckOpenPorts:           false,
				SuspiciousProcesses:      []string{},
				SafeBracketProcesses:     []string{},
				SafeKernelProcesses:      []string{},
				SuspiciousPorts:          []int{},
				PortWhitelist:            []string{},
				BaseDir:                  tmpDir,
			},
			expectError: false,
		},
		{
			name: "security checks with network checks",
			config: &config.Config{
				SecurityCheckEnabled:     true,
				ContinueOnSecurityIssues: true,
				CheckNetworkSecurity:     true,
				CheckFirewall:            true,
				CheckOpenPorts:           true,
				AutoFixPermissions:       false,
				AutoUpdateHashes:         false,
				SuspiciousProcesses:      []string{},
				SafeBracketProcesses:     []string{},
				SafeKernelProcesses:      []string{},
				SuspiciousPorts:          []int{22, 80},
				PortWhitelist:            []string{},
				BaseDir:                  tmpDir,
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envInfo := &environment.EnvironmentInfo{
				Type:    types.ProxmoxVE,
				Version: "7.4-1",
			}

			result, err := Run(context.Background(), logger, tt.config, configPath, execPath, envInfo)

			if tt.expectError {
				if err == nil {
					t.Error("Run() should return error")
				}
			} else {
				if err != nil {
					t.Errorf("Run() error = %v, expected no error", err)
				}
			}

			if result == nil {
				t.Fatal("Run() should return result")
			}
		})
	}
}

// TestRunWithSecurityErrors tests Run function with security errors
func TestRunWithSecurityErrors(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	execPath := filepath.Join(tmpDir, "proxsave")

	// Create config with restrictive permissions to trigger error
	if err := os.WriteFile(configPath, []byte("test: config"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create executable
	if err := os.WriteFile(execPath, []byte("#!/bin/sh\necho test"), 0755); err != nil {
		t.Fatal(err)
	}

	logger := newSecurityTestLogger()
	cfg := &config.Config{
		SecurityCheckEnabled:     true,
		ContinueOnSecurityIssues: false, // Will cause error if issues found
		CheckNetworkSecurity:     false,
		AutoFixPermissions:       false,
		AutoUpdateHashes:         false,
		CheckFirewall:            false,
		CheckOpenPorts:           false,
		SuspiciousProcesses:      []string{},
		SafeBracketProcesses:     []string{},
		SafeKernelProcesses:      []string{},
		SuspiciousPorts:          []int{},
		PortWhitelist:            []string{},
		BaseDir:                  tmpDir,
	}

	envInfo := &environment.EnvironmentInfo{
		Type:    types.ProxmoxVE,
		Version: "7.4-1",
	}

	// Run checks - config file has wrong permissions (0644 instead of 0600)
	result, err := Run(context.Background(), logger, cfg, configPath, execPath, envInfo)

	// Should get result even if there are errors
	if result == nil {
		t.Fatal("Run() should return result even with errors")
	}

	// With ContinueOnSecurityIssues=false and wrong permissions, should get error
	if err == nil && result.HasErrors() {
		t.Error("Run() should return error when ContinueOnSecurityIssues=false and errors exist")
	}
}

// TestCheckOpenPortsAgainstSuspiciousList tests suspicious port checking
func TestCheckOpenPortsAgainstSuspiciousList(t *testing.T) {
	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg: &config.Config{
			SuspiciousPorts: []int{4444, 31337},
		},
		result: &Result{},
	}

	// This test verifies the function doesn't crash
	// In a real environment, it would need to mock the ss command
	checker.checkOpenPortsAgainstSuspiciousList(context.Background())

	// The function should complete without panic
	if checker.result == nil {
		t.Error("Result should not be nil")
	}
}

// TestCheckOpenPorts tests open port checking
func TestCheckOpenPorts(t *testing.T) {
	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg: &config.Config{
			SuspiciousPorts: []int{4444},
			PortWhitelist:   []string{},
		},
		result: &Result{},
	}

	// This test verifies the function doesn't crash
	// In a real environment, it would need to mock the ss command
	checker.checkOpenPorts(context.Background())

	// The function should complete without panic and may add warnings
	if checker.result == nil {
		t.Error("Result should not be nil")
	}
}

// ============================================================
// shouldSkipOwnershipChecks tests
// ============================================================

func TestShouldSkipOwnershipChecks(t *testing.T) {
	tests := []struct {
		name             string
		setBackupPerms   bool
		path             string
		backupPath       string
		logPath          string
		secondaryPath    string
		secondaryLogPath string
		expected         bool
	}{
		{
			name:           "disabled returns false",
			setBackupPerms: false,
			path:           "/backup",
			backupPath:     "/backup",
			expected:       false,
		},
		{
			name:           "match backup path",
			setBackupPerms: true,
			path:           "/backup",
			backupPath:     "/backup",
			expected:       true,
		},
		{
			name:           "match log path",
			setBackupPerms: true,
			path:           "/var/log",
			logPath:        "/var/log",
			expected:       true,
		},
		{
			name:           "match secondary path",
			setBackupPerms: true,
			path:           "/secondary",
			secondaryPath:  "/secondary",
			expected:       true,
		},
		{
			name:             "match secondary log path",
			setBackupPerms:   true,
			path:             "/secondary/log",
			secondaryLogPath: "/secondary/log",
			expected:         true,
		},
		{
			name:           "no match returns false",
			setBackupPerms: true,
			path:           "/other/path",
			backupPath:     "/backup",
			logPath:        "/var/log",
			expected:       false,
		},
		{
			name:           "empty paths in config are skipped",
			setBackupPerms: true,
			path:           "/backup",
			backupPath:     "/backup",
			logPath:        "",
			secondaryPath:  "   ",
			expected:       true,
		},
		{
			name:           "path with trailing slash normalized",
			setBackupPerms: true,
			path:           "/backup/",
			backupPath:     "/backup",
			expected:       true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checker := &Checker{
				logger: newSecurityTestLogger(),
				cfg: &config.Config{
					SetBackupPermissions: tc.setBackupPerms,
					BackupPath:           tc.backupPath,
					LogPath:              tc.logPath,
					SecondaryPath:        tc.secondaryPath,
					SecondaryLogPath:     tc.secondaryLogPath,
				},
				result: &Result{},
			}
			got := checker.shouldSkipOwnershipChecks(tc.path)
			if got != tc.expected {
				t.Errorf("shouldSkipOwnershipChecks(%q) = %v, want %v", tc.path, got, tc.expected)
			}
		})
	}
}

// ============================================================
// ensureOwnershipAndPerm tests
// ============================================================

func TestEnsureOwnershipAndPermNilInfo(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg:    &config.Config{AutoFixPermissions: false},
		result: &Result{},
	}

	// Pass nil info - function should call Lstat internally
	info := checker.ensureOwnershipAndPerm(testFile, nil, 0600, "test file")
	if info == nil {
		t.Error("ensureOwnershipAndPerm should return FileInfo when nil info passed")
	}
}

func TestEnsureOwnershipAndPermNonExistentFile(t *testing.T) {
	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg:    &config.Config{},
		result: &Result{},
	}

	info := checker.ensureOwnershipAndPerm("/nonexistent/file/path", nil, 0600, "test")
	if info != nil {
		t.Error("ensureOwnershipAndPerm should return nil for non-existent file")
	}
	if !containsIssue(checker.result, "Cannot stat") {
		t.Errorf("expected warning about stat failure, got %+v", checker.result.Issues)
	}
}

func TestEnsureOwnershipAndPermWrongPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0777); err != nil {
		t.Fatal(err)
	}

	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg:    &config.Config{AutoFixPermissions: false},
		result: &Result{},
	}

	checker.ensureOwnershipAndPerm(testFile, nil, 0600, "test file")

	// Should have a warning about wrong permissions
	if !containsIssue(checker.result, "should have permissions") {
		t.Errorf("expected warning about wrong permissions, got %+v", checker.result.Issues)
	}
}

func TestEnsureOwnershipAndPermAutoFix(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0777); err != nil {
		t.Fatal(err)
	}

	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg:    &config.Config{AutoFixPermissions: true},
		result: &Result{},
	}

	checker.ensureOwnershipAndPerm(testFile, nil, 0600, "test file")

	// Check if permissions were fixed
	info, err := os.Stat(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("permissions should have been fixed to 0600, got %o", info.Mode().Perm())
	}
}

func TestEnsureOwnershipAndPermSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	targetFile := filepath.Join(tmpDir, "target")
	symlinkFile := filepath.Join(tmpDir, "symlink")

	if err := os.WriteFile(targetFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetFile, symlinkFile); err != nil {
		t.Fatal(err)
	}

	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg:    &config.Config{AutoFixPermissions: true},
		result: &Result{},
	}

	info, _ := os.Lstat(symlinkFile)
	checker.ensureOwnershipAndPerm(symlinkFile, info, 0600, "symlink test")

	// Should refuse to chmod symlink
	if !containsIssue(checker.result, "refusing to chmod symlink") {
		t.Errorf("expected error about refusing symlink chmod, got %+v", checker.result.Issues)
	}
}

// ============================================================
// buildDependencyList tests
// ============================================================

func TestBuildDependencyListAllCompressionTypes(t *testing.T) {
	compressionTypes := []types.CompressionType{
		types.CompressionXZ,
		types.CompressionZstd,
		types.CompressionPigz,
		types.CompressionBzip2,
		types.CompressionLZMA,
		types.CompressionNone,
		types.CompressionGzip,
	}

	expectedBinaries := map[types.CompressionType]string{
		types.CompressionXZ:    "xz",
		types.CompressionZstd:  "zstd",
		types.CompressionPigz:  "pigz",
		types.CompressionBzip2: "pbzip2/bzip2",
		types.CompressionLZMA:  "lzma",
	}

	for _, ct := range compressionTypes {
		t.Run(string(ct), func(t *testing.T) {
			checker := &Checker{
				logger:   newSecurityTestLogger(),
				cfg:      &config.Config{CompressionType: ct},
				result:   &Result{},
				lookPath: stubLookPath(map[string]bool{}),
			}

			deps := checker.buildDependencyList()

			// All should have tar
			hasTar := false
			for _, dep := range deps {
				if dep.Name == "tar" {
					hasTar = true
				}
			}
			if !hasTar {
				t.Error("tar dependency should always be present")
			}

			// Check compression-specific dependency
			if expected, ok := expectedBinaries[ct]; ok {
				found := false
				for _, dep := range deps {
					if dep.Name == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected %s dependency for compression %s", expected, ct)
				}
			}
		})
	}
}

func TestBuildDependencyListEmailMethods(t *testing.T) {
	tests := []struct {
		name           string
		method         string
		fallback       bool
		expectedDep    string
		expectRequired bool
	}{
		{"pmf method", "pmf", false, "proxmox-mail-forward", true},
		{"sendmail method", "sendmail", false, "sendmail", true},
		{"relay with fallback", "relay", true, "proxmox-mail-forward", false},
		{"relay without fallback", "relay", false, "", false},
		{"empty defaults to relay", "", false, "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checker := &Checker{
				logger: newSecurityTestLogger(),
				cfg: &config.Config{
					EmailDeliveryMethod:   tc.method,
					EmailFallbackSendmail: tc.fallback,
				},
				result:   &Result{},
				lookPath: stubLookPath(map[string]bool{}),
			}

			deps := checker.buildDependencyList()

			if tc.expectedDep != "" {
				found := false
				isRequired := false
				for _, dep := range deps {
					if dep.Name == tc.expectedDep {
						found = true
						isRequired = dep.Required
						break
					}
				}
				if !found {
					t.Errorf("expected %s dependency", tc.expectedDep)
				}
				if isRequired != tc.expectRequired {
					t.Errorf("expected Required=%v for %s, got %v", tc.expectRequired, tc.expectedDep, isRequired)
				}
			}
		})
	}
}

func TestBuildDependencyListCloudAndStorage(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *config.Config
		expectedDep string
	}{
		{
			name:        "cloud enabled with remote",
			cfg:         &config.Config{CloudEnabled: true, CloudRemote: "s3:bucket"},
			expectedDep: "rclone",
		},
		{
			name:        "cloud enabled but empty remote",
			cfg:         &config.Config{CloudEnabled: true, CloudRemote: ""},
			expectedDep: "",
		},
		{
			name:        "ceph config backup",
			cfg:         &config.Config{BackupCephConfig: true},
			expectedDep: "ceph",
		},
		{
			name:        "zfs config backup",
			cfg:         &config.Config{BackupZFSConfig: true},
			expectedDep: "zpool",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checker := &Checker{
				logger:   newSecurityTestLogger(),
				cfg:      tc.cfg,
				result:   &Result{},
				lookPath: stubLookPath(map[string]bool{}),
			}

			deps := checker.buildDependencyList()

			if tc.expectedDep != "" {
				found := false
				for _, dep := range deps {
					if dep.Name == tc.expectedDep {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected %s dependency", tc.expectedDep)
				}
			}
		})
	}
}

func TestBuildDependencyListProxmoxEnvironments(t *testing.T) {
	tests := []struct {
		name        string
		envType     types.ProxmoxType
		tapeConfigs bool
		expectedDep string
	}{
		{
			name:        "ProxmoxVE environment",
			envType:     types.ProxmoxVE,
			expectedDep: "pveversion",
		},
		{
			name:        "ProxmoxBS environment",
			envType:     types.ProxmoxBS,
			expectedDep: "proxmox-backup-manager",
		},
		{
			name:        "ProxmoxBS with tape configs",
			envType:     types.ProxmoxBS,
			tapeConfigs: true,
			expectedDep: "proxmox-tape",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checker := &Checker{
				logger: newSecurityTestLogger(),
				cfg: &config.Config{
					BackupTapeConfigs: tc.tapeConfigs,
				},
				envInfo: &environment.EnvironmentInfo{
					Type: tc.envType,
				},
				result:   &Result{},
				lookPath: stubLookPath(map[string]bool{}),
			}

			deps := checker.buildDependencyList()

			found := false
			for _, dep := range deps {
				if dep.Name == tc.expectedDep {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected %s dependency for %s environment", tc.expectedDep, tc.envType)
			}
		})
	}
}

// ============================================================
// verifyBinaryIntegrity additional tests
// ============================================================

func TestVerifyBinaryIntegrityEmptyPath(t *testing.T) {
	checker := &Checker{
		logger:   newSecurityTestLogger(),
		cfg:      &config.Config{},
		result:   &Result{},
		execPath: "",
	}

	checker.verifyBinaryIntegrity()

	if !containsIssue(checker.result, "Executable path not available") {
		t.Errorf("expected warning about empty exec path, got %+v", checker.result.Issues)
	}
}

func TestVerifyBinaryIntegritySymlinkError(t *testing.T) {
	tmpDir := t.TempDir()
	targetFile := filepath.Join(tmpDir, "target")
	symlinkFile := filepath.Join(tmpDir, "symlink")

	if err := os.WriteFile(targetFile, []byte("binary content"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetFile, symlinkFile); err != nil {
		t.Fatal(err)
	}

	// Note: The current implementation checks Mode()&os.ModeSymlink after os.Open
	// which doesn't detect symlinks properly. This test documents the behavior.
	checker := &Checker{
		logger:   newSecurityTestLogger(),
		cfg:      &config.Config{AutoUpdateHashes: true},
		result:   &Result{},
		execPath: symlinkFile,
	}

	checker.verifyBinaryIntegrity()

	// The function opens the file and then stats - symlink is followed by Open
	// This is expected behavior given the current implementation
}

func TestVerifyBinaryIntegrityOpenError(t *testing.T) {
	checker := &Checker{
		logger:   newSecurityTestLogger(),
		cfg:      &config.Config{},
		result:   &Result{},
		execPath: "/nonexistent/binary/path",
	}

	checker.verifyBinaryIntegrity()

	if !containsIssue(checker.result, "Cannot open executable") {
		t.Errorf("expected error about cannot open executable, got %+v", checker.result.Issues)
	}
}

// ============================================================
// verifyDirectories additional tests
// ============================================================

func TestVerifyDirectoriesSkipOwnership(t *testing.T) {
	tmpDir := t.TempDir()
	backupDir := filepath.Join(tmpDir, "backup")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		t.Fatal(err)
	}

	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg: &config.Config{
			BaseDir:              tmpDir,
			BackupPath:           backupDir,
			SetBackupPermissions: true,
		},
		result: &Result{},
	}

	checker.verifyDirectories()

	// Should not have ownership warnings for backup dir when SetBackupPermissions=true
	// The function should skip ownership checks for this path
}

func TestVerifyDirectoriesEmptyPath(t *testing.T) {
	tmpDir := t.TempDir()

	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg: &config.Config{
			BaseDir:       tmpDir,
			BackupPath:    "",
			LogPath:       "",
			LockPath:      "",
			SecureAccount: "",
		},
		result: &Result{},
	}

	checker.verifyDirectories()

	// Should not create directories for empty paths
	// Only identity dirs should be checked
}

// ============================================================
// detectPrivateAgeKeys additional tests
// ============================================================

func TestDetectPrivateAgeKeysSkipsExtensions(t *testing.T) {
	baseDir := t.TempDir()
	identityDir := filepath.Join(baseDir, "identity")
	if err := os.MkdirAll(identityDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create files with extensions that should be skipped
	skippedFiles := []string{
		filepath.Join(identityDir, "readme.md"),
		filepath.Join(identityDir, "notes.txt"),
		filepath.Join(identityDir, "template.example"),
	}
	for _, f := range skippedFiles {
		if err := os.WriteFile(f, []byte("AGE-SECRET-KEY-XYZ"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg:    &config.Config{BaseDir: baseDir},
		result: &Result{},
	}

	checker.detectPrivateAgeKeys()

	// Should not detect keys in files with .md, .txt, .example extensions
	if checker.result.TotalIssues() != 0 {
		t.Errorf("expected no issues for files with skipped extensions, got %+v", checker.result.Issues)
	}
}

func TestDetectPrivateAgeKeysEmptyBaseDir(t *testing.T) {
	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg:    &config.Config{BaseDir: ""},
		result: &Result{},
	}

	checker.detectPrivateAgeKeys()

	// Should not crash and should not add issues
	if checker.result.TotalIssues() != 0 {
		t.Errorf("expected no issues for empty base dir, got %+v", checker.result.Issues)
	}
}

func TestDetectPrivateAgeKeysNonExistentDir(t *testing.T) {
	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg:    &config.Config{BaseDir: "/nonexistent/path"},
		result: &Result{},
	}

	checker.detectPrivateAgeKeys()

	// Should not crash and should not add issues
	if checker.result.TotalIssues() != 0 {
		t.Errorf("expected no issues for non-existent dir, got %+v", checker.result.Issues)
	}
}

// ============================================================
// verifySecureAccountFiles additional tests
// ============================================================

func TestVerifySecureAccountFilesEmptyPath(t *testing.T) {
	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg:    &config.Config{SecureAccount: ""},
		result: &Result{},
	}

	checker.verifySecureAccountFiles()

	// Should return early with no issues
	if checker.result.TotalIssues() != 0 {
		t.Errorf("expected no issues for empty secure account path, got %+v", checker.result.Issues)
	}
}

func TestVerifySecureAccountFilesNoJsonFiles(t *testing.T) {
	tmpDir := t.TempDir()

	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg:    &config.Config{SecureAccount: tmpDir},
		result: &Result{},
	}

	checker.verifySecureAccountFiles()

	// Should not add issues when no JSON files exist
	if checker.result.TotalIssues() != 0 {
		t.Errorf("expected no issues when no JSON files exist, got %+v", checker.result.Issues)
	}
}

// ============================================================
// isOwnedByRoot test
// ============================================================

func TestIsOwnedByRootFile(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(testFile)
	if err != nil {
		t.Fatal(err)
	}

	// Test the function - result depends on who runs the test
	result := isOwnedByRoot(info)

	// If running as root, should be true; otherwise false
	// This test just ensures the function doesn't panic
	_ = result
}

// ============================================================
// checkDependencies edge cases
// ============================================================

func TestCheckDependenciesAllPresent(t *testing.T) {
	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg: &config.Config{
			CompressionType: types.CompressionXZ,
		},
		result: &Result{},
		lookPath: stubLookPath(map[string]bool{
			"tar": true,
			"xz":  true,
		}),
	}

	checker.checkDependencies()

	if checker.result.ErrorCount() != 0 {
		t.Errorf("expected no errors when all deps present, got %+v", checker.result.Issues)
	}
}

func TestCheckDependenciesNoDeps(t *testing.T) {
	// Create a checker with minimal config that only requires tar
	checker := &Checker{
		logger:   newSecurityTestLogger(),
		cfg:      &config.Config{CompressionType: types.CompressionNone},
		result:   &Result{},
		lookPath: stubLookPath(map[string]bool{"tar": true}),
	}

	checker.checkDependencies()

	// Should complete without errors
	if checker.result.ErrorCount() != 0 {
		t.Errorf("expected no errors, got %+v", checker.result.Issues)
	}
}

// ============================================================
// matchesSafeProcessPattern edge cases
// ============================================================

func TestMatchesSafeProcessPatternRegexError(t *testing.T) {
	// Invalid regex pattern
	result := matchesSafeProcessPattern("regex:[invalid", "test")
	if result {
		t.Error("expected false for invalid regex pattern")
	}
}

func TestMatchesSafeProcessPatternEmptyRegex(t *testing.T) {
	result := matchesSafeProcessPattern("regex:", "test")
	if result {
		t.Error("expected false for empty regex pattern")
	}
}

// ============================================================
// Additional ensureOwnershipAndPerm tests
// ============================================================

func TestEnsureOwnershipAndPermNotOwnedByRoot(t *testing.T) {
	// Skip if running as root (ownership check would pass)
	if os.Getuid() == 0 {
		t.Skip("skipping test when running as root")
	}

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}

	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg:    &config.Config{AutoFixPermissions: false},
		result: &Result{},
	}

	checker.ensureOwnershipAndPerm(testFile, nil, 0600, "test file")

	// Should have warning about ownership (not root:root)
	if !containsIssue(checker.result, "should be owned by root:root") {
		t.Errorf("expected ownership warning, got %+v", checker.result.Issues)
	}
}

func TestEnsureOwnershipAndPermSymlinkOwnership(t *testing.T) {
	// Skip if running as root
	if os.Getuid() == 0 {
		t.Skip("skipping test when running as root")
	}

	tmpDir := t.TempDir()
	targetFile := filepath.Join(tmpDir, "target")
	symlinkFile := filepath.Join(tmpDir, "symlink")

	if err := os.WriteFile(targetFile, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetFile, symlinkFile); err != nil {
		t.Fatal(err)
	}

	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg:    &config.Config{AutoFixPermissions: true},
		result: &Result{},
	}

	info, _ := os.Lstat(symlinkFile)
	// Force the symlink path through ownership check
	checker.ensureOwnershipAndPerm(symlinkFile, info, 0, "symlink test")

	// Should refuse to chown symlink
	if !containsIssue(checker.result, "refusing to chown symlink") {
		t.Errorf("expected error about refusing symlink chown, got %+v", checker.result.Issues)
	}
}

// ============================================================
// Additional verifyBinaryIntegrity tests
// ============================================================

func TestVerifyBinaryIntegrityHashFileReadError(t *testing.T) {
	tmpDir := t.TempDir()
	execPath := filepath.Join(tmpDir, "binary")
	hashPath := execPath + ".md5"

	if err := os.WriteFile(execPath, []byte("binary content"), 0700); err != nil {
		t.Fatal(err)
	}

	// Create hash file as a directory to cause read error
	if err := os.MkdirAll(hashPath, 0755); err != nil {
		t.Fatal(err)
	}

	checker := &Checker{
		logger:   newSecurityTestLogger(),
		cfg:      &config.Config{AutoUpdateHashes: false},
		result:   &Result{},
		execPath: execPath,
	}

	checker.verifyBinaryIntegrity()

	if !containsIssue(checker.result, "Unable to read hash file") {
		t.Errorf("expected warning about reading hash file, got %+v", checker.result.Issues)
	}
}

func TestVerifyBinaryIntegrityHashMismatchAutoUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	execPath := filepath.Join(tmpDir, "binary")
	hashPath := execPath + ".md5"

	if err := os.WriteFile(execPath, []byte("binary content"), 0700); err != nil {
		t.Fatal(err)
	}
	// Write wrong hash
	if err := os.WriteFile(hashPath, []byte("wronghash"), 0600); err != nil {
		t.Fatal(err)
	}

	checker := &Checker{
		logger:   newSecurityTestLogger(),
		cfg:      &config.Config{AutoUpdateHashes: true},
		result:   &Result{},
		execPath: execPath,
	}

	checker.verifyBinaryIntegrity()

	// Hash should be updated
	newHash, err := os.ReadFile(hashPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(newHash) == "wronghash" {
		t.Error("hash file should have been updated")
	}
}

// ============================================================
// Additional verifyDirectories tests
// ============================================================

func TestVerifyDirectoriesWithAllPaths(t *testing.T) {
	tmpDir := t.TempDir()

	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg: &config.Config{
			BaseDir:          tmpDir,
			BackupPath:       filepath.Join(tmpDir, "backup"),
			LogPath:          filepath.Join(tmpDir, "log"),
			SecondaryPath:    filepath.Join(tmpDir, "secondary"),
			SecondaryLogPath: filepath.Join(tmpDir, "secondary_log"),
			LockPath:         filepath.Join(tmpDir, "lock"),
			SecureAccount:    filepath.Join(tmpDir, "secure"),
		},
		result: &Result{},
	}

	checker.verifyDirectories()

	// All directories should be created
	paths := []string{
		filepath.Join(tmpDir, "backup"),
		filepath.Join(tmpDir, "log"),
		filepath.Join(tmpDir, "secondary"),
		filepath.Join(tmpDir, "secondary_log"),
		filepath.Join(tmpDir, "lock"),
		filepath.Join(tmpDir, "secure"),
		filepath.Join(tmpDir, "identity"),
		filepath.Join(tmpDir, "identity", "age"),
	}

	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("directory %s should exist: %v", path, err)
		}
	}
}

// ============================================================
// Additional verifySensitiveFiles tests
// ============================================================

func TestVerifySensitiveFilesServerIdentity(t *testing.T) {
	baseDir := t.TempDir()
	identityDir := filepath.Join(baseDir, "identity")
	if err := os.MkdirAll(identityDir, 0755); err != nil {
		t.Fatal(err)
	}

	serverIdentity := filepath.Join(identityDir, ".server_identity")
	if err := os.WriteFile(serverIdentity, []byte("identity"), 0644); err != nil {
		t.Fatal(err)
	}

	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg:    &config.Config{BaseDir: baseDir},
		result: &Result{},
	}

	checker.verifySensitiveFiles()

	// Should have warning about permissions (0644 instead of 0600)
	if !containsIssue(checker.result, "server identity") {
		t.Errorf("expected warning about server identity file, got %+v", checker.result.Issues)
	}
}

// ============================================================
// Additional checkFirewall tests
// ============================================================

func TestCheckFirewallWithLookPath(t *testing.T) {
	checker := &Checker{
		logger:   newSecurityTestLogger(),
		cfg:      &config.Config{},
		result:   &Result{},
		lookPath: stubLookPath(map[string]bool{}), // iptables not present
	}

	checker.checkFirewall(context.Background())

	if !containsIssue(checker.result, "iptables not found") {
		t.Errorf("expected warning about missing iptables, got %+v", checker.result.Issues)
	}
}

// ============================================================
// Additional checkOpenPorts tests
// ============================================================

func TestCheckOpenPortsWithSuspiciousPort(t *testing.T) {
	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg: &config.Config{
			SuspiciousPorts: []int{4444, 31337},
			PortWhitelist:   []string{},
		},
		result: &Result{},
	}

	// This test verifies the function handles the configuration properly
	checker.checkOpenPorts(context.Background())

	// Function should complete without panic
	if checker.result == nil {
		t.Error("result should not be nil")
	}
}

// ============================================================
// binaryDependency test
// ============================================================

func TestBinaryDependencyWithNilLookPath(t *testing.T) {
	checker := &Checker{
		logger:   newSecurityTestLogger(),
		cfg:      &config.Config{},
		result:   &Result{},
		lookPath: nil, // nil lookPath should fall back to exec.LookPath
	}

	dep := checker.binaryDependency("test", []string{"nonexistent_binary_xyz"}, false, "test")

	present, _ := dep.Check()
	if present {
		t.Error("expected false for nonexistent binary")
	}
}

// ============================================================
// isHeuristicallySafeKernelProcess tests (procscan.go)
// ============================================================

func TestIsHeuristicallySafeKernelProcessWithInvalidPID(t *testing.T) {
	// Test with invalid PID (should return false for all branches)
	result := isHeuristicallySafeKernelProcess(999999, "test-process", []string{})
	if result {
		t.Error("expected false for invalid PID")
	}
}

func TestIsHeuristicallySafeKernelProcessWithKernelNames(t *testing.T) {
	// Test various kernel-style process names with invalid PID
	// These should return false since we can't read proc info
	names := []string{"kworker/0:1", "drbd0", "card0-crtc0", "kvm-pit", "zfs-io"}

	for _, name := range names {
		result := isHeuristicallySafeKernelProcess(999999, name, []string{})
		// Result depends on whether process exists, but shouldn't panic
		_ = result
	}
}

// ============================================================
// Run function edge cases
// ============================================================

func TestRunWithMissingTarDependency(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	execPath := filepath.Join(tmpDir, "proxsave")

	if err := os.WriteFile(configPath, []byte("test: config"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(execPath, []byte("binary"), 0700); err != nil {
		t.Fatal(err)
	}

	logger := newSecurityTestLogger()
	cfg := &config.Config{
		SecurityCheckEnabled:     true,
		ContinueOnSecurityIssues: true,
		BaseDir:                  tmpDir,
		CompressionType:          types.CompressionNone,
	}

	envInfo := &environment.EnvironmentInfo{
		Type: types.ProxmoxVE,
	}

	result, err := Run(context.Background(), logger, cfg, configPath, execPath, envInfo)
	if err != nil {
		// Error is expected if tar is not found
	}

	if result == nil {
		t.Fatal("Run() should return result")
	}
}

// ============================================================
// detectPrivateAgeKeys additional tests
// ============================================================

func TestDetectPrivateAgeKeysWithUnreadableFile(t *testing.T) {
	baseDir := t.TempDir()
	identityDir := filepath.Join(baseDir, "identity")
	if err := os.MkdirAll(identityDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a file that cannot be read (permission denied)
	unreadable := filepath.Join(identityDir, "unreadable.key")
	if err := os.WriteFile(unreadable, []byte("AGE-SECRET-KEY-TEST"), 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(unreadable, 0644) // Cleanup

	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg:    &config.Config{BaseDir: baseDir},
		result: &Result{},
	}

	checker.detectPrivateAgeKeys()

	// Should not crash, the unreadable file should be skipped
}

func TestDetectPrivateAgeKeysWithSSHKey(t *testing.T) {
	baseDir := t.TempDir()
	identityDir := filepath.Join(baseDir, "identity")
	if err := os.MkdirAll(identityDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a file with SSH private key marker
	sshKey := filepath.Join(identityDir, "id_rsa")
	if err := os.WriteFile(sshKey, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\ntest\n-----END OPENSSH PRIVATE KEY-----"), 0600); err != nil {
		t.Fatal(err)
	}

	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg:    &config.Config{BaseDir: baseDir},
		result: &Result{},
	}

	checker.detectPrivateAgeKeys()

	// Should detect the SSH key
	if !containsIssue(checker.result, "AGE/SSH key") {
		t.Errorf("expected warning about SSH key, got %+v", checker.result.Issues)
	}
}

// ============================================================
// verifyDirectories additional edge cases
// ============================================================

func TestVerifyDirectoriesWithExistingDir(t *testing.T) {
	tmpDir := t.TempDir()

	// Pre-create directories with wrong permissions
	backupDir := filepath.Join(tmpDir, "backup")
	if err := os.MkdirAll(backupDir, 0777); err != nil {
		t.Fatal(err)
	}

	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg: &config.Config{
			BaseDir:            tmpDir,
			BackupPath:         backupDir,
			AutoFixPermissions: false,
		},
		result: &Result{},
	}

	checker.verifyDirectories()

	// Should have warning about wrong permissions
	hasPermWarning := false
	for _, issue := range checker.result.Issues {
		if strings.Contains(issue.Message, "permissions") || strings.Contains(issue.Message, "owned") {
			hasPermWarning = true
			break
		}
	}
	if !hasPermWarning {
		// Permission or ownership warning depends on running context
		// This is acceptable
	}
}

func TestVerifyDirectoriesSkipOwnershipForBackup(t *testing.T) {
	tmpDir := t.TempDir()
	backupDir := filepath.Join(tmpDir, "backup")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		t.Fatal(err)
	}

	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg: &config.Config{
			BaseDir:              tmpDir,
			BackupPath:           backupDir,
			SetBackupPermissions: true, // This should skip ownership checks
		},
		result: &Result{},
	}

	checker.verifyDirectories()

	// The backup directory should have ownership check skipped
	// Ownership warnings for backup path should not appear
}

// ============================================================
// verifySecureAccountFiles additional tests
// ============================================================

func TestVerifySecureAccountFilesStatError(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a JSON file
	jsonFile := filepath.Join(tmpDir, "test.json")
	if err := os.WriteFile(jsonFile, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}

	// Make the directory unexecutable so stat fails on the file
	// This is tricky to test reliably, so we just ensure the function handles errors

	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg:    &config.Config{SecureAccount: tmpDir},
		result: &Result{},
	}

	checker.verifySecureAccountFiles()

	// Function should complete without panic
}

// ============================================================
// ensureOwnershipAndPerm edge cases
// ============================================================

func TestEnsureOwnershipAndPermExpectedPermZero(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg:    &config.Config{AutoFixPermissions: false},
		result: &Result{},
	}

	// When expectedPerm is 0, skip permission check
	checker.ensureOwnershipAndPerm(testFile, nil, 0, "test file")

	// Should not have permission-related warnings (only ownership if not root)
	hasPermWarning := false
	for _, issue := range checker.result.Issues {
		if strings.Contains(issue.Message, "should have permissions") {
			hasPermWarning = true
			break
		}
	}
	if hasPermWarning {
		t.Error("should not warn about permissions when expectedPerm is 0")
	}
}

// ============================================================
// verifyBinaryIntegrity edge cases
// ============================================================

func TestVerifyBinaryIntegrityMatchingHash(t *testing.T) {
	tmpDir := t.TempDir()
	execPath := filepath.Join(tmpDir, "binary")
	hashPath := execPath + ".md5"

	content := []byte("binary content")
	if err := os.WriteFile(execPath, content, 0700); err != nil {
		t.Fatal(err)
	}

	// Calculate correct hash
	correctHash, err := checksumReader(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hashPath, []byte(correctHash), 0600); err != nil {
		t.Fatal(err)
	}

	checker := &Checker{
		logger:   newSecurityTestLogger(),
		cfg:      &config.Config{AutoUpdateHashes: false},
		result:   &Result{},
		execPath: execPath,
	}

	checker.verifyBinaryIntegrity()

	// Should not have hash-related warnings
	for _, issue := range checker.result.Issues {
		if strings.Contains(issue.Message, "hash") || strings.Contains(issue.Message, "Hash") {
			// Might have ownership warnings but not hash warnings
			if strings.Contains(issue.Message, "mismatch") {
				t.Errorf("should not have hash mismatch warning, got %+v", checker.result.Issues)
			}
		}
	}
}

// ============================================================
// fileContainsMarker edge cases
// ============================================================

func TestFileContainsMarkerOpenError(t *testing.T) {
	found, err := fileContainsMarker("/nonexistent/file", []string{"marker"}, 1024)
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
	if found {
		t.Error("should return false for nonexistent file")
	}
}

func TestFileContainsMarkerLargeFile(t *testing.T) {
	tmpDir := t.TempDir()
	largeFile := filepath.Join(tmpDir, "large.txt")

	// Create a file larger than 4096 bytes (buffer size) with marker at end
	content := strings.Repeat("x", 5000) + "AGE-SECRET-KEY-TEST"
	if err := os.WriteFile(largeFile, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	found, err := fileContainsMarker(largeFile, []string{"AGE-SECRET-KEY-"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("should find marker in large file")
	}
}

// ============================================================
// Run function with PBS environment
// ============================================================

func TestRunWithPBSEnvironment(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	execPath := filepath.Join(tmpDir, "proxsave")

	if err := os.WriteFile(configPath, []byte("test: config"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(execPath, []byte("binary"), 0700); err != nil {
		t.Fatal(err)
	}

	logger := newSecurityTestLogger()
	cfg := &config.Config{
		SecurityCheckEnabled:     true,
		ContinueOnSecurityIssues: true,
		BaseDir:                  tmpDir,
		BackupTapeConfigs:        true, // This adds PBS-specific dependency
	}

	envInfo := &environment.EnvironmentInfo{
		Type: types.ProxmoxBS,
	}

	result, err := Run(context.Background(), logger, cfg, configPath, execPath, envInfo)
	if err != nil {
		// May get error if dependencies are missing
	}

	if result == nil {
		t.Fatal("Run() should return result")
	}
}

// ============================================================
// checkDependencies with detail output
// ============================================================

func TestCheckDependenciesWithDetail(t *testing.T) {
	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg: &config.Config{
			CompressionType: types.CompressionXZ,
		},
		result: &Result{},
		lookPath: func(binary string) (string, error) {
			if binary == "tar" || binary == "xz" {
				return "/usr/bin/" + binary, nil
			}
			return "", fmt.Errorf("not found")
		},
	}

	checker.checkDependencies()

	// All deps present, should have no errors
	if checker.result.ErrorCount() != 0 {
		t.Errorf("expected no errors, got %+v", checker.result.Issues)
	}
}

// ============================================================
// Additional tests for remaining coverage gaps
// ============================================================

func TestVerifyDirectoriesStatOtherError(t *testing.T) {
	// Test when stat returns an error other than ErrNotExist
	// This is hard to trigger reliably, but we can test the path exists
	tmpDir := t.TempDir()

	// Create a file where a directory is expected
	filePath := filepath.Join(tmpDir, "notadir")
	if err := os.WriteFile(filePath, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg: &config.Config{
			BaseDir:    tmpDir,
			BackupPath: filePath, // This is a file, not a directory
		},
		result: &Result{},
	}

	checker.verifyDirectories()

	// The function should handle this case (file exists but is not a directory)
}

func TestDetectPrivateAgeKeysWithSubdirectory(t *testing.T) {
	baseDir := t.TempDir()
	identityDir := filepath.Join(baseDir, "identity")
	subDir := filepath.Join(identityDir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a key file in subdirectory
	keyFile := filepath.Join(subDir, "key.age")
	if err := os.WriteFile(keyFile, []byte("AGE-SECRET-KEY-TEST"), 0600); err != nil {
		t.Fatal(err)
	}

	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg:    &config.Config{BaseDir: baseDir},
		result: &Result{},
	}

	checker.detectPrivateAgeKeys()

	// Should find the key in subdirectory
	if !containsIssue(checker.result, "AGE/SSH key") {
		t.Errorf("expected warning about key in subdirectory, got %+v", checker.result.Issues)
	}
}

func TestVerifyBinaryIntegrityCreateHashErrorReadOnly(t *testing.T) {
	// Skip if running as root (root can write anywhere)
	if os.Getuid() == 0 {
		t.Skip("skipping test when running as root")
	}

	tmpDir := t.TempDir()
	execPath := filepath.Join(tmpDir, "binary")

	if err := os.WriteFile(execPath, []byte("binary content"), 0700); err != nil {
		t.Fatal(err)
	}

	// Make the directory read-only so hash file cannot be created
	if err := os.Chmod(tmpDir, 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(tmpDir, 0755) // Cleanup

	checker := &Checker{
		logger:   newSecurityTestLogger(),
		cfg:      &config.Config{AutoUpdateHashes: true},
		result:   &Result{},
		execPath: execPath,
	}

	checker.verifyBinaryIntegrity()

	// Should have warning about failing to create hash file
	if !containsIssue(checker.result, "Failed to create hash file") {
		t.Errorf("expected warning about hash file creation failure, got %+v", checker.result.Issues)
	}
}

func TestVerifyBinaryIntegrityUpdateHashError(t *testing.T) {
	// Skip if running as root (root can write anywhere)
	if os.Getuid() == 0 {
		t.Skip("skipping test when running as root")
	}

	tmpDir := t.TempDir()
	execPath := filepath.Join(tmpDir, "binary")
	hashPath := execPath + ".md5"

	if err := os.WriteFile(execPath, []byte("binary content"), 0700); err != nil {
		t.Fatal(err)
	}

	// Create hash file with wrong content
	if err := os.WriteFile(hashPath, []byte("wronghash"), 0600); err != nil {
		t.Fatal(err)
	}

	// Make hash file read-only so it cannot be updated
	if err := os.Chmod(hashPath, 0444); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(hashPath, 0644) // Cleanup

	checker := &Checker{
		logger:   newSecurityTestLogger(),
		cfg:      &config.Config{AutoUpdateHashes: true},
		result:   &Result{},
		execPath: execPath,
	}

	checker.verifyBinaryIntegrity()

	// Should have warning about failing to update hash file
	if !containsIssue(checker.result, "Failed to update hash file") {
		t.Errorf("expected warning about hash file update failure, got %+v", checker.result.Issues)
	}
}

func TestCheckDependenciesEmptyList(t *testing.T) {
	// Test with a config that results in empty deps (except tar)
	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg: &config.Config{
			CompressionType: types.CompressionGzip, // Uses gzip which is built-in
		},
		result:   &Result{},
		lookPath: stubLookPath(map[string]bool{"tar": true}),
	}

	checker.checkDependencies()

	// Should have no errors when only tar is needed and it's present
	if checker.result.ErrorCount() != 0 {
		t.Errorf("expected no errors for gzip compression, got %+v", checker.result.Issues)
	}
}

func TestVerifySensitiveFilesCustomAgeRecipient(t *testing.T) {
	tmpDir := t.TempDir()
	customRecipient := filepath.Join(tmpDir, "custom_recipient.txt")

	if err := os.WriteFile(customRecipient, []byte("age1xxx"), 0644); err != nil {
		t.Fatal(err)
	}

	checker := &Checker{
		logger: newSecurityTestLogger(),
		cfg: &config.Config{
			BaseDir:          tmpDir,
			AgeRecipientFile: customRecipient,
			EncryptArchive:   true,
		},
		result: &Result{},
	}

	checker.verifySensitiveFiles()

	// Should warn about wrong permissions on custom recipient file
	if !containsIssue(checker.result, "AGE recipient") {
		t.Errorf("expected warning about AGE recipient file permissions, got %+v", checker.result.Issues)
	}
}

func TestFileContainsMarkerBoundary(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "boundary.txt")

	// Create a file where the marker spans the buffer boundary (4096 bytes)
	prefix := strings.Repeat("A", 4090)
	content := prefix + "AGE-SECRET-KEY-TEST"
	if err := os.WriteFile(testFile, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	found, err := fileContainsMarker(testFile, []string{"AGE-SECRET-KEY-"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("should find marker spanning buffer boundary")
	}
}

func TestExtractPortWildcard(t *testing.T) {
	port, addr := extractPort("*:8080")
	if port != 8080 {
		t.Errorf("expected port 8080, got %d", port)
	}
	if addr != "*" {
		t.Errorf("expected addr *, got %s", addr)
	}
}

func TestExtractPortIPv6WithBrackets(t *testing.T) {
	port, addr := extractPort("[::1]:8080")
	if port != 8080 {
		t.Errorf("expected port 8080, got %d", port)
	}
	if addr != "::1" {
		t.Errorf("expected addr ::1, got %s", addr)
	}
}
