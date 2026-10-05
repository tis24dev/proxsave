package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// Characterization lock for the whole output of every notification channel. The input
// of each case is the NotificationData the channel receives in production, produced by
// the real conversion in internal/orchestrator (TestNotificationGoldens writes it under
// internal/orchestrator/testdata/notification_goldens, one JSON file per channel). This
// test renders every channel from those files and compares the result byte for byte:
// Telegram (buildMessage), the email subject, text and HTML, the MIME message handed to
// proxmox-mail-forward, the Gotify request body, the JSON the email relay POSTs with its
// headers and signature, and the five webhook payloads.
//
// These goldens record what the code does TODAY, defects included. Regenerate
// deliberately, after the inputs: go test ./internal/notify/ -run TestNotificationChannelGoldens -update
var updateNotifyGoldens = flag.Bool("update", false, "rewrite notification channel golden files")

const (
	notifyGoldenInputDir  = "../orchestrator/testdata/notification_goldens"
	notifyGoldenOutputDir = "testdata/notification_goldens"
	// The email recipient the channel is configured with; EMAIL_FROM stays empty, so the
	// notifier puts the default sender in the message.
	notifyGoldenRecipient = "admin@example.com"
	notifyGoldenRelayPath = "/send"
)

// loadNotifyGoldenData reads one channel's input. ok is false when the channel was not
// called in that case. The file must decode with no unknown field and re-encode to the
// same bytes, so nothing the producer wrote is lost on the way in.
func loadNotifyGoldenData(t *testing.T, path string) (*NotificationData, bool) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var data NotificationData
	if err := dec.Decode(&data); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	again, err := json.MarshalIndent(&data, "", "  ")
	if err != nil {
		t.Fatalf("re-encode %s: %v", path, err)
	}
	if string(append(again, '\n')) != string(raw) {
		t.Fatalf("%s does not survive a decode/encode round trip", path)
	}
	return &data, true
}

func newNotifyGoldenLogger() *logging.Logger {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)
	return logger
}

// indentNotifyGoldenJSON is the golden form of a request body: the exact bytes, indented.
// json.Indent only adds whitespace outside strings, so json.Compact of the golden gives
// back the bytes that were sent.
func indentNotifyGoldenJSON(t *testing.T, raw []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		t.Fatalf("indent %q: %v", raw, err)
	}
	out.WriteByte('\n')
	return out.Bytes()
}

// notifyGoldenRequest is one request a fake server received.
type notifyGoldenRequest struct {
	method string
	path   string
	header http.Header
	body   []byte
}

type notifyGoldenServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []notifyGoldenRequest
	reply    string
}

func newNotifyGoldenServer(t *testing.T, reply string) *notifyGoldenServer {
	t.Helper()
	s := &notifyGoldenServer{reply: reply}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, notifyGoldenRequest{method: r.Method, path: r.URL.Path, header: r.Header.Clone(), body: body})
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(s.reply))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *notifyGoldenServer) taken() []notifyGoldenRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.requests
	s.requests = nil
	return out
}

// renderNotifyGoldenTelegram is the message text every Telegram path sends.
func renderNotifyGoldenTelegram(data *NotificationData) []byte {
	return []byte((&TelegramNotifier{}).buildMessage(data))
}

// notifyGoldenCRToken stands for one carriage-return byte (0x0D) in the MIME golden. The
// message mixes LF line ends (headers, boundaries) with CRLF ones (the quoted-printable
// bodies), and a raw CR in a checked-in file would be rewritten by an autocrlf checkout.
// The golden therefore holds no CR byte: every CR is written as this token, left in place
// (so "<CR>" sits right before the LF it precedes). Replacing each token with 0x0D gives
// back the exact bytes handed over; the message is checked to hold no literal token, so
// the substitution cannot be ambiguous.
const notifyGoldenCRToken = "<CR>"

// renderNotifyGoldenPMF sends the email through EMAIL_DELIVERY_METHOD=pmf to a fake
// proxmox-mail-forward that stores its standard input, and returns those bytes: the MIME
// message exactly as handed over (sendmail receives the same buildEmailMessage output),
// with every CR written as notifyGoldenCRToken.
func renderNotifyGoldenPMF(t *testing.T, data *NotificationData) []byte {
	t.Helper()
	capturePath := filepath.Join(t.TempDir(), "pmf_stdin.eml")
	t.Setenv("PMF_CAPTURE_PATH", capturePath)
	script := writeCaptureScript(t, "proxmox-mail-forward", "PMF_CAPTURE_PATH")
	origCandidates := pmfLookPathCandidates
	pmfLookPathCandidates = []string{script}
	t.Cleanup(func() { pmfLookPathCandidates = origCandidates })

	notifier, err := NewEmailNotifier(EmailConfig{
		Enabled:        true,
		DeliveryMethod: EmailDeliveryPMF,
		Recipient:      notifyGoldenRecipient,
	}, data.ProxmoxType, newNotifyGoldenLogger())
	if err != nil {
		t.Fatalf("NewEmailNotifier(pmf): %v", err)
	}
	result, err := notifier.Send(context.Background(), data)
	if err != nil || result == nil || !result.Success {
		t.Fatalf("pmf Send: result=%+v err=%v", result, err)
	}
	got, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read pmf capture: %v", err)
	}
	checkNotifyGoldenMIME(t, got, BuildEmailPlainText(data), BuildEmailHTML(data))
	if bytes.Contains(got, []byte(notifyGoldenCRToken)) {
		t.Fatalf("the MIME message holds a literal %q: the CR token would be ambiguous", notifyGoldenCRToken)
	}
	return bytes.ReplaceAll(got, []byte("\r"), []byte(notifyGoldenCRToken))
}

// checkNotifyGoldenMIME decodes the message and checks that it carries, in this order,
// the text body and the HTML body the email builders return.
func checkNotifyGoldenMIME(t *testing.T, raw []byte, wantText, wantHTML string) {
	t.Helper()
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse MIME: %v", err)
	}
	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" {
		t.Fatalf("Content-Type %q (%v), want multipart/alternative", msg.Header.Get("Content-Type"), err)
	}
	reader := multipart.NewReader(msg.Body, params["boundary"])
	var partTypes, bodies []string
	for {
		part, err := reader.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("next MIME part: %v", err)
		}
		decoded, err := io.ReadAll(quotedprintable.NewReader(part))
		if err != nil {
			t.Fatalf("decode MIME part: %v", err)
		}
		partTypes = append(partTypes, part.Header.Get("Content-Type"))
		bodies = append(bodies, normalizeNotifyGoldenBody(string(decoded)))
	}
	wantTypes := []string{"text/plain; charset=UTF-8", "text/html; charset=UTF-8"}
	if strings.Join(partTypes, "|") != strings.Join(wantTypes, "|") {
		t.Fatalf("MIME parts %q, want %q", partTypes, wantTypes)
	}
	if bodies[0] != normalizeNotifyGoldenBody(wantText) || bodies[1] != normalizeNotifyGoldenBody(wantHTML) {
		t.Fatalf("MIME bodies are not BuildEmailPlainText and BuildEmailHTML, in this order")
	}
}

// normalizeNotifyGoldenBody compares a body across its MIME encoding: the
// quoted-printable writer turns every line break into CRLF and the part delimiter takes
// the trailing ones, so line breaks are compared as LF and trailing ones are dropped.
// The byte-exact golden of the message is what pins the encoding itself.
func normalizeNotifyGoldenBody(s string) string {
	return strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), "\r\n")
}

var notifyGoldenRelayTimeRe = regexp.MustCompile(`"t":(\d+)`)

// renderNotifyGoldenRelay sends the email through the relay to a fake worker and returns
// the body (indented, the send time masked) and the headers that do not change from run
// to run. It checks that X-Signature is the HMAC-SHA256 of the exact bytes received and
// that the masked time is the time of the send.
func renderNotifyGoldenRelay(t *testing.T, data *NotificationData) (body, headers []byte) {
	t.Helper()
	srv := newNotifyGoldenServer(t, `{"success":true}`)
	relay := DefaultCloudRelayConfig
	relay.WorkerURL = srv.URL + notifyGoldenRelayPath
	relay.MaxRetries = 0
	notifier, err := NewEmailNotifier(EmailConfig{
		Enabled:          true,
		DeliveryMethod:   EmailDeliveryRelay,
		Recipient:        notifyGoldenRecipient,
		CloudRelayConfig: relay,
	}, data.ProxmoxType, newNotifyGoldenLogger())
	if err != nil {
		t.Fatalf("NewEmailNotifier(relay): %v", err)
	}
	before := time.Now().Unix()
	result, err := notifier.Send(context.Background(), data)
	after := time.Now().Unix()
	if err != nil || result == nil || !result.Success {
		t.Fatalf("relay Send: result=%+v err=%v", result, err)
	}
	reqs := srv.taken()
	if len(reqs) != 1 {
		t.Fatalf("relay received %d requests, want 1", len(reqs))
	}
	req := reqs[0]

	mac := hmac.New(sha256.New, []byte(DefaultCloudRelayConfig.HMACSecret))
	mac.Write(req.body)
	if got, want := req.header.Get("X-Signature"), hex.EncodeToString(mac.Sum(nil)); got != want {
		t.Fatalf("X-Signature %q is not the HMAC-SHA256 of the body received (%q)", got, want)
	}

	m := notifyGoldenRelayTimeRe.FindAllSubmatch(req.body, -1)
	if len(m) != 1 {
		t.Fatalf("relay body carries %d \"t\" fields, want 1", len(m))
	}
	sent, _ := strconv.ParseInt(string(m[0][1]), 10, 64)
	if sent < before || sent > after {
		t.Fatalf("relay \"t\"=%d is not the send time [%d, %d]", sent, before, after)
	}
	masked := notifyGoldenRelayTimeRe.ReplaceAll(req.body, []byte(`"t":"<SEND TIME, UNIX SECONDS>"`))

	var h strings.Builder
	h.WriteString(req.method + " " + req.path + "\n")
	for _, name := range []string{"Authorization", "Content-Type", "User-Agent", "X-Script-Version", "X-Server-Mac"} {
		h.WriteString(name + ": " + strings.Join(req.header.Values(name), ", ") + "\n")
	}
	h.WriteString("X-Signature: <HMAC-SHA256 of the body as sent, verified>\n")
	return indentNotifyGoldenJSON(t, masked), []byte(h.String())
}

// renderNotifyGoldenGotify sends to a fake Gotify server and returns the request body.
func renderNotifyGoldenGotify(t *testing.T, data *NotificationData) []byte {
	t.Helper()
	srv := newNotifyGoldenServer(t, `{"id":1}`)
	notifier, err := NewGotifyNotifier(GotifyConfig{Enabled: true, ServerURL: srv.URL, Token: "golden-app-token"}, newNotifyGoldenLogger())
	if err != nil {
		t.Fatalf("NewGotifyNotifier: %v", err)
	}
	result, err := notifier.Send(context.Background(), data)
	if err != nil || result == nil || !result.Success {
		t.Fatalf("gotify Send: result=%+v err=%v", result, err)
	}
	reqs := srv.taken()
	if len(reqs) != 1 || reqs[0].path != "/message" {
		t.Fatalf("gotify requests %+v, want one POST /message", reqs)
	}
	return indentNotifyGoldenJSON(t, reqs[0].body)
}

// notifyGoldenWebhookFormats are the five payload formats, one endpoint each.
var notifyGoldenWebhookFormats = []string{"discord", "slack", "teams", "generic", "pushover"}

// renderNotifyGoldenWebhooks sends to one fake endpoint per format and returns each
// request body, keyed by format.
func renderNotifyGoldenWebhooks(t *testing.T, data *NotificationData) map[string][]byte {
	t.Helper()
	srv := newNotifyGoldenServer(t, `{}`)
	endpoints := make([]config.WebhookEndpoint, 0, len(notifyGoldenWebhookFormats))
	for _, format := range notifyGoldenWebhookFormats {
		ep := config.WebhookEndpoint{Name: format, URL: srv.URL + "/" + format, Format: format}
		if format == "pushover" {
			ep.Auth = config.WebhookAuth{Token: "golden-pushover-app-token", User: "golden-pushover-user-key"}
		}
		endpoints = append(endpoints, ep)
	}
	notifier, err := NewWebhookNotifier(&config.WebhookConfig{Enabled: true, Endpoints: endpoints, Timeout: 5}, newNotifyGoldenLogger())
	if err != nil {
		t.Fatalf("NewWebhookNotifier: %v", err)
	}
	result, err := notifier.Send(context.Background(), data)
	if err != nil || result == nil || !result.Success {
		t.Fatalf("webhook Send: result=%+v err=%v", result, err)
	}
	out := map[string][]byte{}
	for _, req := range srv.taken() {
		format := strings.TrimPrefix(req.path, "/")
		if _, dup := out[format]; dup {
			t.Fatalf("webhook %s received twice", format)
		}
		out[format] = indentNotifyGoldenJSON(t, req.body)
	}
	if len(out) != len(notifyGoldenWebhookFormats) {
		t.Fatalf("webhook payloads for %d formats, want %d", len(out), len(notifyGoldenWebhookFormats))
	}
	return out
}

// renderNotifyGoldenCase renders every channel the case called, keyed by golden name.
func renderNotifyGoldenCase(t *testing.T, inputDir string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	if data, ok := loadNotifyGoldenData(t, filepath.Join(inputDir, "telegram.json")); ok {
		files["telegram.txt"] = renderNotifyGoldenTelegram(data)
	}
	if data, ok := loadNotifyGoldenData(t, filepath.Join(inputDir, "email.json")); ok {
		files["email_subject.txt"] = []byte(BuildEmailSubject(data))
		files["email.txt"] = []byte(BuildEmailPlainText(data))
		files["email.html"] = []byte(BuildEmailHTML(data))
		files["email_pmf.eml"] = renderNotifyGoldenPMF(t, data)
		files["email_relay_body.json"], files["email_relay_headers.txt"] = renderNotifyGoldenRelay(t, data)
	}
	if data, ok := loadNotifyGoldenData(t, filepath.Join(inputDir, "gotify.json")); ok {
		files["gotify_body.json"] = renderNotifyGoldenGotify(t, data)
	}
	if data, ok := loadNotifyGoldenData(t, filepath.Join(inputDir, "webhook.json")); ok {
		for format, body := range renderNotifyGoldenWebhooks(t, data) {
			files["webhook_"+format+".json"] = body
		}
	}
	return files
}

// notifyGoldenMessageID stands for the random part of the Message-ID ProxSave gives
// every email it builds (newMessageID); the domain is still taken from the From address.
const notifyGoldenMessageID = "proxsave-00000000000000000000000000000000"

// TestNotificationChannelGoldens renders every channel of every case and compares the
// whole output with its golden file.
func TestNotificationChannelGoldens(t *testing.T) {
	origMessageID := newMessageID
	newMessageID = func(from string) string { return notifyGoldenMessageID + "@" + messageIDDomain(from) }
	t.Cleanup(func() { newMessageID = origMessageID })

	entries, err := os.ReadDir(notifyGoldenInputDir)
	if err != nil {
		t.Fatalf("read inputs %s (run the orchestrator goldens first): %v", notifyGoldenInputDir, err)
	}
	var cases []string
	for _, e := range entries {
		if e.IsDir() {
			cases = append(cases, e.Name())
		}
	}
	if len(cases) == 0 {
		t.Fatalf("no cases under %s", notifyGoldenInputDir)
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			files := renderNotifyGoldenCase(t, filepath.Join(notifyGoldenInputDir, name))
			assertNotifyChannelGoldenDir(t, filepath.Join(notifyGoldenOutputDir, name), files)
		})
	}
	assertNotifyChannelGoldenCases(t, cases)
}

func assertNotifyChannelGoldenDir(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	if *updateNotifyGoldens {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatalf("clear %s: %v", dir, err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		for name, data := range files {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
		}
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s (run with -update to create): %v", dir, err)
	}
	var onDisk, produced []string
	for _, e := range entries {
		onDisk = append(onDisk, e.Name())
	}
	for name := range files {
		produced = append(produced, name)
	}
	sort.Strings(produced)
	if strings.Join(onDisk, ",") != strings.Join(produced, ",") {
		t.Fatalf("%s holds %v, the case renders %v", dir, onDisk, produced)
	}
	for _, name := range produced {
		want, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !bytes.Equal(want, files[name]) {
			t.Errorf("golden mismatch for %s/%s: %s\n--- got ---\n%s", dir, name, firstNotifyGoldenDifference(want, files[name]), files[name])
		}
	}
}

// firstNotifyGoldenDifference names the first line where two outputs differ.
func firstNotifyGoldenDifference(want, got []byte) string {
	wl := strings.Split(string(want), "\n")
	gl := strings.Split(string(got), "\n")
	for i := 0; i < len(wl) || i < len(gl); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g || i >= len(wl) || i >= len(gl) {
			return "line " + strconv.Itoa(i+1) + ": want " + strconv.Quote(w) + ", got " + strconv.Quote(g)
		}
	}
	return "same lines, different bytes"
}

// assertNotifyChannelGoldenCases fails on an output directory whose input case is gone.
func assertNotifyChannelGoldenCases(t *testing.T, cases []string) {
	t.Helper()
	entries, err := os.ReadDir(notifyGoldenOutputDir)
	if err != nil {
		if *updateNotifyGoldens && os.IsNotExist(err) {
			return
		}
		t.Fatalf("read %s: %v", notifyGoldenOutputDir, err)
	}
	known := map[string]bool{}
	for _, c := range cases {
		known[c] = true
	}
	for _, e := range entries {
		if known[e.Name()] {
			continue
		}
		if *updateNotifyGoldens {
			if err := os.RemoveAll(filepath.Join(notifyGoldenOutputDir, e.Name())); err != nil {
				t.Fatalf("remove stale %s: %v", e.Name(), err)
			}
			continue
		}
		t.Fatalf("%s/%s has no input case", notifyGoldenOutputDir, e.Name())
	}
}
