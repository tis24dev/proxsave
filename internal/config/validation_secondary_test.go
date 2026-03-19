package config

import "testing"

func TestValidateRequiredSecondaryPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{name: "valid mount path", path: "/mnt/secondary"},
		{name: "valid subdirectory", path: "/mnt/secondary/log"},
		{name: "valid absolute with colon", path: "/mnt/data:archive"},
		{name: "empty", path: "", wantErr: "SECONDARY_PATH is required when SECONDARY_ENABLED=true"},
		{name: "relative", path: "relative/path", wantErr: "SECONDARY_PATH must be an absolute local filesystem path"},
		{name: "rclone remote", path: "gdrive:backups", wantErr: "SECONDARY_PATH must be an absolute local filesystem path"},
		{name: "host remote", path: "host:/backup", wantErr: "SECONDARY_PATH must be an absolute local filesystem path"},
		{name: "unc share", path: "//server/share", wantErr: "SECONDARY_PATH must be an absolute local filesystem path"},
		{name: "windows unc share", path: `\\server\share`, wantErr: "SECONDARY_PATH must be an absolute local filesystem path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateRequiredSecondaryPath(tt.path)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateRequiredSecondaryPath(%q) error = %v", tt.path, err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("ValidateRequiredSecondaryPath(%q) error = %v, want %q", tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestValidateOptionalSecondaryLogPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{name: "empty allowed", path: ""},
		{name: "valid path", path: "/mnt/secondary/log"},
		{name: "relative", path: "logs", wantErr: "SECONDARY_LOG_PATH must be an absolute local filesystem path"},
		{name: "remote style", path: "remote:/logs", wantErr: "SECONDARY_LOG_PATH must be an absolute local filesystem path"},
		{name: "unc share", path: "//server/logs", wantErr: "SECONDARY_LOG_PATH must be an absolute local filesystem path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateOptionalSecondaryLogPath(tt.path)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateOptionalSecondaryLogPath(%q) error = %v", tt.path, err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("ValidateOptionalSecondaryLogPath(%q) error = %v, want %q", tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestValidateOptionalSecondaryPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{name: "empty allowed", path: ""},
		{name: "valid path", path: "/mnt/secondary"},
		{name: "relative", path: "relative/path", wantErr: "SECONDARY_PATH must be an absolute local filesystem path"},
		{name: "remote style", path: "remote:/backup", wantErr: "SECONDARY_PATH must be an absolute local filesystem path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateOptionalSecondaryPath(tt.path)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateOptionalSecondaryPath(%q) error = %v", tt.path, err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("ValidateOptionalSecondaryPath(%q) error = %v, want %q", tt.path, err, tt.wantErr)
			}
		})
	}
}
