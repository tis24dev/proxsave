package environment

import (
	"errors"
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
	})
}
