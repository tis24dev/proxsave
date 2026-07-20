package serverbot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// F11-03: a 302 from the bot-server host must NOT cause the Client to re-send the
// per-request X-Server-Auth secret to the redirect target. Go's stdlib strips only
// Authorization/Cookie/WWW-Authenticate cross-host, not the custom X-Server-Auth, so
// without a redirect-refusing CheckRedirect the secret leaks to any 302 Location.
func TestDo_RefusesRedirect_SecretNeverLeaks(t *testing.T) {
	var attackerHits int64
	var sawSecret atomic.Bool
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&attackerHits, 1)
		if r.Header.Get(serverAuthHeader) != "" {
			sawSecret.Store(true)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer attacker.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL, http.StatusFound)
	}))
	defer origin.Close()

	c := New(origin.URL, nil, nil)
	resp, err := c.Do(context.Background(), Request{Path: "/", Secret: "TOP-SECRET"})

	if err == nil {
		t.Fatal("expected a transport error when the bot-server redirects, got nil")
	}
	if resp != nil {
		t.Errorf("expected nil response on refused redirect, got %+v", resp)
	}
	if n := atomic.LoadInt64(&attackerHits); n != 0 {
		t.Errorf("attacker host was contacted %d time(s); the redirect must NOT be followed", n)
	}
	if sawSecret.Load() {
		t.Error("X-Server-Auth leaked to the redirect target")
	}
}
