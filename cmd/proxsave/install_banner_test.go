package main

import (
	"errors"
	"testing"
)

// TestInstallBanner pins F12-02: the pure installBanner classifier and its
// interplay with wrapInstallError/isInstallAbortedError. An honest ctx-cancel
// wrapped by wrapInstallError must classify as an abort and yield the
// "Installation aborted" banner (installBannerAborted), never "Installation
// failed". This locks the reachable tail-guard contract: title AND level are
// asserted together on installBanner's return tuple (not the rendered footer),
// so the completed/aborted/failed switch cannot silently drift.
func TestInstallBanner(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantTitle   string
		wantLevel   installBannerLevel
		wantAborted bool
	}{
		{
			name:        "nil is completed",
			err:         nil,
			wantTitle:   "Installation completed",
			wantLevel:   installBannerCompleted,
			wantAborted: false,
		},
		{
			name:        "wrapped interactive abort is aborted",
			err:         wrapInstallError(errInteractiveAborted),
			wantTitle:   "Installation aborted",
			wantLevel:   installBannerAborted,
			wantAborted: true,
		},
		{
			name:        "plain error is failed",
			err:         errors.New("boom"),
			wantTitle:   "Installation failed",
			wantLevel:   installBannerFailed,
			wantAborted: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isInstallAbortedError(tt.err); got != tt.wantAborted {
				t.Fatalf("isInstallAbortedError(%v) = %v, want %v", tt.err, got, tt.wantAborted)
			}
			title, level := installBanner(tt.err)
			if title != tt.wantTitle || level != tt.wantLevel {
				t.Fatalf("installBanner(%v) = (%q, %v), want (%q, %v)", tt.err, title, level, tt.wantTitle, tt.wantLevel)
			}
		})
	}
}
