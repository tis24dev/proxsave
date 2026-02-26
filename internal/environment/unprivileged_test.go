package environment

import (
	"errors"
	"os"
	"strings"
	"testing"
)

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
	t.Run("shifted uid/gid maps", func(t *testing.T) {
		setValue(t, &readFileFunc, func(path string) ([]byte, error) {
			switch path {
			case selfUIDMapPath:
				return []byte("0 100000 65536\n"), nil
			case selfGIDMapPath:
				return []byte("0 100000 65536\n"), nil
			case systemdContainerPath:
				return []byte("lxc\n"), nil
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
	})

	t.Run("privileged mapping", func(t *testing.T) {
		setValue(t, &readFileFunc, func(path string) ([]byte, error) {
			switch path {
			case selfUIDMapPath:
				return []byte("0 0 4294967295\n"), nil
			case selfGIDMapPath:
				return []byte("0 0 4294967295\n"), nil
			case systemdContainerPath:
				return []byte("lxc\n"), nil
			default:
				return nil, errors.New("not found")
			}
		})

		info := DetectUnprivilegedContainer()
		if info.Detected {
			t.Fatalf("Detected=true, want false (details=%q)", info.Details)
		}
		if !info.UIDMap.OK || info.UIDMap.Outside0 != 0 {
			t.Fatalf("unexpected UIDMap: ok=%v outside0=%d details=%q", info.UIDMap.OK, info.UIDMap.Outside0, info.Details)
		}
		if !info.GIDMap.OK || info.GIDMap.Outside0 != 0 {
			t.Fatalf("unexpected GIDMap: ok=%v outside0=%d details=%q", info.GIDMap.OK, info.GIDMap.Outside0, info.Details)
		}
	})

	t.Run("maps unavailable", func(t *testing.T) {
		setValue(t, &readFileFunc, func(path string) ([]byte, error) {
			switch path {
			case selfUIDMapPath, selfGIDMapPath, systemdContainerPath:
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
		if !strings.Contains(info.Details, "container=unavailable(err=not found)") {
			t.Fatalf("expected container unavailable detail, got %q", info.Details)
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
		if !strings.Contains(info.Details, "container=empty") {
			t.Fatalf("expected empty container detail, got %q", info.Details)
		}
	})
}
