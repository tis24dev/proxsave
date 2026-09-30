package serverbot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/version"
)

const (
	versionHeader    = "X-Proxsave-Version"
	serverAuthHeader = "X-Server-Auth"
	provisionHeader  = "X-Proxsave-Provision"
	notifyIDHeader   = "X-Notify-Id"

	defaultTimeout  = 5 * time.Second
	defaultMaxBytes = 8192
)

// Client is a thin authenticated transport to the proxsave bot-server (ServerAPIHost).
// It owns host normalization, version stamping, the per-request auth/provision/
// notify-id headers, a per-request timeout, a bounded body read, and transport-error
// redaction -- and nothing endpoint-specific. It holds NO serverID and NO secret:
// both are per-Request, so one Client serves the no-secret get-chat-id call, the
// authenticated notify call, and the fresh-token confirm without mutation.
type Client struct {
	base   string          // strings.TrimRight(host, "/") computed once
	http   *http.Client    // owned copy of the caller's client with a no-redirect CheckRedirect
	logger *logging.Logger // optional; debug only, NEVER a body, NEVER a secret
}

// New normalizes the host once and returns a Client whose http.Client refuses ALL
// redirects. The caller's client (or a zero client when nil) is shallow-copied so its
// Transport/pool and Timeout are preserved while CheckRedirect is overridden WITHOUT
// mutating the caller's object -- critical because callers pass the shared, global
// http.DefaultClient. The no-redirect policy therefore holds on every path through New.
func New(serverAPIHost string, httpClient *http.Client, logger *logging.Logger) *Client {
	var effective http.Client
	if httpClient != nil {
		effective = *httpClient // shallow copy: shares Transport/Jar, copies Timeout
	}
	effective.CheckRedirect = refuseRedirect
	return &Client{
		base:   strings.TrimRight(serverAPIHost, "/"),
		http:   &effective,
		logger: logger,
	}
}

// refuseRedirect fail-closes on ANY HTTP redirect. CheckRedirect is consulted only when
// a 3xx with a Location would be followed; returning an error aborts BEFORE the follow-up
// request is sent, so the per-request X-Server-Auth secret never leaves the origin host.
// Go's stdlib strips only Authorization/Cookie/WWW-Authenticate cross-host, NOT custom
// headers, so following a redirect would leak the relay secret. The bot-server API never
// legitimately redirects. The error surfaces to Do as a *TransportError ("request").
func refuseRedirect(_ *http.Request, via []*http.Request) error {
	return fmt.Errorf("serverbot: refusing HTTP redirect after %d hop(s)", len(via))
}

// Do performs ONLY the shared transport surface, nothing semantic:
//  1. ctx = context.WithTimeout(ctx, req.Timeout or 5s)
//  2. url = base + Path + "?" + Query.Encode()
//  3. headers: ALWAYS X-Proxsave-Version; X-Server-Auth iff Secret != ""; X-Proxsave-
//     Provision:"1" iff Provision; X-Notify-Id iff NotifyID != ""; Content-Type iff Body != nil
//  4. execute; body = io.ReadAll(io.LimitReader(rc, req.MaxBytes or 8192))
//  5. return (&Response{Status, cloned Header, Body}, nil) for ANY completed exchange,
//     including non-2xx
//
// LOAD-BEARING: an HTTP status is NEVER an error. err != nil only on an encode/build/
// dial/read failure, and that error is a *TransportError whose message is already
// redacted (URL stripped, per-request secret masked).
//
// With a logger, each call writes DEBUG lines under req.LogOperation ("serverbot" when empty):
// the URL without its query, then the stages the exchange completed (dns ok addr=..., connected,
// request written), then either "response http=<code> elapsed=<ms>" or "failed stage=dns|connect|
// request|response error=<the TransportError's redacted message>". NEVER a body, NEVER a secret.
func (c *Client) Do(ctx context.Context, req Request) (*Response, error) {
	op := req.LogOperation
	if op == "" {
		op = defaultLogOperation
	}
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	c.logURL(op, method, req.Path)

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var trace *stages
	if c.logger != nil {
		trace = &stages{}
		reqCtx = httptrace.WithClientTrace(reqCtx, trace.clientTrace())
	}

	endpoint := c.base + req.Path
	if enc := req.Query.Encode(); enc != "" {
		endpoint += "?" + enc
	}

	var bodyReader io.Reader
	if req.Body != nil {
		raw, err := json.Marshal(req.Body)
		if err != nil {
			return nil, c.failed(op, "encode", nil, newTransportError("encode", err, req.Secret))
		}
		bodyReader = bytes.NewReader(raw)
	}

	httpReq, err := http.NewRequestWithContext(reqCtx, method, endpoint, bodyReader)
	if err != nil {
		return nil, c.failed(op, "build", nil, newTransportError("build", err, req.Secret))
	}

	httpReq.Header.Set(versionHeader, version.String())
	if req.Secret != "" {
		httpReq.Header.Set(serverAuthHeader, req.Secret)
	}
	if req.Provision {
		httpReq.Header.Set(provisionHeader, "1")
	}
	if req.NotifyID != "" {
		httpReq.Header.Set(notifyIDHeader, req.NotifyID)
	}
	if req.Body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	started := time.Now()
	resp, err := c.http.Do(httpReq)
	if err != nil {
		te := newTransportError("request", err, req.Secret)
		if trace != nil {
			return nil, c.failed(op, trace.failedStage(), trace, te)
		}
		return nil, te
	}
	defer func() { _ = resp.Body.Close() }()

	maxBytes := req.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, c.failed(op, "response", trace, newTransportError("read", err, req.Secret))
	}

	if trace != nil {
		c.logStages(op, trace)
		logging.DebugStep(c.logger, op, "response http=%d elapsed=%dms", resp.StatusCode, time.Since(started).Milliseconds())
	}
	return &Response{
		Status: resp.StatusCode,
		Header: resp.Header.Clone(),
		Body:   body,
	}, nil
}

// logURL writes the DEBUG line naming where the call goes: the host and path, never the query
// (server_id and friends live there) and never a password embedded in the host. The method is
// named only when it is not GET.
func (c *Client) logURL(op, method, path string) {
	if c.logger == nil {
		return
	}
	target := c.base + path
	if u, err := url.Parse(target); err == nil {
		target = u.Redacted()
	}
	if method == http.MethodGet {
		logging.DebugStep(c.logger, op, "url=%s", target)
		return
	}
	logging.DebugStep(c.logger, op, "url=%s method=%s", target, method)
}

// logStages writes one DEBUG line per stage the round trip completed.
func (c *Client) logStages(op string, trace *stages) {
	if trace == nil {
		return
	}
	for _, line := range trace.completed() {
		logging.DebugStep(c.logger, op, "%s", line)
	}
}

// failed writes the completed stages and the failed one, and returns te unchanged. The error text
// is the TransportError's own redacted message, so the line carries nothing te would not.
func (c *Client) failed(op, stage string, trace *stages, te *TransportError) *TransportError {
	if c.logger != nil {
		c.logStages(op, trace)
		logging.DebugStep(c.logger, op, "failed stage=%s error=%s", stage, te.redacted)
	}
	return te
}
