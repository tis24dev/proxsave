package serverbot

import (
	"encoding/json"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// Request describes one call to the bot-server. Every field is caller-supplied; the
// Client adds no endpoint knowledge. Secret == "" omits X-Server-Auth (the pre-auth
// get-chat-id call). Provision and NotifyID are caller-gated and never defaulted on.
type Request struct {
	Method    string        // http.MethodGet / http.MethodPost ("" -> GET)
	Path      string        // "/api/notify", "/api/get-chat-id", ... (leading slash, no host)
	Query     url.Values    // .Encode() escapes every value; caller sets server_id etc.
	Secret    string        // != "" -> X-Server-Auth; "" -> header omitted
	Provision bool          // true -> X-Proxsave-Provision:"1"
	NotifyID  string        // != "" -> X-Notify-Id (serverbot never mints one)
	Body      any           // != nil -> json.Marshal + Content-Type: application/json
	Timeout   time.Duration // per-request ctx cap; 0 -> 5s
	MaxBytes  int64         // response read cap; 0 -> 8192
}

// Response carries the RAW HTTP status (load-bearing: the caller maps semantics) and
// the bounded body. It never contains endpoint DTOs (e.g. login_url): the caller
// parses those from Body.
type Response struct {
	Status int    // raw HTTP status code
	Body   []byte // io.ReadAll(io.LimitReader(body, MaxBytes)); never unbounded
}

// JSON unmarshals the body into v.
func (r *Response) JSON(v any) error { return json.Unmarshal(r.Body, v) }

// Snippet returns a log-safe excerpt of the body: every non-printable rune is
// dropped (control chars AND Unicode format/bidi/zero-width tricks, so a hostile
// body can inject neither a terminal escape nor a Trojan-Source visual reorder into
// a log line) and the result is truncated to at most n runes. unicode.IsPrint keeps
// graphic runes plus the ASCII space; it excludes C0/C1/DEL, U+202E/U+200B/U+2066,
// and exotic separators. It is NOT secret-redacted: the caller must wrap it with
// logging.RedactSecrets(snippet, secret) when the per-request secret could appear in
// the body.
func (r *Response) Snippet(n int) string {
	if n <= 0 {
		return ""
	}
	var b strings.Builder
	count := 0
	for _, ru := range string(r.Body) {
		if count >= n {
			break
		}
		if !unicode.IsPrint(ru) {
			continue
		}
		b.WriteRune(ru)
		count++
	}
	return b.String()
}
