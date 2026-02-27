package environment

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

type fakeFileInfo struct {
	name string
	dir  bool
}

func (fi fakeFileInfo) Name() string       { return fi.name }
func (fi fakeFileInfo) Size() int64        { return 0 }
func (fi fakeFileInfo) Mode() os.FileMode  { return 0o644 }
func (fi fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fi fakeFileInfo) IsDir() bool        { return fi.dir }
func (fi fakeFileInfo) Sys() any           { return nil }

func TestParseIDMapOutsideZero(t *testing.T) {
	t.Run("privileged mapping", func(t *testing.T) {
		outside0, length, ok := parseIDMapOutsideZero("0          0 4294967295\n")
		if !ok {
			t.Fatal("expected ok=true")
		}
		if outside0 != 0 {
			t.Fatalf("outside0=%d, want 0", outside0)
		}
		if length != 4294967295 {
			t.Fatalf("length=%d, want 4294967295", length)
		}
	})

	t.Run("unprivileged mapping", func(t *testing.T) {
		outside0, length, ok := parseIDMapOutsideZero("0 100000 65536\n")
		if !ok {
			t.Fatal("expected ok=true")
		}
		if outside0 != 100000 {
			t.Fatalf("outside0=%d, want 100000", outside0)
		}
		if length != 65536 {
			t.Fatalf("length=%d, want 65536", length)
		}
	})

	t.Run("multiple lines", func(t *testing.T) {
		outside0, length, ok := parseIDMapOutsideZero("0 100000 65536\n1000 0 1\n")
		if !ok {
			t.Fatal("expected ok=true")
		}
		if outside0 != 100000 || length != 65536 {
			t.Fatalf("got outside0=%d length=%d, want outside0=100000 length=65536", outside0, length)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		_, _, ok := parseIDMapOutsideZero("not a map\n")
		if ok {
			t.Fatal("expected ok=false")
		}
	})
}

func TestDetectUnprivilegedContainer(t *testing.T) {
	setValue(t, &getEUIDFunc, func() int { return 0 })
	setValue(t, &lookupEnvFunc, func(string) (string, bool) { return "", false })
	setValue(t, &statFunc, func(string) (os.FileInfo, error) { return nil, os.ErrNotExist })

	t.Run("shifted uid/gid maps", func(t *testing.T) {
		setValue(t, &readFileFunc, func(path string) ([]byte, error) {
			switch path {
			case selfUIDMapPath:
				return []byte("0 100000 65536\n"), nil
			case selfGIDMapPath:
				return []byte("0 100000 65536\n"), nil
			case systemdContainerPath:
				return []byte("lxc\n"), nil
			case proc1CgroupPath, selfCgroupPath:
				return nil, os.ErrNotExist
			default:
				return nil, errors.New("not found")
			}
		})

		info := DetectUnprivilegedContainer()
		if !info.Detected {
			t.Fatalf("Detected=false, want true (details=%q)", info.Details)
		}
		if !info.UIDMap.OK || info.UIDMap.Outside0 != 100000 {
			t.Fatalf("unexpected UIDMap: ok=%v outside0=%d details=%q", info.UIDMap.OK, info.UIDMap.Outside0, info.Details)
		}
		if !info.GIDMap.OK || info.GIDMap.Outside0 != 100000 {
			t.Fatalf("unexpected GIDMap: ok=%v outside0=%d details=%q", info.GIDMap.OK, info.GIDMap.Outside0, info.Details)
		}
		if !info.SystemdContainer.OK || info.SystemdContainer.Value != "lxc" {
			t.Fatalf("unexpected SystemdContainer: ok=%v value=%q details=%q", info.SystemdContainer.OK, info.SystemdContainer.Value, info.Details)
		}
		if !strings.Contains(info.Details, "uid_map=0->100000") {
			t.Fatalf("expected uid_map details, got %q", info.Details)
		}
		if !strings.Contains(info.Details, "gid_map=0->100000") {
			t.Fatalf("expected gid_map details, got %q", info.Details)
		}
		if !strings.Contains(info.Details, "container=lxc") {
			t.Fatalf("expected container details, got %q", info.Details)
		}
		if !strings.Contains(info.Details, "container_src=systemd") {
			t.Fatalf("expected container_src details, got %q", info.Details)
		}
	})

	t.Run("privileged mapping (but container detected)", func(t *testing.T) {
		setValue(t, &readFileFunc, func(path string) ([]byte, error) {
			switch path {
			case selfUIDMapPath:
				return []byte("0 0 4294967295\n"), nil
			case selfGIDMapPath:
				return []byte("0 0 4294967295\n"), nil
			case systemdContainerPath:
				return []byte("lxc\n"), nil
			case proc1CgroupPath, selfCgroupPath:
				return nil, os.ErrNotExist
			default:
				return nil, errors.New("not found")
			}
		})

		info := DetectUnprivilegedContainer()
		if !info.Detected {
			t.Fatalf("Detected=false, want true (details=%q)", info.Details)
		}
		if !info.UIDMap.OK || info.UIDMap.Outside0 != 0 {
			t.Fatalf("unexpected UIDMap: ok=%v outside0=%d details=%q", info.UIDMap.OK, info.UIDMap.Outside0, info.Details)
		}
		if !info.GIDMap.OK || info.GIDMap.Outside0 != 0 {
			t.Fatalf("unexpected GIDMap: ok=%v outside0=%d details=%q", info.GIDMap.OK, info.GIDMap.Outside0, info.Details)
		}
		if info.ContainerRuntime != "lxc" || info.ContainerSource != "systemd" {
			t.Fatalf("unexpected container: runtime=%q source=%q details=%q", info.ContainerRuntime, info.ContainerSource, info.Details)
		}
		if !strings.Contains(info.Details, "container=lxc") {
			t.Fatalf("expected container details, got %q", info.Details)
		}
	})

	t.Run("maps unavailable", func(t *testing.T) {
		setValue(t, &readFileFunc, func(path string) ([]byte, error) {
			switch path {
			case selfUIDMapPath, selfGIDMapPath, systemdContainerPath, proc1CgroupPath, selfCgroupPath:
				return nil, os.ErrNotExist
			default:
				return nil, errors.New("not found")
			}
		})

		info := DetectUnprivilegedContainer()
		if info.Detected {
			t.Fatalf("Detected=true, want false (details=%q)", info.Details)
		}
		if info.UIDMap.OK || info.GIDMap.OK {
			t.Fatalf("expected UID/GID maps to be unavailable (details=%q)", info.Details)
		}
		if info.SystemdContainer.OK {
			t.Fatalf("expected container to be unavailable (details=%q)", info.Details)
		}
		if !strings.Contains(info.Details, "uid_map=unavailable(err=not found)") {
			t.Fatalf("expected uid_map unavailable detail, got %q", info.Details)
		}
		if !strings.Contains(info.Details, "gid_map=unavailable(err=not found)") {
			t.Fatalf("expected gid_map unavailable detail, got %q", info.Details)
		}
		if !strings.Contains(info.Details, "container=none") {
			t.Fatalf("expected container none detail, got %q", info.Details)
		}
	})

	t.Run("unparseable uid_map falls back to gid_map", func(t *testing.T) {
		setValue(t, &readFileFunc, func(path string) ([]byte, error) {
			switch path {
			case selfUIDMapPath:
				return []byte("not a map\n"), nil
			case selfGIDMapPath:
				return []byte("0 100000 65536\n"), nil
			case systemdContainerPath:
				return []byte("\n"), nil
			case proc1CgroupPath, selfCgroupPath:
				return nil, os.ErrNotExist
			default:
				return nil, errors.New("not found")
			}
		})

		info := DetectUnprivilegedContainer()
		if !info.Detected {
			t.Fatalf("Detected=false, want true (details=%q)", info.Details)
		}
		if info.UIDMap.OK || info.UIDMap.ParseError == "" {
			t.Fatalf("expected UIDMap parse error, got ok=%v parseErr=%q (details=%q)", info.UIDMap.OK, info.UIDMap.ParseError, info.Details)
		}
		if !info.GIDMap.OK || info.GIDMap.Outside0 != 100000 {
			t.Fatalf("unexpected GIDMap: ok=%v outside0=%d (details=%q)", info.GIDMap.OK, info.GIDMap.Outside0, info.Details)
		}
		if !strings.Contains(info.Details, "uid_map=unparseable") {
			t.Fatalf("expected uid_map unparseable detail, got %q", info.Details)
		}
		if !strings.Contains(info.Details, "container=none") {
			t.Fatalf("expected none container detail, got %q", info.Details)
		}
	})

	t.Run("privileged host mapping (no container)", func(t *testing.T) {
		setValue(t, &readFileFunc, func(path string) ([]byte, error) {
			switch path {
			case selfUIDMapPath:
				return []byte("0 0 4294967295\n"), nil
			case selfGIDMapPath:
				return []byte("0 0 4294967295\n"), nil
			case systemdContainerPath, proc1CgroupPath, selfCgroupPath:
				return nil, os.ErrNotExist
			default:
				return nil, errors.New("not found")
			}
		})

		info := DetectUnprivilegedContainer()
		if info.Detected {
			t.Fatalf("Detected=true, want false (details=%q)", info.Details)
		}
		if info.ContainerRuntime != "" || info.ContainerSource != "" {
			t.Fatalf("expected empty container runtime/source, got runtime=%q source=%q (details=%q)", info.ContainerRuntime, info.ContainerSource, info.Details)
		}
		if !strings.Contains(info.Details, "container=none") {
			t.Fatalf("expected container none detail, got %q", info.Details)
		}
	})

	t.Run("docker marker implies restricted context", func(t *testing.T) {
		setValue(t, &statFunc, func(path string) (os.FileInfo, error) {
			if path == dockerMarkerPath {
				return fakeFileInfo{name: "dockerenv"}, nil
			}
			return nil, os.ErrNotExist
		})
		setValue(t, &readFileFunc, func(path string) ([]byte, error) {
			switch path {
			case selfUIDMapPath:
				return []byte("0 0 4294967295\n"), nil
			case selfGIDMapPath:
				return []byte("0 0 4294967295\n"), nil
			case systemdContainerPath, proc1CgroupPath, selfCgroupPath:
				return nil, os.ErrNotExist
			default:
				return nil, errors.New("not found")
			}
		})

		info := DetectUnprivilegedContainer()
		if !info.Detected {
			t.Fatalf("Detected=false, want true (details=%q)", info.Details)
		}
		if info.ContainerRuntime != "docker" || info.ContainerSource != "marker" {
			t.Fatalf("unexpected container: runtime=%q source=%q details=%q", info.ContainerRuntime, info.ContainerSource, info.Details)
		}
	})

	t.Run("cgroup hint implies restricted context", func(t *testing.T) {
		setValue(t, &readFileFunc, func(path string) ([]byte, error) {
			switch path {
			case selfUIDMapPath:
				return []byte("0 0 4294967295\n"), nil
			case selfGIDMapPath:
				return []byte("0 0 4294967295\n"), nil
			case systemdContainerPath:
				return nil, os.ErrNotExist
			case proc1CgroupPath:
				return []byte("0::/docker/abcdef\n"), nil
			case selfCgroupPath:
				return nil, os.ErrNotExist
			default:
				return nil, errors.New("not found")
			}
		})

		info := DetectUnprivilegedContainer()
		if !info.Detected {
			t.Fatalf("Detected=false, want true (details=%q)", info.Details)
		}
		if info.ContainerRuntime != "docker" || info.ContainerSource != "cgroup" {
			t.Fatalf("unexpected container: runtime=%q source=%q details=%q", info.ContainerRuntime, info.ContainerSource, info.Details)
		}
	})

	t.Run("env container implies restricted context", func(t *testing.T) {
		setValue(t, &lookupEnvFunc, func(key string) (string, bool) {
			if key == "container" {
				return "podman", true
			}
			return "", false
		})
		setValue(t, &readFileFunc, func(path string) ([]byte, error) {
			switch path {
			case selfUIDMapPath:
				return []byte("0 0 4294967295\n"), nil
			case selfGIDMapPath:
				return []byte("0 0 4294967295\n"), nil
			case systemdContainerPath, proc1CgroupPath, selfCgroupPath:
				return nil, os.ErrNotExist
			default:
				return nil, errors.New("not found")
			}
		})

		info := DetectUnprivilegedContainer()
		if !info.Detected {
			t.Fatalf("Detected=false, want true (details=%q)", info.Details)
		}
		if info.ContainerRuntime != "podman" || info.ContainerSource != "env" {
			t.Fatalf("unexpected container: runtime=%q source=%q details=%q", info.ContainerRuntime, info.ContainerSource, info.Details)
		}
	})

	t.Run("non-root implies restricted context", func(t *testing.T) {
		setValue(t, &getEUIDFunc, func() int { return 1000 })
		setValue(t, &readFileFunc, func(path string) ([]byte, error) {
			switch path {
			case selfUIDMapPath:
				return []byte("0 0 4294967295\n"), nil
			case selfGIDMapPath:
				return []byte("0 0 4294967295\n"), nil
			case systemdContainerPath, proc1CgroupPath, selfCgroupPath:
				return nil, os.ErrNotExist
			default:
				return nil, errors.New("not found")
			}
		})

		info := DetectUnprivilegedContainer()
		if !info.Detected {
			t.Fatalf("Detected=false, want true (details=%q)", info.Details)
		}
		if info.EUID != 1000 {
			t.Fatalf("EUID=%d, want 1000 (details=%q)", info.EUID, info.Details)
		}
	})
}
