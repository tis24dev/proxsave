package main

import "testing"

// The system-crontab extractor took the command field as it was, so a /etc/cron.d line
// with an assignment in front of the command ("root TZ=UTC /usr/local/bin/proxsave")
// was invisible to the duplicate-schedule checks, while the same job in the root
// crontab was seen (3eac58c4). Both extractors now share the step-over.
func TestSystemCronCommandTokenStepsOverAssignments(t *testing.T) {
	for _, tc := range []struct{ system, user, want string }{
		{"0 2 * * * root TZ=UTC /usr/local/bin/proxsave", "0 2 * * * TZ=UTC /usr/local/bin/proxsave", "/usr/local/bin/proxsave"},
		{"@daily root TZ=Europe/Rome /usr/local/bin/proxsave --backup", "@daily TZ=Europe/Rome /usr/local/bin/proxsave --backup", "/usr/local/bin/proxsave"},
		{"0 2 * * * root PATH=/usr/sbin:/usr/bin LANG=C /usr/local/bin/proxsave", "0 2 * * * PATH=/usr/sbin:/usr/bin LANG=C /usr/local/bin/proxsave", "/usr/local/bin/proxsave"},
		{"0 2 * * * root /usr/local/bin/proxsave", "0 2 * * * /usr/local/bin/proxsave", "/usr/local/bin/proxsave"},
		{`0 2 * * * root MSG=a\ proxsave /usr/bin/rsync`, `0 2 * * * MSG=a\ proxsave /usr/bin/rsync`, `MSG=a\`},
		{"MAILTO=root", "MAILTO=root", ""},
		{"0 2 * * * root", "0 2 * * *", ""},
	} {
		if got := systemCronCommandToken(tc.system); got != tc.want {
			t.Errorf("systemCronCommandToken(%q) = %q, want %q", tc.system, got, tc.want)
		}
		if got := cronCommandToken(tc.user); got != tc.want {
			t.Errorf("cronCommandToken(%q) = %q, want %q", tc.user, got, tc.want)
		}
	}
}
