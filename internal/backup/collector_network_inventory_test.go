package backup

import "testing"

func TestParseEthtoolPermanentMAC(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{
			name:   "capitalized",
			input:  "Permanent address: 00:11:22:33:44:55\n",
			expect: "00:11:22:33:44:55",
		},
		{
			name:   "lowercase",
			input:  "permanent address: aa:bb:cc:dd:ee:ff\n",
			expect: "aa:bb:cc:dd:ee:ff",
		},
		{
			name:   "extra whitespace",
			input:  "Permanent address:    00:aa:bb:cc:dd:ee   \n",
			expect: "00:aa:bb:cc:dd:ee",
		},
		{
			name:   "missing",
			input:  "some other output\n",
			expect: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseEthtoolPermanentMAC(tt.input); got != tt.expect {
				t.Fatalf("got %q want %q", got, tt.expect)
			}
		})
	}
}
