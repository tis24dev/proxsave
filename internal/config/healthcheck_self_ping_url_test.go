package config

import "testing"

// The daemon assembles a self-mode ping URL from a check id, so a host configured with an
// id alone pings a real check. The dashboard's healthcheck screen has to reach the same
// answer, which is why the resolution lives here and not in either caller.
func TestHealthcheckSelfPingURL(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		key      string
		fullURL  string
		checkID  string
		want     string
	}{
		{"full url wins over everything", "https://hc-ping.com", "k", "https://example.test/ping/x", "id", "https://example.test/ping/x"},
		{"id against the default endpoint", "https://hc-ping.com", "", "", "abc-123", "https://hc-ping.com/abc-123"},
		{"id with a ping key", "https://hc-ping.com", "pk", "", "slug", "https://hc-ping.com/pk/slug"},
		{"trailing slashes on the endpoint are dropped", "https://hc-ping.com//", "", "", "abc", "https://hc-ping.com/abc"},
		{"id with no endpoint has nothing to ping", "", "", "", "abc", ""},
		{"neither url nor id", "https://hc-ping.com", "", "", "", ""},
		{"whitespace is not a url", "https://hc-ping.com", "", "   ", "abc", "https://hc-ping.com/abc"},
		{"whitespace is not an id", "https://hc-ping.com", "", "", "   ", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{HealthcheckPingEndpoint: tc.endpoint, HealthcheckPingKey: tc.key}
			if got := c.HealthcheckSelfPingURL(tc.fullURL, tc.checkID); got != tc.want {
				t.Fatalf("HealthcheckSelfPingURL(%q, %q) = %q, want %q", tc.fullURL, tc.checkID, got, tc.want)
			}
		})
	}
}
