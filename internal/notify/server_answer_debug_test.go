package notify

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// proxyPage is a reverse proxy's 502 page with an escape sequence and a bidi rune, as
// the bot server, api.telegram.org or a Gotify server can answer through a proxy.
const proxyPage = "<html>\r\n<head><title>502 Bad Gateway</title></head>\r\n<body>\r\n" +
	"<center><h1>502 Bad Gateway</h1></center>\r\n<hr><center>nginx</center>\r\n</body>\r\n</html>\r\n\x1b[2J\u202e"

// assertAnswerOnlyAtDebug checks that a server's answer stayed out of the error and
// reached the log once, at DEBUG, on one line of printable characters.
func assertAnswerOnlyAtDebug(t *testing.T, site string, log string, err error, wantErr string, secrets ...string) {
	t.Helper()
	if err == nil || err.Error() != wantErr {
		t.Fatalf("%s: error %q, want %q", site, err, wantErr)
	}
	if !strings.Contains(log, "502 Bad Gateway</title>") {
		t.Fatalf("%s: no DEBUG line with the answer; log:\n%s", site, log)
	}
	for _, raw := range append([]string{"\x1b", "\u202e", "\r"}, secrets...) {
		if strings.Contains(log, raw) {
			t.Fatalf("%s: %q in the log:\n%s", site, raw, log)
		}
	}
	for line := range strings.SplitSeq(strings.TrimSuffix(log, "\n"), "\n") {
		if !strings.HasPrefix(line, "[") {
			t.Fatalf("%s: the answer split a line, %q has no prefix; log:\n%s", site, line, log)
		}
	}
}

// A non-2xx answer went raw, multi-line and unbounded for api.telegram.org, into the
// error of three senders, printed at DEBUG twice (the sender and the adapter). The
// errors now name the status only; the answer is one DEBUG line, cut to 200.
func TestSenderErrorsKeepTheServerAnswerAtDebug(t *testing.T) {
	const botToken = "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	const gotifyToken = "GotifyAppTokenAbc123"

	newLogger := func(out *bytes.Buffer) *logging.Logger {
		logger := logging.New(types.LogLevelDebug, false)
		logger.SetOutput(out)
		return logger
	}

	t.Run("telegram personal", func(t *testing.T) {
		var out bytes.Buffer
		notifier, err := NewTelegramNotifier(TelegramConfig{
			Enabled: true, Mode: TelegramModePersonal, BotToken: botToken, ChatID: "123456",
		}, newLogger(&out))
		if err != nil {
			t.Fatal(err)
		}
		notifier.client = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusBadGateway,
				Body:       io.NopCloser(strings.NewReader(proxyPage + " " + req.URL.Path + strings.Repeat("x", 1<<20))),
				Header:     make(http.Header),
			}, nil
		})}
		sendErr := notifier.sendToTelegram(context.Background(), botToken, "123456", "text")
		if sendErr != nil {
			notifier.logger.Debug("Telegram failure detail: %v", sendErr)
		}
		assertAnswerOnlyAtDebug(t, "telegram personal", out.String(), sendErr, "telegram api returned status 502", botToken)
	})

	t.Run("telegram centralized", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(proxyPage))
		}))
		defer server.Close()
		var out bytes.Buffer
		notifier, err := NewTelegramNotifier(TelegramConfig{
			Enabled: true, Mode: TelegramModeCentralized, ServerAPIHost: server.URL, ServerID: "1234567890123456",
		}, newLogger(&out))
		if err != nil {
			t.Fatal(err)
		}
		_, _, fetchErr := notifier.fetchCentralizedCredentials(context.Background())
		assertAnswerOnlyAtDebug(t, "telegram centralized", out.String(), fetchErr, "unexpected status 502")
	})

	t.Run("gotify", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(proxyPage + " token=" + r.URL.Query().Get("token") + " " + r.Header.Get("X-Gotify-Key")))
		}))
		defer server.Close()
		var out bytes.Buffer
		notifier, err := NewGotifyNotifier(GotifyConfig{Enabled: true, ServerURL: server.URL, Token: gotifyToken}, newLogger(&out))
		if err != nil {
			t.Fatal(err)
		}
		result, err := notifier.Send(context.Background(), createTestNotificationData())
		if err != nil {
			t.Fatal(err)
		}
		assertAnswerOnlyAtDebug(t, "gotify", out.String(), result.Error, "gotify returned HTTP 502", gotifyToken)
	})
}
