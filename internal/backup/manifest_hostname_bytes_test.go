package backup

import "testing"

// TestHostnameFromManifestBytes pins the helper the cloud attribution path reads its
// manifests through. Two pipelines wrote manifests in two formats, and a reader that
// understands only one of them reports "this archive names no host" for every archive
// the other one wrote. On a shared remote root that answer used to mean "claimable by
// whoever is listing", so the format gap was a cross-host deletion channel.
//
// The precedence row is not decoration: JSON must win whenever it parses, so the
// helper can only ever turn "no host" into a name and never one name into another.
func TestHostnameFromManifestBytes(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{
			name: "the JSON manifest the Go pipeline writes",
			data: `{"hostname":"pve.home.arpa","created_at":"2025-01-02T10:00:00Z"}`,
			want: "pve.home.arpa",
		},
		{
			name: "the KEY=VALUE sidecar the pre-Go pipeline wrote",
			data: "COMPRESSION_TYPE=gzip\nHOSTNAME=hostB\nSCRIPT_VERSION=0.9\n",
			want: "hostB",
		},
		{
			name: "JSON that names no host",
			data: `{"created_at":"2025-01-02T10:00:00Z"}`,
			want: "",
		},
		{
			name: "a KEY=VALUE sidecar that names no host",
			data: "COMPRESSION_TYPE=gzip\nPROXMOX_TYPE=pve\n",
			want: "",
		},
		{
			name: "JSON wins whenever it parses, HOSTNAME text inside it and all",
			data: "{\"hostname\":\"from-json\",\"script_version\":\"HOSTNAME=from-legacy\"}",
			want: "from-json",
		},
		{
			name: "garbage bytes name nobody",
			data: "\x00\x01not a manifest at all",
			want: "",
		},
		{
			name: "empty input names nobody",
			data: "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HostnameFromManifestBytes([]byte(tt.data)); got != tt.want {
				t.Fatalf("HostnameFromManifestBytes = %q, want %q", got, tt.want)
			}
		})
	}
}
