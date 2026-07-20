// Package main contains the proxsave command entrypoint.
package main

import (
	"reflect"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
)

// TestEnabledNotifyChannels is the Fase-2C CLIENT contract for the authoritative enabled-set
// the daemon sends to the server: the notification channels (email/telegram/gotify/webhook)
// that are enabled, lowercased and sorted. Metrics/Prometheus is a sink, not a notification
// channel, so it is excluded; the result is always a non-nil (possibly empty) slice.
func TestEnabledNotifyChannels(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want []string
	}{
		{
			name: "email+gotify enabled -> sorted",
			cfg:  config.Config{EmailEnabled: true, GotifyEnabled: true},
			want: []string{"email", "gotify"},
		},
		{
			name: "none enabled -> empty non-nil slice",
			cfg:  config.Config{},
			want: []string{},
		},
		{
			name: "metrics only -> excluded (empty)",
			cfg:  config.Config{MetricsEnabled: true},
			want: []string{},
		},
		{
			name: "all four -> sorted",
			cfg:  config.Config{EmailEnabled: true, TelegramEnabled: true, GotifyEnabled: true, WebhookEnabled: true},
			want: []string{"email", "gotify", "telegram", "webhook"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := enabledNotifyChannels(&tc.cfg)
			if got == nil {
				t.Fatalf("enabledNotifyChannels must return a non-nil slice (empty -> the server pauses all)")
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("enabledNotifyChannels = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSelfURLsResolvesNotify is the Fase-2C CLIENT contract for self-mode per-channel notify
// checks: selfURLs resolves checks["notify-<ch>"] from a full URL when set, else assembled
// from the check ID via the ping endpoint; a channel with neither is absent from the map.
func TestSelfURLsResolvesNotify(t *testing.T) {
	cfg := config.Config{
		HealthcheckMode:              "self",
		HealthcheckPingEndpoint:      "https://hc-ping.com",
		HealthcheckNotifyEmailID:     "e",                // assembled: <endpoint>/e
		HealthcheckNotifyTelegramURL: "https://x/ping/t", // full URL wins verbatim
	}
	d := &daemon{cfg: &cfg}
	_, _, checks := d.selfURLs()

	if got := checks[health.CheckKeyNotify("email")]; got != "https://hc-ping.com/e" {
		t.Fatalf("notify-email = %q, want https://hc-ping.com/e (assembled from ID)", got)
	}
	if got := checks[health.CheckKeyNotify("telegram")]; got != "https://x/ping/t" {
		t.Fatalf("notify-telegram = %q, want https://x/ping/t (full URL)", got)
	}
	// A channel with neither URL nor ID resolves to nothing -> absent from the map.
	if _, ok := checks[health.CheckKeyNotify("gotify")]; ok {
		t.Fatalf("notify-gotify must be absent when neither URL nor ID is configured")
	}
	if _, ok := checks[health.CheckKeyNotify("webhook")]; ok {
		t.Fatalf("notify-webhook must be absent when neither URL nor ID is configured")
	}
}
