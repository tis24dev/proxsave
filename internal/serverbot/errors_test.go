package serverbot

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestAuthRejected(t *testing.T) {
	for _, s := range []int{401, 403} {
		if !AuthRejected(s) {
			t.Errorf("AuthRejected(%d) = false, want true", s)
		}
	}
	for _, s := range []int{200, 400, 404, 409, 500, 503} {
		if AuthRejected(s) {
			t.Errorf("AuthRejected(%d) = true, want false", s)
		}
	}
}

func TestTransportErrorUnwrap(t *testing.T) {
	te := newTransportError("request", context.Canceled, "sek")
	if !errors.Is(te, context.Canceled) {
		t.Error("Unwrap must expose the original error for errors.Is (context.Canceled)")
	}
	if te.Op != "request" {
		t.Errorf("Op = %q, want request", te.Op)
	}
	if !strings.HasPrefix(te.Error(), "request: ") {
		t.Errorf("Error() = %q, want it prefixed by the op", te.Error())
	}
}
