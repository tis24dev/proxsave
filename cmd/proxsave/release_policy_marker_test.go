package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// extractReleaseMarker runs the release pipeline's own extract_pr_marker on body, the way
// post-merge-release, release-guard, release-notes-guard and release-intake call it.
func extractReleaseMarker(t *testing.T, body string) (string, bool) {
	t.Helper()
	for _, tool := range []string{"bash", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available: %v", tool, err)
		}
	}
	cmd := exec.Command("bash", "-c", "source ../../.github/scripts/release-policy.sh && extract_pr_marker release-tag")
	cmd.Env = append(os.Environ(), "PR_BODY="+body)
	out, err := cmd.Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return "", false
		}
		t.Fatalf("run extract_pr_marker: %v", err)
	}
	return strings.TrimSpace(string(out)), true
}

// The release-tag marker is what turns the merge of a release PR into a release. GitHub
// stores a body edited through the API or the web form with CRLF line endings, and the
// marker regex anchors on "$", which does not match before "\r": on the merge of PR #343
// post-merge-release logged "No release marker found", synced dev and published nothing,
// and the v0.45.0 tag had to be created by hand.
func TestExtractReleaseMarkerAcceptsCRLFBodies(t *testing.T) {
	const lines = "Automated release PR for `v0.45.0`.\n\n<!-- release-tag: v0.45.0 -->\n\n<!-- This is an auto-generated comment: release notes by coderabbit.ai -->\n## Summary by CodeRabbit\n"
	for _, tc := range []struct {
		name string
		body string
	}{
		{"LF, as release-intake writes it", lines},
		{"CRLF, as GitHub stores an edited body", strings.ReplaceAll(lines, "\n", "\r\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tag, ok := extractReleaseMarker(t, tc.body)
			if !ok || tag != "v0.45.0" {
				t.Fatalf("extract_pr_marker = (%q, found=%v), want (\"v0.45.0\", true) for body %q", tag, ok, tc.body)
			}
		})
	}
}

// The marker still has to be a line of its own: a body without it, or with it inside other
// text, is a maintenance PR and must not publish a release.
func TestExtractReleaseMarkerNeedsTheMarkerLine(t *testing.T) {
	for _, body := range []string{
		"Maintenance PR, no release.\r\n",
		"see <!-- release-tag: v0.45.0 --> inline\n",
		"",
	} {
		if tag, ok := extractReleaseMarker(t, body); ok {
			t.Errorf("extract_pr_marker found %q in %q, want no marker", tag, body)
		}
	}
}
