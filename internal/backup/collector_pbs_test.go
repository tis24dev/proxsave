package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxmox-backup/internal/pbs"
)

func TestGetDatastoreListSuccessWithOverrides(t *testing.T) {
	deps := CollectorDeps{
		LookPath: func(cmd string) (string, error) {
			if cmd != "proxmox-backup-manager" {
				t.Fatalf("unexpected lookPath for %s", cmd)
			}
			return "/usr/bin/" + cmd, nil
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			t.Helper()
			if name != "proxmox-backup-manager" {
				t.Fatalf("unexpected command %s", name)
			}
			expected := []string{"datastore", "list", "--output-format=json"}
			if len(args) != len(expected) {
				t.Fatalf("unexpected args: %v", args)
			}
			for i, want := range expected {
				if args[i] != want {
					t.Fatalf("unexpected arg[%d]=%s", i, args[i])
				}
			}
			return []byte(`[{"name":"primary","path":"/data/primary","comment":"main"},{"name":"","path":"/ignored"}]`), nil
		},
	}

	collector := newTestCollectorWithDeps(t, deps)
	collector.config.PBSDatastorePaths = []string{" /custom/store ", "/data/primary", "/weird/path/??"}

	datastores, err := collector.getDatastoreList(context.Background())
	if err != nil {
		t.Fatalf("getDatastoreList failed: %v", err)
	}
	if len(datastores) != 3 {
		t.Fatalf("expected 3 datastores, got %d: %+v", len(datastores), datastores)
	}
	if datastores[0].Name != "primary" || datastores[0].Path != "/data/primary" {
		t.Fatalf("unexpected first datastore: %+v", datastores[0])
	}
	if datastores[1].Name != "store" || datastores[1].Path != "/custom/store" {
		t.Fatalf("override datastore mismatch: %+v", datastores[1])
	}
	if datastores[2].Name != "datastore_3" || datastores[2].Path != "/weird/path/??" {
		t.Fatalf("invalid override fallback mismatch: %+v", datastores[2])
	}
	if datastores[2].Comment != "configured via PBS_DATASTORE_PATH" {
		t.Fatalf("expected override comment, got %q", datastores[2].Comment)
	}
}

func TestGetDatastoreListContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	collector := newTestCollector(t)
	if _, err := collector.getDatastoreList(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestGetDatastoreListNoCLI(t *testing.T) {
	collector := newTestCollectorWithDeps(t, CollectorDeps{
		LookPath: func(string) (string, error) {
			return "", fmt.Errorf("missing")
		},
	})
	datastores, err := collector.getDatastoreList(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(datastores) != 0 {
		t.Fatalf("expected no datastores, got %v", datastores)
	}
}

func TestGetDatastoreListCommandError(t *testing.T) {
	collector := newTestCollectorWithDeps(t, CollectorDeps{
		LookPath: func(cmd string) (string, error) {
			return "/usr/bin/" + cmd, nil
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return nil, fmt.Errorf("command failed")
		},
	})

	_, err := collector.getDatastoreList(context.Background())
	if err == nil || !strings.Contains(err.Error(), "datastore list failed") {
		t.Fatalf("expected datastore list error, got %v", err)
	}
}

func TestGetDatastoreListBadJSON(t *testing.T) {
	collector := newTestCollectorWithDeps(t, CollectorDeps{
		LookPath: func(cmd string) (string, error) {
			return "/usr/bin/" + cmd, nil
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return []byte("not-json"), nil
		},
	})

	_, err := collector.getDatastoreList(context.Background())
	if err == nil || !strings.Contains(err.Error(), "failed to parse datastore list JSON") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestHasTapeSupportContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	collector := newTestCollectorWithDeps(t, CollectorDeps{
		Stat: func(string) (os.FileInfo, error) {
			t.Fatal("stat should not be called after context cancellation")
			return nil, nil
		},
	})
	if _, err := collector.hasTapeSupport(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestHasTapeSupportConfigFilePresent(t *testing.T) {
	collector := newTestCollectorWithDeps(t, CollectorDeps{
		Stat: func(string) (os.FileInfo, error) {
			return fakeFileInfo{}, nil
		},
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("runCommand should not be called when tape.cfg exists")
			return nil, nil
		},
	})
	hasTape, err := collector.hasTapeSupport(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasTape {
		t.Fatal("expected tape support when tape.cfg exists")
	}
}

func TestHasTapeSupportNoCLI(t *testing.T) {
	collector := newTestCollectorWithDeps(t, CollectorDeps{
		Stat: func(string) (os.FileInfo, error) {
			return nil, os.ErrNotExist
		},
		LookPath: func(string) (string, error) {
			return "", fmt.Errorf("not found")
		},
	})
	hasTape, err := collector.hasTapeSupport(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasTape {
		t.Fatal("expected tape support disabled when CLI missing")
	}
}

func TestHasTapeSupportCommandError(t *testing.T) {
	collector := newTestCollectorWithDeps(t, CollectorDeps{
		Stat: func(string) (os.FileInfo, error) {
			return nil, os.ErrNotExist
		},
		LookPath: func(cmd string) (string, error) {
			return "/usr/bin/" + cmd, nil
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return nil, fmt.Errorf("drive list failed")
		},
	})
	_, err := collector.hasTapeSupport(context.Background())
	if err == nil || !strings.Contains(err.Error(), "proxmox-tape drive list failed") {
		t.Fatalf("expected proxmox-tape error, got %v", err)
	}
}

func TestHasTapeSupportCommandErrorAfterContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	collector := newTestCollectorWithDeps(t, CollectorDeps{
		Stat: func(string) (os.FileInfo, error) {
			return nil, os.ErrNotExist
		},
		LookPath: func(cmd string) (string, error) {
			return "/usr/bin/" + cmd, nil
		},
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			cancel()
			return nil, fmt.Errorf("failure")
		},
	})
	if _, err := collector.hasTapeSupport(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestHasTapeSupportNoDrives(t *testing.T) {
	collector := newTestCollectorWithDeps(t, CollectorDeps{
		Stat: func(string) (os.FileInfo, error) {
			return nil, os.ErrNotExist
		},
		LookPath: func(cmd string) (string, error) {
			return "/usr/bin/" + cmd, nil
		},
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("   \n"), nil
		},
	})
	hasTape, err := collector.hasTapeSupport(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasTape {
		t.Fatal("expected false when no drives are reported")
	}
}

func TestHasTapeSupportHasDrives(t *testing.T) {
	collector := newTestCollectorWithDeps(t, CollectorDeps{
		Stat: func(string) (os.FileInfo, error) {
			return nil, os.ErrNotExist
		},
		LookPath: func(cmd string) (string, error) {
			return "/usr/bin/" + cmd, nil
		},
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("drive1\n"), nil
		},
	})
	hasTape, err := collector.hasTapeSupport(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasTape {
		t.Fatal("expected true when drives are reported")
	}
}

func TestCollectDatastoreNamespacesSuccess(t *testing.T) {
	stubListNamespaces(t, func(name, path string) ([]pbs.Namespace, bool, error) {
		if name != "store1" || path != "/fake" {
			t.Fatalf("unexpected datastore %s %s", name, path)
		}
		return []pbs.Namespace{
			{Ns: "", Path: "/fake"},
			{Ns: "child", Path: "/fake/child"},
		}, true, nil
	})

	collector := newTestCollectorWithDeps(t, CollectorDeps{})
	dsDir := filepath.Join(collector.tempDir, "datastores")
	if err := os.MkdirAll(dsDir, 0o755); err != nil {
		t.Fatalf("failed to create datastore dir: %v", err)
	}

	ds := pbsDatastore{Name: "store1", Path: "/fake"}
	if err := collector.collectDatastoreNamespaces(ds, dsDir); err != nil {
		t.Fatalf("collectDatastoreNamespaces failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dsDir, "store1_namespaces.json"))
	if err != nil {
		t.Fatalf("namespaces file not created: %v", err)
	}

	var namespaces []pbs.Namespace
	if err := json.Unmarshal(data, &namespaces); err != nil {
		t.Fatalf("failed to decode namespaces: %v", err)
	}
	if len(namespaces) != 2 || namespaces[1].Ns != "child" {
		t.Fatalf("unexpected namespaces: %+v", namespaces)
	}
}

func TestCollectDatastoreNamespacesError(t *testing.T) {
	stubListNamespaces(t, func(string, string) ([]pbs.Namespace, bool, error) {
		return nil, false, fmt.Errorf("boom")
	})

	collector := newTestCollectorWithDeps(t, CollectorDeps{})
	dsDir := filepath.Join(collector.tempDir, "datastores")
	if err := os.MkdirAll(dsDir, 0o755); err != nil {
		t.Fatalf("failed to create datastore dir: %v", err)
	}

	err := collector.collectDatastoreNamespaces(pbsDatastore{Name: "store1"}, dsDir)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected error from list namespaces, got %v", err)
	}
}

func TestCollectDatastoreConfigsDryRun(t *testing.T) {
	stubListNamespaces(t, func(string, string) ([]pbs.Namespace, bool, error) {
		return []pbs.Namespace{{Ns: ""}}, false, nil
	})

	collector := newTestCollectorWithDeps(t, CollectorDeps{
		LookPath: func(cmd string) (string, error) {
			if cmd != "proxmox-backup-manager" {
				return "", fmt.Errorf("unexpected command %s", cmd)
			}
			return "/usr/bin/" + cmd, nil
		},
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`{"status":"ok"}`), nil
		},
	})

	datastores := []pbsDatastore{{Name: "store1", Path: "/fake"}}
	if err := collector.collectDatastoreConfigs(context.Background(), datastores); err != nil {
		t.Fatalf("collectDatastoreConfigs failed: %v", err)
	}

	nsFile := filepath.Join(collector.tempDir, "datastores", "store1_namespaces.json")
	if _, err := os.Stat(nsFile); err != nil {
		t.Fatalf("expected namespaces file, got %v", err)
	}
}

func TestCollectUserConfigsWithTokens(t *testing.T) {
	collector := newTestCollectorWithDeps(t, CollectorDeps{
		LookPath: func(cmd string) (string, error) {
			if cmd != "proxmox-backup-manager" {
				return "", fmt.Errorf("unexpected command %s", cmd)
			}
			return "/usr/bin/" + cmd, nil
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			t.Helper()
			if name != "proxmox-backup-manager" {
				return nil, fmt.Errorf("unexpected command %s", name)
			}
			if len(args) != 4 || args[0] != "user" || args[1] != "list-tokens" {
				return nil, fmt.Errorf("unexpected args %v", args)
			}
			return []byte(`[{"tokenid":"mytoken"}]`), nil
		},
	})
	commandsDir := filepath.Join(collector.tempDir, "commands")
	if err := os.MkdirAll(commandsDir, 0o755); err != nil {
		t.Fatalf("failed to create commands dir: %v", err)
	}
	userList := `[{"userid":"user@pam"},{"userid":""}]`
	if err := os.WriteFile(filepath.Join(commandsDir, "user_list.json"), []byte(userList), 0o644); err != nil {
		t.Fatalf("failed to write user list: %v", err)
	}

	if err := collector.collectUserConfigs(context.Background()); err != nil {
		t.Fatalf("collectUserConfigs failed: %v", err)
	}

	tokensPath := filepath.Join(collector.tempDir, "users", "tokens.json")
	data, err := os.ReadFile(tokensPath)
	if err != nil {
		t.Fatalf("tokens.json not created: %v", err)
	}

	var aggregated map[string]json.RawMessage
	if err := json.Unmarshal(data, &aggregated); err != nil {
		t.Fatalf("failed to decode aggregated tokens: %v", err)
	}
	if len(aggregated) != 1 {
		t.Fatalf("expected 1 aggregated user, got %d", len(aggregated))
	}
	if _, ok := aggregated["user@pam"]; !ok {
		t.Fatalf("expected token entry for user@pam, got %v", aggregated)
	}
}

func TestCollectUserConfigsMissingUserList(t *testing.T) {
	collector := newTestCollectorWithDeps(t, CollectorDeps{})
	if err := collector.collectUserConfigs(context.Background()); err != nil {
		t.Fatalf("collectUserConfigs failed: %v", err)
	}

	tokensPath := filepath.Join(collector.tempDir, "users", "tokens.json")
	if _, err := os.Stat(tokensPath); !os.IsNotExist(err) {
		t.Fatalf("expected no tokens.json, got err=%v", err)
	}
}

func stubListNamespaces(t *testing.T, fn func(string, string) ([]pbs.Namespace, bool, error)) {
	t.Helper()
	orig := listNamespacesFunc
	listNamespacesFunc = fn
	t.Cleanup(func() {
		listNamespacesFunc = orig
	})
}

type fakeFileInfo struct{}

func (fakeFileInfo) Name() string       { return "fake" }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) Mode() os.FileMode  { return 0 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return false }
func (fakeFileInfo) Sys() interface{}   { return nil }
