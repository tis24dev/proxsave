package cron

import "testing"

func TestNormalizeTime(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		defaultValue string
		want         string
		wantErr      string
	}{
		{name: "default fallback", input: "", defaultValue: DefaultTime, want: DefaultTime},
		{name: "normalize short values", input: "3:7", defaultValue: DefaultTime, want: "03:07"},
		{name: "trim whitespace", input: " 03:15 ", defaultValue: DefaultTime, want: "03:15"},
		{name: "invalid format", input: "0315", defaultValue: DefaultTime, wantErr: "cron time must be in HH:MM format"},
		{name: "invalid hour", input: "24:00", defaultValue: DefaultTime, wantErr: "cron hour must be between 00 and 23"},
		{name: "invalid minute", input: "00:60", defaultValue: DefaultTime, wantErr: "cron minute must be between 00 and 59"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeTime(tt.input, tt.defaultValue)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatal("expected error")
				}
				if err.Error() != tt.wantErr {
					t.Fatalf("NormalizeTime(%q) error = %q, want %q", tt.input, err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeTime(%q) returned error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeTime(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestTimeToSchedule(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "valid", in: "02:05", want: "05 02 * * *"},
		{name: "normalized short", in: "2:5", want: "05 02 * * *"},
		{name: "invalid", in: "bad", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TimeToSchedule(tt.in); got != tt.want {
				t.Fatalf("TimeToSchedule(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
