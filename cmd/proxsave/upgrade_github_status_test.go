package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

type rewriteToServer struct {
	target *url.URL
	base   http.RoundTripper
}

func (r rewriteToServer) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = r.target.Scheme
	req.URL.Host = r.target.Host
	return r.base.RoundTrip(req)
}

// A non-200 answer from GitHub went raw into the upgrade's ERROR line: a proxy's 502
// page made 8 lines with its escape and bidi bytes. The line now names the status, plus
// the message of GitHub's JSON error for the release API (its rate limit says why there);
// the answer itself goes to a DEBUG line, on one line of printable characters.
func TestUpgradeErrorsNameTheGitHubStatusNotTheAnswer(t *testing.T) {
	page := "<html>\r\n<head><title>502 Bad Gateway</title></head>\r\n<body>\r\n" +
		"<center><h1>502 Bad Gateway</h1></center>\r\n<hr><center>nginx</center>\r\n</body>\r\n</html>\r\n\x1b[2J\u202e"
	rateLimit := `{"message":"API rate limit exceeded for 203.0.113.7. (But here's the good news: Authenticated requests get a higher rate limit. Check out the documentation for more details.)","documentation_url":"https://docs.github.com/rest/overview/resources-in-the-rest-api#rate-limiting"}`

	for _, tc := range []struct {
		status   int
		body     string
		release  string
		download string
	}{
		{http.StatusBadGateway, page,
			"failed to fetch latest release: status 502 Bad Gateway",
			"download failed with status 502 Bad Gateway"},
		{http.StatusForbidden, rateLimit,
			"failed to fetch latest release: status 403 Forbidden: API rate limit exceeded for 203.0.113.7. (But here's the good news: Authenticated requests get a higher rate limit. Check out the documentation for more details.)",
			"download failed with status 403 Forbidden"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		target, _ := url.Parse(server.URL)
		prev := http.DefaultTransport
		http.DefaultTransport = rewriteToServer{target: target, base: prev}
		_, _, _, releaseErr := fetchLatestRelease(context.Background())
		http.DefaultTransport = prev

		var out bytes.Buffer
		bootstrap := logging.NewBootstrapLogger()
		bootstrap.SetConsoleQuiet(true)
		mirror := logging.New(types.LogLevelDebug, false)
		mirror.SetOutput(&out)
		bootstrap.SetMirrorLogger(mirror)
		bootstrap.SetLevel(types.LogLevelDebug)
		root, err := os.OpenRoot(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		downloadErr := downloadFile(context.Background(), server.URL+"/proxsave.tar.gz", root, "proxsave.tar.gz", bootstrap)
		_ = root.Close()
		server.Close()

		if releaseErr == nil || releaseErr.Error() != tc.release {
			t.Fatalf("HTTP %d: release error %q, want %q", tc.status, releaseErr, tc.release)
		}
		if downloadErr == nil || downloadErr.Error() != tc.download {
			t.Fatalf("HTTP %d: download error %q, want %q", tc.status, downloadErr, tc.download)
		}
		log := out.String()
		if !strings.Contains(log, "response body=") {
			t.Fatalf("HTTP %d: no DEBUG line with the answer; log:\n%s", tc.status, log)
		}
		for _, raw := range []string{"\x1b", "\u202e", "\r"} {
			if strings.Contains(log, raw) {
				t.Fatalf("HTTP %d: raw answer (%q) in the log:\n%s", tc.status, raw, log)
			}
		}
	}
}
