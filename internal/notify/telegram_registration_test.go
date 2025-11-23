package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tis24dev/proxmox-backup/internal/logging"
	"github.com/tis24dev/proxmox-backup/internal/types"
)

func TestCheckTelegramRegistrationMissingServerID(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	status := CheckTelegramRegistration(context.Background(), "https://central.test", "", logger)

	if status.Code != 0 || status.Error == nil {
		t.Fatalf("expected missing server ID error, got %+v", status)
	}
}

func TestCheckTelegramRegistrationResponses(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	cases := []struct {
		name        string
		statusCode  int
		expectCode  int
		expectError bool
	}{
		{"200-ok", http.StatusOK, 200, false},
		{"403-first-comm", http.StatusForbidden, 403, true},
		{"409-missing-reg", http.StatusConflict, 409, true},
		{"422-invalid", http.StatusUnprocessableEntity, 422, true},
		{"500-unexpected", http.StatusInternalServerError, 500, true},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.name))
			}))
			defer server.Close()

			status := CheckTelegramRegistration(context.Background(), server.URL, "server-123", logger)
			if status.Code != tt.expectCode {
				t.Fatalf("Code=%d, want %d", status.Code, tt.expectCode)
			}
			if tt.expectError && status.Error == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tt.expectError && status.Error != nil {
				t.Fatalf("unexpected error: %v", status.Error)
			}
		})
	}
}
