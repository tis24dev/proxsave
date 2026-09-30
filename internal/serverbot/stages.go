package serverbot

import (
	"net/http/httptrace"
	"strings"
	"sync"
)

// defaultLogOperation is the DEBUG operation a Request that names none is logged under.
const defaultLogOperation = "serverbot"

// stages records how far one round trip got, so its DEBUG lines tell "not sent" (dns, connect,
// request) from "sent, no answer" (response). The httptrace hooks may run on the transport's own
// goroutines, so every field is guarded by mu; Do reads them only after the round trip returned
// and writes the lines itself, in stage order, so a late hook can never interleave with them.
type stages struct {
	mu         sync.Mutex
	dnsStarted bool
	dnsDone    bool
	dnsErr     error
	addrs      []string
	connected  bool
	reused     bool
	wrote      bool
	writeErr   error
}

func (s *stages) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.dnsStarted = true
		},
		DNSDone: func(info httptrace.DNSDoneInfo) {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.dnsDone, s.dnsErr = true, info.Err
			for _, a := range info.Addrs {
				s.addrs = append(s.addrs, a.String())
			}
		},
		GotConn: func(info httptrace.GotConnInfo) {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.connected, s.reused = true, info.Reused
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.wrote, s.writeErr = true, info.Err
		},
	}
}

// completed returns the DEBUG messages of the stages the round trip completed, in order.
func (s *stages) completed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	if s.dnsDone && s.dnsErr == nil {
		out = append(out, "dns ok addr="+strings.Join(s.addrs, ","))
	}
	if s.connected {
		if s.reused {
			out = append(out, "connected reused=true")
		} else {
			out = append(out, "connected")
		}
	}
	if s.wrote && s.writeErr == nil {
		out = append(out, "request written")
	}
	return out
}

// failedStage names the first stage a failed round trip did not complete: dns and connect mean
// the request never left this host, request that it did not leave in full, response that the
// relay got it and its answer did not arrive.
func (s *stages) failedStage() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.dnsErr != nil || (s.dnsStarted && !s.dnsDone):
		return "dns"
	case !s.connected:
		return "connect"
	case !s.wrote || s.writeErr != nil:
		return "request"
	default:
		return "response"
	}
}
