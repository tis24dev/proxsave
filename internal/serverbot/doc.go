// Package serverbot is a thin authenticated transport to the proxsave bot-server
// (ServerAPIHost). It owns transport + auth ONLY: host normalization, version
// stamping, the auth/provision/notify-id headers, a per-request timeout, bounded
// body reads, and transport-error redaction. It owns NO endpoint vocabulary: no
// paths, query keys, DTOs, or HTTP-status semantics live here -- those stay in the
// callers (notify, health). It targets ONLY the bot-server (ServerAPIHost); it must
// never be pointed at api.telegram.org or the hc.proxsave.dev monitor.
//
// Import contract (enforced by the leaf guard in the lint target): this package may
// import only internal/logging, internal/version, and the standard library. It must
// not import internal/{health,notify,orchestrator,config,identity}.
package serverbot
