package orchestrator

import "testing"

func TestUnescapeProcPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "no escapes", in: "/mnt/pbs", want: "/mnt/pbs"},
		{name: "space", in: `/mnt/pbs\040datastore`, want: "/mnt/pbs datastore"},
		{name: "tab", in: `/mnt/pbs\011datastore`, want: "/mnt/pbs\tdatastore"},
		{name: "newline", in: `/mnt/pbs\012datastore`, want: "/mnt/pbs\ndatastore"},
		{name: "backslash", in: `/mnt/pbs\134datastore`, want: `/mnt/pbs\datastore`},
		{name: "multiple", in: `a\040b\134c`, want: `a b\c`},
		{name: "incomplete", in: `a\0b`, want: `a\0b`},
		{name: "non octal digit", in: `a\08b`, want: `a\08b`},
		{name: "out of range preserved", in: `a\777b`, want: `a\777b`},
		{name: "null byte", in: `a\000b`, want: "a\x00b"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := unescapeProcPath(tt.in); got != tt.want {
				t.Fatalf("unescapeProcPath(%q)=%q want %q", tt.in, got, tt.want)
			}
		})
	}
}
