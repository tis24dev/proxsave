package security

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

// TestMain points pbsTokenFilesFn at paths that never exist, so no test in this package stats the
// real /root/.pbs-token or the secure_account of an installed ProxSave through the defaultBaseDir
// fallback. The tests that exercise the token check install their own list with withPBSTokenFiles.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "proxsave-pbs-token-")
	if err != nil {
		panic(err)
	}
	missing := filepath.Join(dir, "missing")
	pbsTokenFilesFn = func(string) []string {
		return []string{filepath.Join(missing, "pbs_token")}
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// withPBSTokenFiles replaces the token list for one test: the files config.PBSTokenFiles names
// under secureAccount, with the /root entry moved to rootToken.
func withPBSTokenFiles(t *testing.T, rootToken string, seen *string) {
	t.Helper()
	prev := pbsTokenFilesFn
	pbsTokenFilesFn = func(secureAccount string) []string {
		if seen != nil {
			*seen = secureAccount
		}
		files := config.PBSTokenFiles(secureAccount)
		files[len(files)-1] = rootToken
		return files
	}
	t.Cleanup(func() { pbsTokenFilesFn = prev })
}

func TestVerifySensitiveFilesPBSTokenPermissions(t *testing.T) {
	secureDir := t.TempDir()
	token := filepath.Join(secureDir, "pbs_token")
	if err := os.WriteFile(token, []byte("name=secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var seen string
	withPBSTokenFiles(t, filepath.Join(t.TempDir(), ".pbs-token"), &seen)

	checker := newChecker(t, &config.Config{BaseDir: t.TempDir(), SecureAccount: secureDir})
	checker.verifySensitiveFiles(context.Background())

	if seen != secureDir {
		t.Fatalf("token list built from %q; want SECURE_ACCOUNT %q", seen, secureDir)
	}
	if !containsIssue(checker.result, "PBS token file "+token+" should have permissions 600") {
		t.Fatalf("expected a permission warning naming %s, got %+v", token, checker.result.Issues)
	}
}

func TestVerifySensitiveFilesPBSTokenAPIAndRootPaths(t *testing.T) {
	secureDir := t.TempDir()
	apiToken := filepath.Join(secureDir, "pbs_api_token")
	rootToken := filepath.Join(t.TempDir(), ".pbs-token")
	for _, path := range []string{apiToken, rootToken} {
		if err := os.WriteFile(path, []byte("secret\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	withPBSTokenFiles(t, rootToken, nil)

	checker := newChecker(t, &config.Config{BaseDir: t.TempDir(), SecureAccount: secureDir})
	checker.verifySensitiveFiles(context.Background())

	for _, path := range []string{apiToken, rootToken} {
		if !containsIssue(checker.result, "PBS token file "+path+" should have permissions 600") {
			t.Errorf("expected a permission warning naming %s, got %+v", path, checker.result.Issues)
		}
	}
}

func TestVerifySensitiveFilesPBSTokenAbsent(t *testing.T) {
	withPBSTokenFiles(t, filepath.Join(t.TempDir(), ".pbs-token"), nil)

	checker := newChecker(t, &config.Config{BaseDir: t.TempDir(), SecureAccount: t.TempDir()})
	checker.verifySensitiveFiles(context.Background())

	if checker.result.TotalIssues() != 0 {
		t.Fatalf("absent token files must raise nothing, got %+v", checker.result.Issues)
	}
}

func TestVerifySensitiveFilesPBSTokenSymlinkToLooseTarget(t *testing.T) {
	secureDir := t.TempDir()
	target := filepath.Join(t.TempDir(), "real_token")
	if err := os.WriteFile(target, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(secureDir, "pbs_token")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	withPBSTokenFiles(t, filepath.Join(t.TempDir(), ".pbs-token"), nil)

	checker := newChecker(t, &config.Config{BaseDir: t.TempDir(), SecureAccount: secureDir, AutoFixPermissions: true})
	checker.verifySensitiveFiles(context.Background())

	if !containsIssue(checker.result, "refusing to chmod symlink "+link) {
		t.Fatalf("expected the symlinked token to be refused, got %+v", checker.result.Issues)
	}
	if checker.result.ErrorCount() == 0 {
		t.Fatalf("a refused symlink must be an error, got %+v", checker.result.Issues)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("symlink target modified through the link: perms %o, want 644", got)
	}
}
