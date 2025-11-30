package orchestrator

import (
	"archive/tar"
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
)

// ========================================
// decrypt.go tests
// ========================================

func TestBuildDecryptPathOptions(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *config.Config
		wantCount int
		wantPaths []string
		wantLabel []string
	}{
		{
			name: "all paths enabled",
			cfg: &config.Config{
				BackupPath:       "/backup/local",
				SecondaryEnabled: true,
				SecondaryPath:    "/backup/secondary",
				CloudEnabled:     true,
				CloudRemote:      "/backup/cloud",
			},
			wantCount: 3,
			wantPaths: []string{"/backup/local", "/backup/secondary", "/backup/cloud"},
			wantLabel: []string{"Local backups", "Secondary backups", "Cloud backups"},
		},
		{
			name: "only local path",
			cfg: &config.Config{
				BackupPath:       "/backup/local",
				SecondaryEnabled: false,
				CloudEnabled:     false,
			},
			wantCount: 1,
			wantPaths: []string{"/backup/local"},
			wantLabel: []string{"Local backups"},
		},
		{
			name: "local and secondary",
			cfg: &config.Config{
				BackupPath:       "/backup/local",
				SecondaryEnabled: true,
				SecondaryPath:    "/backup/secondary",
				CloudEnabled:     false,
			},
			wantCount: 2,
			wantPaths: []string{"/backup/local", "/backup/secondary"},
			wantLabel: []string{"Local backups", "Secondary backups"},
		},
		{
			name: "empty backup path skipped",
			cfg: &config.Config{
				BackupPath:       "",
				SecondaryEnabled: true,
				SecondaryPath:    "/backup/secondary",
			},
			wantCount: 1,
			wantPaths: []string{"/backup/secondary"},
			wantLabel: []string{"Secondary backups"},
		},
		{
			name: "whitespace paths trimmed",
			cfg: &config.Config{
				BackupPath:       "  /backup/local  ",
				SecondaryEnabled: false,
			},
			wantCount: 1,
			wantPaths: []string{"/backup/local"},
			wantLabel: []string{"Local backups"},
		},
		{
			name: "cloud with rclone remote excluded",
			cfg: &config.Config{
				BackupPath:   "/backup/local",
				CloudEnabled: true,
				CloudRemote:  "gdrive:backups", // rclone remote, not local path
			},
			wantCount: 1,
			wantPaths: []string{"/backup/local"},
			wantLabel: []string{"Local backups"},
		},
		{
			name: "cloud with local absolute path included",
			cfg: &config.Config{
				BackupPath:   "/backup/local",
				CloudEnabled: true,
				CloudRemote:  "/mnt/cloud/backups",
			},
			wantCount: 2,
			wantPaths: []string{"/backup/local", "/mnt/cloud/backups"},
			wantLabel: []string{"Local backups", "Cloud backups"},
		},
		{
			name: "secondary enabled but path empty",
			cfg: &config.Config{
				BackupPath:       "/backup/local",
				SecondaryEnabled: true,
				SecondaryPath:    "   ",
			},
			wantCount: 1,
			wantPaths: []string{"/backup/local"},
			wantLabel: []string{"Local backups"},
		},
		{
			name: "cloud enabled but empty remote",
			cfg: &config.Config{
				BackupPath:   "/backup/local",
				CloudEnabled: true,
				CloudRemote:  "   ",
			},
			wantCount: 1,
			wantPaths: []string{"/backup/local"},
			wantLabel: []string{"Local backups"},
		},
		{
			name: "cloud absolute with colon allowed",
			cfg: &config.Config{
				BackupPath:   "/backup/local",
				CloudEnabled: true,
				CloudRemote:  "/mnt/backups:foo",
			},
			wantCount: 2,
			wantPaths: []string{"/backup/local", "/mnt/backups:foo"},
			wantLabel: []string{"Local backups", "Cloud backups"},
		},
		{
			name:      "all paths empty",
			cfg:       &config.Config{},
			wantCount: 0,
			wantPaths: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := buildDecryptPathOptions(tt.cfg)

			if len(options) != tt.wantCount {
				t.Errorf("buildDecryptPathOptions() returned %d options; want %d",
					len(options), tt.wantCount)
			}

			for i, wantPath := range tt.wantPaths {
				if i >= len(options) {
					break
				}
				if options[i].Path != wantPath {
					t.Errorf("option[%d].Path = %q; want %q", i, options[i].Path, wantPath)
				}
				if tt.wantLabel != nil && options[i].Label != tt.wantLabel[i] {
					t.Errorf("option[%d].Label = %q; want %q", i, options[i].Label, tt.wantLabel[i])
				}
			}
		})
	}
}

func TestParseIdentityInput(t *testing.T) {
	t.Run("passphrase derived identity", func(t *testing.T) {
		passphrase := "passphrase-identity"

		got, err := parseIdentityInput(passphrase)
		if err != nil {
			t.Fatalf("parseIdentityInput error: %v", err)
		}

		want, err := deriveDeterministicIdentitiesFromPassphrase(passphrase)
		if err != nil {
			t.Fatalf("deriveDeterministicIdentitiesFromPassphrase error: %v", err)
		}

		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("parseIdentityInput() identity mismatch, got %q want %q", fmt.Sprint(got), fmt.Sprint(want))
		}
	})

	t.Run("age secret key uppercased", func(t *testing.T) {
		id, err := age.GenerateX25519Identity()
		if err != nil {
			t.Fatalf("generate identity: %v", err)
		}
		secretLower := strings.ToLower(id.String())

		got, err := parseIdentityInput(secretLower)
		if err != nil {
			t.Fatalf("parseIdentityInput(%q) error: %v", secretLower, err)
		}

		if len(got) != 1 || fmt.Sprint(got[0]) != id.String() {
			t.Fatalf("parseIdentityInput() did not parse secret key correctly, got %q want %q", fmt.Sprint(got), id.String())
		}
	})
}

func TestSanitizeBundleEntryName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		input     string
		want      string
		expectErr bool
	}{
		{"simple name", "archive.tar", "archive.tar", false},
		{"leading dot slash", "./data/file.txt", "file.txt", false},
		{"nested path collapsed", "dir/sub/../data.bin", "data.bin", false},
		{"trailing spaces trimmed", "  manifest.json  ", "manifest.json", false},
		{"windows separators collapsed", "dir\\child\\file.txt", "file.txt", false},
		{"mixed windows traversal cleaned", ".\\nested\\..\\manifest.json", "manifest.json", false},
		{"windows traversal rejected", "..\\escape.txt", "", true},
		{"absolute path rejected", "/etc/passwd", "", true},
		{"parent directory prefix rejected", "../foo", "", true},
		{"multiple parent traversal rejected", "dir/../../foo", "", true},
		{"only dots rejected", "..", "", true},
		{"slash parent segment rejected", "a/../..", "", true},
		{"empty name", "   ", "", true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := sanitizeBundleEntryName(tt.input)
			if tt.expectErr {
				if err == nil {
					t.Fatalf("expected error for %q but got none (result=%q)", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("sanitizeBundleEntryName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractBundleToWorkdir(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	t.Run("extracts files into workdir", func(t *testing.T) {
		workDir := t.TempDir()
		bundlePath := createTestBundle(t, []bundleEntry{
			{name: "nested/archive.age", data: []byte("archive-data")},
			{name: "./manifest/backup.metadata", data: []byte("metadata-data")},
			{name: "checksum/backup.sha256", data: []byte("checksum-data")},
		})

		staged, err := extractBundleToWorkdir(bundlePath, workDir)
		if err != nil {
			t.Fatalf("extractBundleToWorkdir error: %v", err)
		}

		if staged.ArchivePath != filepath.Join(workDir, "archive.age") {
			t.Fatalf("ArchivePath = %q; want %q", staged.ArchivePath, filepath.Join(workDir, "archive.age"))
		}
		if staged.MetadataPath != filepath.Join(workDir, "backup.metadata") {
			t.Fatalf("MetadataPath = %q; want %q", staged.MetadataPath, filepath.Join(workDir, "backup.metadata"))
		}
		if staged.ChecksumPath != filepath.Join(workDir, "backup.sha256") {
			t.Fatalf("ChecksumPath = %q; want %q", staged.ChecksumPath, filepath.Join(workDir, "backup.sha256"))
		}

		if data, err := os.ReadFile(staged.ArchivePath); err != nil || string(data) != "archive-data" {
			t.Fatalf("archive contents = %q, err=%v; want archive-data", string(data), err)
		}
		if data, err := os.ReadFile(staged.MetadataPath); err != nil || string(data) != "metadata-data" {
			t.Fatalf("metadata contents = %q, err=%v; want metadata-data", string(data), err)
		}
		if data, err := os.ReadFile(staged.ChecksumPath); err != nil || string(data) != "checksum-data" {
			t.Fatalf("checksum contents = %q, err=%v; want checksum-data", string(data), err)
		}
	})

	t.Run("rejects traversal entry names", func(t *testing.T) {
		workDir := t.TempDir()
		bundlePath := createTestBundle(t, []bundleEntry{
			{name: "..\\escape.metadata", data: []byte("evil")},
		})

		if _, err := extractBundleToWorkdir(bundlePath, workDir); err == nil {
			t.Fatalf("expected error for traversal entry name")
		}
	})
}

type bundleEntry struct {
	name string
	data []byte
}

func createTestBundle(t *testing.T, entries []bundleEntry) string {
	t.Helper()
	dir := t.TempDir()
	bundlePath := filepath.Join(dir, "bundle.tar")

	f, err := os.Create(bundlePath)
	if err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	defer f.Close()

	tw := tar.NewWriter(f)
	for _, entry := range entries {
		hdr := &tar.Header{
			Name: entry.name,
			Mode: 0o640,
			Size: int64(len(entry.data)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %q: %v", entry.name, err)
		}
		if _, err := tw.Write(entry.data); err != nil {
			t.Fatalf("write data for %q: %v", entry.name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	return bundlePath
}

func TestEnsureWritablePath(t *testing.T) {
	t.Run("overwrite existing file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "existing.txt")
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatalf("write existing: %v", err)
		}

		reader := bufio.NewReader(strings.NewReader("1\n"))
		got, err := ensureWritablePath(context.Background(), reader, path, "test file")
		if err != nil {
			t.Fatalf("ensureWritablePath error: %v", err)
		}
		if got != path {
			t.Fatalf("ensureWritablePath returned %q; want %q", got, path)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected original file to be removed, stat err=%v", err)
		}
	})

	t.Run("enter new path", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "existing.txt")
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatalf("write existing: %v", err)
		}
		newPath := filepath.Join(dir, "new.txt")
		input := strings.NewReader("2\n" + newPath + "\n")

		got, err := ensureWritablePath(context.Background(), bufio.NewReader(input), path, "test file")
		if err != nil {
			t.Fatalf("ensureWritablePath error: %v", err)
		}
		if got != newPath {
			t.Fatalf("ensureWritablePath returned %q; want %q", got, newPath)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected original file to remain, stat err=%v", err)
		}
	})

	t.Run("abort", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "existing.txt")
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatalf("write existing: %v", err)
		}
		_, err := ensureWritablePath(context.Background(), bufio.NewReader(strings.NewReader("0\n")), path, "test file")
		if !errors.Is(err, ErrDecryptAborted) {
			t.Fatalf("expected ErrDecryptAborted; got %v", err)
		}
	})
}

func TestDecryptWithIdentity(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	recipient := id.Recipient()

	dir := t.TempDir()
	plain := []byte("secret data for testing")
	encryptedPath := filepath.Join(dir, "archive.age")
	decryptedPath := filepath.Join(dir, "archive")

	f, err := os.Create(encryptedPath)
	if err != nil {
		t.Fatalf("create encrypted file: %v", err)
	}
	encWriter, err := age.Encrypt(f, recipient)
	if err != nil {
		t.Fatalf("age.Encrypt: %v", err)
	}
	if _, err := encWriter.Write(plain); err != nil {
		t.Fatalf("write ciphertext: %v", err)
	}
	if err := encWriter.Close(); err != nil {
		t.Fatalf("close age writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}

	if err := decryptWithIdentity(encryptedPath, decryptedPath, id); err != nil {
		t.Fatalf("decryptWithIdentity: %v", err)
	}
	got, err := os.ReadFile(decryptedPath)
	if err != nil {
		t.Fatalf("read decrypted: %v", err)
	}
	if string(got) != string(plain) {
		t.Fatalf("decrypted content = %q; want %q", string(got), string(plain))
	}
}

func TestParseMenuIndex(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		max     int
		want    int
		wantErr bool
		errMsg  string
	}{
		{"valid first", "1", 5, 0, false, ""},
		{"valid middle", "3", 5, 2, false, ""},
		{"valid last", "5", 5, 4, false, ""},
		{"zero invalid", "0", 5, 0, true, ""},
		{"negative invalid", "-1", 5, 0, true, ""},
		{"exceeds max", "6", 5, 0, true, ""},
		{"non-numeric", "abc", 5, 0, true, ""},
		{"empty string", "", 5, 0, true, ""},
		{"whitespace", "  ", 5, 0, true, ""},
		{"float invalid", "2.5", 5, 0, true, ""},
		{"max zero", "1", 0, 0, true, "between 1 and 0"},
		{"max negative", "1", -1, 0, true, "between 1 and -1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMenuIndex(tt.input, tt.max)

			if tt.wantErr {
				if err == nil {
					t.Errorf("parseMenuIndex(%q, %d) expected error; got nil", tt.input, tt.max)
				} else if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("parseMenuIndex(%q, %d) error = %q; want substring %q", tt.input, tt.max, err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("parseMenuIndex(%q, %d) unexpected error: %v", tt.input, tt.max, err)
				}
				if got != tt.want {
					t.Errorf("parseMenuIndex(%q, %d) = %d; want %d", tt.input, tt.max, got, tt.want)
				}
			}
		})
	}
}

func TestFormatTargets(t *testing.T) {
	tests := []struct {
		name     string
		manifest *backup.Manifest
		want     string
	}{
		{
			name:     "multiple targets",
			manifest: &backup.Manifest{ProxmoxTargets: []string{"pve", "ceph"}, ProxmoxVersion: " 8.0.4 "},
			want:     "pve+ceph",
		},
		{
			name:     "with proxmox targets",
			manifest: &backup.Manifest{ProxmoxTargets: []string{"pve", "ceph"}},
			want:     "pve+ceph",
		},
		{
			name:     "single target",
			manifest: &backup.Manifest{ProxmoxTargets: []string{"pbs"}},
			want:     "pbs",
		},
		{
			name:     "fallback to proxmox type",
			manifest: &backup.Manifest{ProxmoxTargets: []string{}, ProxmoxType: "pve"},
			want:     "pve",
		},
		{
			name:     "unknown when empty",
			manifest: &backup.Manifest{ProxmoxTargets: []string{}, ProxmoxType: ""},
			want:     "unknown target",
		},
		{
			name:     "nil targets fallback",
			manifest: &backup.Manifest{ProxmoxTargets: nil, ProxmoxType: "pbs"},
			want:     "pbs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTargets(tt.manifest)
			if got != tt.want {
				t.Errorf("formatTargets() = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestFormatTargetSummary(t *testing.T) {
	tests := []struct {
		name     string
		manifest *backup.Manifest
		want     string
	}{
		{
			name: "full info with cluster",
			manifest: &backup.Manifest{
				ProxmoxTargets: []string{"pve"},
				ProxmoxVersion: "8.0.4",
				ClusterMode:    "cluster",
			},
			want: "pve v8.0.4 (cluster)",
		},
		{
			name: "standalone mode",
			manifest: &backup.Manifest{
				ProxmoxTargets: []string{"pbs"},
				ProxmoxVersion: "3.1.2",
				ClusterMode:    "standalone",
			},
			want: "pbs v3.1.2 (standalone)",
		},
		{
			name: "version with v prefix",
			manifest: &backup.Manifest{
				ProxmoxTargets: []string{"pve"},
				ProxmoxVersion: "v8.0.4",
				ClusterMode:    "",
			},
			want: "pve v8.0.4",
		},
		{
			name: "unknown version",
			manifest: &backup.Manifest{
				ProxmoxTargets: []string{"pve"},
				ProxmoxVersion: "",
				ClusterMode:    "",
			},
			want: "pve vunknown",
		},
		{
			name: "empty cluster mode ignored",
			manifest: &backup.Manifest{
				ProxmoxTargets: []string{"pbs"},
				ProxmoxVersion: "3.0",
				ClusterMode:    "",
			},
			want: "pbs v3.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTargetSummary(tt.manifest)
			if got != tt.want {
				t.Errorf("formatTargetSummary() = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestStatusFromManifest(t *testing.T) {
	tests := []struct {
		name     string
		manifest *backup.Manifest
		want     string
	}{
		{
			name:     "age encrypted with whitespace",
			manifest: &backup.Manifest{EncryptionMode: "  age "},
			want:     "encrypted",
		},
		{
			name:     "age encrypted",
			manifest: &backup.Manifest{EncryptionMode: "age"},
			want:     "encrypted",
		},
		{
			name:     "AGE uppercase",
			manifest: &backup.Manifest{EncryptionMode: "AGE"},
			want:     "encrypted",
		},
		{
			name:     "Age mixed case",
			manifest: &backup.Manifest{EncryptionMode: "Age"},
			want:     "encrypted",
		},
		{
			name:     "none encryption",
			manifest: &backup.Manifest{EncryptionMode: "none"},
			want:     "plain",
		},
		{
			name:     "empty encryption",
			manifest: &backup.Manifest{EncryptionMode: ""},
			want:     "plain",
		},
		{
			name:     "other value",
			manifest: &backup.Manifest{EncryptionMode: "gpg"},
			want:     "plain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := statusFromManifest(tt.manifest)
			if got != tt.want {
				t.Errorf("statusFromManifest() = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestIsLocalFilesystemPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"absolute unix path", "/var/backup", true},
		{"absolute with spaces", "/var/my backup", true},
		{"absolute with colon", "/mnt/data:foo", true},
		{"absolute with surrounding spaces", "  /mnt/backup  ", true},
		{"rclone remote", "gdrive:backups", false},
		{"rclone remote with path", "s3:bucket/folder", false},
		{"empty string", "", false},
		{"whitespace only", "   ", false},
		{"relative path", "backup/local", false},
		{"dot relative", "./backup", false},
		{"windows absolute", "C:/backup", false},          // not unix absolute
		{"rclone local remote", "local:/mnt/data", false}, // has colon
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isLocalFilesystemPath(tt.path)
			if got != tt.want {
				t.Errorf("isLocalFilesystemPath(%q) = %v; want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestFormatClusterMode(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"cluster", "cluster"},
		{"CLUSTER", "cluster"},
		{"Cluster", "cluster"},
		{"standalone", "standalone"},
		{"STANDALONE", "standalone"},
		{"  cluster  ", "cluster"},
		{"", ""},
		{"unknown", ""},
		{"single", ""},
		{"ha", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := formatClusterMode(tt.input)
			if got != tt.want {
				t.Errorf("formatClusterMode(%q) = %q; want %q", tt.input, got, tt.want)
			}
		})
	}
}
