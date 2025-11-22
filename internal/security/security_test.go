package security

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxmox-backup/internal/config"
	"github.com/tis24dev/proxmox-backup/internal/logging"
	"github.com/tis24dev/proxmox-backup/internal/types"
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
		EmailFallbackSendmail: true, // sendmail becomes optional dependency
	}
	checker := newCheckerForTest(cfg, stubLookPath(map[string]bool{
		"tar": true, // present
		// sendmail missing -> warning
	}))

	checker.checkDependencies()

	if got := checker.result.WarningCount(); got != 1 {
		t.Fatalf("expected 1 warning, got %d issues=%+v", got, checker.result.Issues)
	}
	msg := checker.result.Issues[0].Message
	if !strings.Contains(msg, "Optional dependency") || !strings.Contains(msg, "sendmail") {
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
