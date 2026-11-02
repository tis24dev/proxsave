package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// A proxy page answered by the bot server reached the run's WARNING line raw: every
// CR/LF, escape byte and bidi rune, up to the 8 KiB read cap. The WARNING now names
// the status only, and the DEBUG error line carries the body quoted and cut.
func TestTelegramServerStatusKeepsTheServerBodyOutOfTheWarning(t *testing.T) {
	page := "<html>\r\n<head><title>502 Bad Gateway</title></head>\r\n<body>\r\n" +
		"<center><h1>502 Bad Gateway</h1></center>\r\n<hr><center>nginx</center>\r\n</body>\r\n</html>\r\n" +
		strings.Repeat("<!-- a padding to disable MSIE and Chrome friendly error page -->\n", 80) +
		"\x1b[2J\x1b[H\u202eFAKE"

	for _, tc := range []struct {
		status  int
		warning string
	}{
		{http.StatusBadGateway, "Telegram: 502 - Unexpected response\n"},
		{http.StatusForbidden, "Telegram: 403 - Start the bot and send the Server ID\n"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(page))
		}))

		prev := logging.GetDefaultLogger()
		var out bytes.Buffer
		logger := logging.New(types.LogLevelDebug, false)
		logger.SetOutput(&out)
		logging.SetDefaultLogger(logger)

		rt := &appRuntime{
			ctx:    context.Background(),
			logger: logger,
			cfg: &config.Config{
				TelegramEnabled: true,
				TelegramBotType: "centralized",
				ServerAPIHost:   srv.URL,
				BaseDir:         t.TempDir(),
			},
			serverIDValue: "1234567890123456",
		}
		logTelegramServerStatus(rt)
		logging.SetDefaultLogger(prev)
		srv.Close()

		log := out.String()
		if rt.cfg.TelegramEnabled {
			t.Fatalf("HTTP %d: Telegram still enabled after the WARNING", tc.status)
		}
		if !strings.Contains(log, tc.warning) {
			t.Fatalf("HTTP %d: no WARNING line %q; log:\n%s", tc.status, tc.warning, log)
		}
		for _, raw := range []string{"\x1b", "\u202e", "\r", "<head>\n", "<html>\r\n"} {
			if strings.Contains(log, raw) {
				t.Fatalf("HTTP %d: raw server body (%q) in the log:\n%s", tc.status, raw, log)
			}
		}
		for line := range strings.SplitSeq(strings.TrimSuffix(log, "\n"), "\n") {
			if !strings.HasPrefix(line, "[") {
				t.Fatalf("HTTP %d: the body split a line, %q has no prefix; log:\n%s", tc.status, line, log)
			}
		}
	}
}
