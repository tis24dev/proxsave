package version

import (
	"runtime/debug"
	"testing"
)

func withPatchedGlobals(t *testing.T, versionValue string, reader func() (*debug.BuildInfo, bool)) {
	t.Helper()
	originalVersion := Version
	originalReader := readBuildInfo

	Version = versionValue
	if reader != nil {
		readBuildInfo = reader
	} else {
		readBuildInfo = originalReader
	}

	t.Cleanup(func() {
		Version = originalVersion
		readBuildInfo = originalReader
	})
}

func TestStringPrefersInjectedVersion(t *testing.T) {
	withPatchedGlobals(t, " v1.2.3 ", func() (*debug.BuildInfo, bool) {
		t.Fatalf("unexpected call to readBuildInfo when version is set")
		return nil, false
	})

	got := String()
	if got != "1.2.3" {
		t.Fatalf("String() = %q, want %q", got, "1.2.3")
	}
}

func TestStringUsesBuildInfoWhenVersionEmpty(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "v2.3.4"}}

	withPatchedGlobals(t, "", func() (*debug.BuildInfo, bool) {
		return info, true
	})

	got := String()
	if got != "2.3.4" {
		t.Fatalf("String() = %q, want %q", got, "2.3.4")
	}
}

func TestStringFallsBackToPlaceholder(t *testing.T) {
	testCases := []struct {
		name   string
		reader func() (*debug.BuildInfo, bool)
	}{
		{
			name: "no build info",
			reader: func() (*debug.BuildInfo, bool) {
				return nil, false
			},
		},
		{
			name: "empty version",
			reader: func() (*debug.BuildInfo, bool) {
				return &debug.BuildInfo{Main: debug.Module{Version: ""}}, true
			},
		},
		{
			name: "devel version",
			reader: func() (*debug.BuildInfo, bool) {
				return &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			withPatchedGlobals(t, "", tc.reader)

			if got := String(); got != "0.0.0-dev" {
				t.Fatalf("String() = %q, want %q", got, "0.0.0-dev")
			}
		})
	}
}
