package types

import "testing"

func TestNormalizeHostname(t *testing.T) {
	tests := []struct {
		name string
		host string
		want string
	}{
		{name: "case and a trailing root dot are the same name", host: "PVE.Home.Arpa.", want: "pve.home.arpa"},
		{name: "surrounding space is not part of the name", host: " pve ", want: "pve"},
		{name: "a trailing root dot alone", host: "pve.", want: "pve"},
		{name: "an unqualified name is left alone", host: "pve", want: "pve"},
		{name: "empty", host: "", want: ""},
		{name: "blank", host: "  ", want: ""},
		{name: "a qualified name keeps its domain", host: "pve.siteA.example", want: "pve.sitea.example"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeHostname(tt.host); got != tt.want {
				t.Fatalf("NormalizeHostname(%q) = %q, want %q", tt.host, got, tt.want)
			}
		})
	}
}

// TestNormalizeHostnameNeverStripsADomain pins the property the whole retention
// ownership rule rests on: normalising is spelling only. If this function ever
// starts folding to the first label, "pve.siteA.example" and "pve.siteB.example"
// become one machine and one host's retention prunes the other's backups.
func TestNormalizeHostnameNeverStripsADomain(t *testing.T) {
	if NormalizeHostname("pve.siteA.example") == NormalizeHostname("pve") {
		t.Fatal("normalising must not reduce a qualified name to its first label")
	}
	if NormalizeHostname("pve.siteA.example") == NormalizeHostname("pve.siteB.example") {
		t.Fatal("two domains must stay two machines")
	}
}
