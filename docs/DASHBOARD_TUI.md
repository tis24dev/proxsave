# Dashboard TUI Architecture

This is the developer reference for the interactive terminal UI: the dashboard and every graphical flow (backup stream, restore, decrypt, new key, install). It covers the Charm (bubbletea v2) stack ProxSave is built on, the load-bearing invariants that are easy to break, and how to add a screen or test one headlessly.

For the operator-facing walkthrough of the screens, see [DASHBOARD.md](DASHBOARD.md).

## Overview

There is exactly one long-lived bubbletea program per interactive mode, wrapped in a `shell.Session` that runs it in a goroutine. Engine code never touches the program directly; it talks to the UI through a single generic bridge, `shell.Ask[T]`, which pushes a screen onto a modal stack and blocks until the user answers. On top of the shell sit nine reusable component screens, a shared theme, and a sanitizer that every component runs on untrusted data.

The design has a few deliberate, non-obvious rules. Break any of these and the symptoms are subtle (a stranded goroutine, a garbled live log, the wrong screen popped, an injected escape sequence):

- Screen removal is by id, not pop-the-top, because a resolve runs asynchronously.
- Ctrl+C is a global interrupt, not a back action. Esc is per-screen back or No.
- The streaming viewport keeps SoftWrap off and wraps itself, or it is O(N) per frame.
- Every untrusted string is sanitized at the component constructor, because lipgloss has none of tview's structural escape immunity.
- The dashboard-to-flow handoff adopts the live program, it never tears the frame down and rebuilds it.

## Where it lives

| Path | Role |
|------|------|
| `cmd/proxsave/dashboard.go` (+ `dashboard_*.go`) | The entry gate, the menu dispatch, the session handoff, and the in-session diagnostic/daemon/upgrade/cleanup/support screens. |
| `internal/ui/shell/` | The core: `Session` (`session.go`), the `Ask` bridge (`ask.go`), the `Screen`/`Resolver` model (`screen.go`), routing and rendering (`router.go`), the test harness (`testing.go`). |
| `internal/ui/components/` | The reusable screens: selector, multiselect, formgrid, confirm, input, pager, streamtask, taskview, notice, and the sanitizer. |
| `internal/ui/theme/theme.go` | The Proxmox palette, symbols, and the Status keyword mappers. |
| `internal/ui/flows/menu/menu.go` | The launcher menu (builds items, returns an `Action`). |
| `internal/ui/flows/{install,agesetup}/` | The install wizard, diagnostic setup screens, and the AGE new-key flow. |
| `internal/orchestrator/workflow_ui_charm*.go` | The adapter that maps engine prompts to components for backup/restore/decrypt. |

## The entry gate and dispatch

`maybeRunDashboard` in `cmd/proxsave/dashboard.go` is the whole entry point, and it lives in `cmd`, not in the shell. It runs only when `args != nil` and both gates pass:

- `dashboardIsBareInvocation()`: `len(os.Args) <= 1`. Any flag at all, including `--config`, disqualifies it.
- `dashboardIsInteractive()`: stdin and stdout are both TTYs and `TERM` is non-empty and not `dumb`.

Both are package vars defaulting to the real checks, so tests can force the dashboard on or off. In the lifecycle (`main_lifecycle.go`) the call sits after flag parsing, the version/help modes, the first `rejectIncompatibleModes`, cleanup-guards mode, and config-path resolution, and before the pre-runtime dispatch.

Dispatch is by args-mutation. When you pick an action the dashboard sets the same field the flag sets (`args.Restore`, `args.Decrypt`, `args.ForceNewKey`, `args.Install`, `args.NewInstall`, `args.Support`) and returns `handled=false` so the normal flag-driven flow runs it. The one exception is Backup, which mutates nothing: it sets `keepAlive` and stashes the session, then lets the ordinary no-flag run proceed and adopt that session. Because a menu choice can flip an args field, `rejectIncompatibleModes` runs a second time right after `maybeRunDashboard` so the menu can never reach a combination the flags would reject.

`maybeRunDashboard` returns `handled=true` (stop here) only for a terminal action (Exit), the idle timeout, or a dashboard failure. It never falls through into a backup on failure. The idle timeout is `dashboardIdleTimeout = 10 * time.Minute`, applied per menu render (so interaction resets it), and on `DeadlineExceeded` it exits `ExitSuccess` with a distinct stderr message.

The menu itself (`menu.Run`) is a pure launcher: it assembles `[]components.SelectorItem`, calls `shell.Ask`, and returns an `Action`. It reads nothing and mutates nothing; the daemon group is context-aware only because the caller passes in a precomputed `menu.DaemonState` (from `SchedulerMode`, `DaemonOptOut`, or an unreadable config). Esc, Ctrl+C, and a dying UI all resolve to `ActionExit` with a nil error, so a human at the menu never gets a surprise backup out of a failed screen.

## The Session

`shell.Session` (in `session.go`) owns exactly one `*tea.Program`, a `done` channel, a write-once `runErr`, and an atomic id counter.

- `Start(ctx, cfg, opts...)` scrubs all chrome strings (see [Sanitization](#sanitization)), builds program options starting from `tea.WithContext(ctx)`, appends `colorprofile.Ascii` when `cfg.UseColor` is false, appends caller options last (so tests can inject input and renderer), and launches `prog.Run()` in a goroutine that records `runErr` and closes `done`. It returns immediately.
- Cancelling `ctx` kills the program and restores the terminal (it is passed via `tea.WithContext`). `Close()` calls `prog.Quit()` then blocks on `done`, so the terminal is restored before the caller prints anything. `Close` is idempotent and swallows the two expected terminations, `tea.ErrProgramKilled` (context kill) and `tea.ErrInterrupted` (Ctrl+C), returning nil for them.
- `Send` forwards to `prog.Send` and is a safe no-op once the program has terminated.

`closedErr()` wraps `runErr` into `ErrClosed`; a blocked `Ask` returns this when `done` fires. `IsAbort(err)` matches `ErrClosed` or the legacy `ErrAborted`, and flows use it to turn a UI death or a Ctrl+C into a clean abort.

## Session handoff: Adopt, stash, release

`Adopt(cfg)` rebrands a running session without tearing it down. It sends an `adoptConfigMsg`; the router swaps `cfg` wholesale but preserves the previous `observeScreenPush` (that belongs to the program and the test harness, not the incoming flow) and leaves the screen stack untouched. Only header and footer chrome changes (subtitle, version, config path, build signature, color gate). The altscreen never flashes.

The dashboard uses this to hand its live program to a flow:

- `stashDashboardSession` stores the session and bootstrap logger under a mutex, mutes the console for the handoff window (`bootstrap.SetConsoleQuiet(true)`, default logger output swapped to `io.Discard`), and installs `adoptDashboardSession` as `orchestrator.SetUISessionHandoff`. The mute matters because anything printed while the altscreen is up corrupts the frame.
- The flow's `orchestrator.newUISession` calls the handoff. `adoptDashboardSession` consumes the stash once (it nils the session, so a second call returns nil), latches `graphical=true`, calls `session.Adopt(cfg)`, and lifts the mute. Install and new-key adopt through `newAgeSetupSession`, which tries the handoff first and falls back to `shell.Start`.
- A freshly created run logger self-mutes to `io.Discard` while `dashboardHandoffPending()` is true, so it stays off the console until adoption.
- `releaseDashboardLeftovers`, deferred in `main.run()`, is the safety net: if the chosen flow died before adopting (the session is still stashed), it closes the session, restores the console, and replays only the warning-and-worse entries recorded since the mute via `bootstrap.ReplayConsoleSince(mark)`, so an early failure is never invisible. If the session was already adopted it only resets the graphical latch for test isolation.

The `graphical` latch is set on adoption and, unlike the session and bootstrap, is not cleared by it (only at process end). This is deliberate: `dashboardRunWasGraphical()` gates the plain-scrollback CLI final-summary footer off for the whole graphical run, since the outcome is already on screen.

## The Ask bridge

`Ask[T](ctx, s, scr AskScreen[T])` in `ask.go` is the only bridge between an engine goroutine and the UI event loop. It:

1. Creates a buffered result channel (capacity 1) and a `sync.Once`-guarded resolve closure.
2. Calls `scr.Bind(resolve)`, mints `id = nextID.Add(1)`, sets it on the screen, and sends `pushScreenMsg{id, screen}`.
3. Blocks on a three-way select:
   - the resolve channel fires: return `(v, err)` and send an idempotent `removeScreenMsg{id}` as a safety net (the screen normally pops itself);
   - `ctx.Done()` fires: send a best-effort `removeScreenMsg{id}` and return `ctx.Err()`;
   - `s.done` fires: return `closedErr()` (`ErrClosed`) and send no removal, the program is already gone.

Two invariants keep this correct. The resolve channel is buffered so the tea loop never blocks delivering the answer, and it is `Once`-guarded so a user answer racing a cancellation cannot double-send (first resolve wins). And every `Ask` selects on `s.done`, so a program that dies mid-prompt (context kill or Ctrl+C) can never strand the blocked engine goroutine. Flows issue one `Ask` at a time per session; concurrent asks would stack and route keys to the most recent.

## Screens, Resolver, and pop-by-id

`Screen` (in `screen.go`) is `Init() tea.Cmd`, `Update(msg) (Screen, tea.Cmd)`, `View(width, height int) string` (inner dimensions inside the frame), `Title() string`, `Help() string`. Screens are stacked; only the top one receives user input.

`AskScreen[T]` embeds `Screen` plus `Bind(respond func(T, error))`, called once by `Ask`. `Resolver[T]` is the reusable resolve half: components embed it, `Bind` stores the responder, and `Resolve(v, err)` calls the responder and returns a command that emits `removeScreenMsg` for that screen's id.

Removal is strictly by id, never pop-the-top. `Ask` records the id on the screen; `Resolve` pops that exact id. This matters because the resolve command runs asynchronously: the engine may have already pushed the next screen before the pop message lands, so popping the top would drop the wrong screen. An unknown id is a no-op. `TestRouterResolvePopsByIDNotTop` locks this.

## Routing and the frame

`rootModel` (in `router.go`) holds `cfg`, the screen stack, and the terminal size. Its `Update` handles, in order: `WindowSizeMsg`, `adoptConfigMsg`, `pushScreenMsg` (append and fire `observeScreenPush` with the screen's title), `removeScreenMsg` (remove by id), and a `ctrl+c` key press mapped to `tea.Interrupt`. Then it classifies the message:

- User input (`KeyMsg`, `MouseMsg`, and the paste messages) goes to the top screen only, after mouse translation.
- Every other message (ticks, task progress and done, spinner ticks) broadcasts to the top screen and to any buried screen that implements `BackgroundMessageReceiver` and returns true. This keeps third-party widgets (text inputs, huh forms) insulated from each other's unexported messages, while a buried countdown or a buried streaming task keeps ticking. Among components, `Task` and `StreamTask` always opt in; `Confirm` opts in only while a countdown is armed.

Ctrl+C is a global interrupt. From any screen it returns `tea.Interrupt` and does not mutate the stack; pending asks then unblock via their `s.done` branch and return `ErrClosed`, identical to a UI death. Going back one level is Esc or an on-screen Back item, handled per-screen before any abort fall-through. `ErrAborted` survives only as a defensive match in `IsAbort`.

The frame is a rounded border with the orange theme color, a header (app name, breadcrumbs joined by an arrow, right-aligned version), a rule, the body, and a footer (the top screen's help legend plus a `Config:`/`Build:` status line). `View()` always enables the altscreen and cell-motion mouse (parity with the old tview UI, which always had mouse on), regardless of config. The OSC background repaint and window title are set only when `cfg.UseColor` is true, because monochrome mode targets dumb and serial terminals.

`bodyViewport()` is the single source of truth for body geometry, used by both `render()` and `translateMouse()`. lipgloss v2 is border-box, so the inner width is `w-4` (border and padding) and the body origin is column 2, row 3. `render()` hard-crops the body (and the chrome) to the inner size before `lipgloss.Place`, because `Place` does not clip and an oversized body would push the footer and buttons off the altscreen. `translateMouse` rebases mouse coordinates into body space and swallows clicks, motion, and release outside the body on all four sides, so a component never hit-tests a cropped-off chrome row; wheel events pass through at any position so scrolling works anywhere.

## Components

All nine live in `internal/ui/components`, embed `shell.Resolver`, are driven through `shell.Ask`, and sanitize their data strings at construction.

| Component | What it is |
|-----------|------------|
| `Selector[T]` | Single-pick list. Arrow/`j`/`k` navigation (no wrap, skips separators), Home/End, digit shortcuts only when there are 9 or fewer selectable rows, `/` filter offered above 8, wheel and click. Enter selects, Esc clears an active filter first then resolves the back sentinel. |
| `MultiSelect[T]` | Checkbox list resolving to the selected values. Space toggles, `a` all, `i` invert. `WithMinSelected` rejects an empty confirm inline. An actions mode adds select-all and confirm buttons and changes Enter semantics. An optional detail pane shows the highlighted item's detail when wide enough. |
| `FormGrid` | A single-screen aligned form (label left, control right), never a sequence of one-field screens. Toggle, text, and select fields; per-field `Active` gate and `Validate`; a fixed consent note above the fields; Tab reaches Cancel; only the Continue button submits. |
| `Confirm` | Yes/No, default focus equals the Enter default and defaults to No. `WithDefaultYes` flips it, `WithDanger` renders a warning and disables the single-key `y`/`n` shortcuts, `WithCountdown` adds a timer. |
| `Input` | Single-line text or secret (`WithSecret` masks it). `WithValidate` rejects inline (the error is sanitized because it can echo the value). Enter confirms; Esc aborts with `shell.ErrAborted` unless a back sentinel is set with `WithInputBack`. |
| `Pager` | Scrollable static text (restore plans, reports). Enter confirms, Esc or `q` aborts, so a reflex Esc on a plan never counts as acceptance. Self-wraps like StreamTask. |
| `Task` | Spinner plus title plus a single latest progress line. Resolves only on completion, never on input alone; Esc cancels. |
| `StreamTask` | A contained, scrollable, colored live-log panel. See [below](#streamtask-performance). |
| `Notice` | A message with a single acknowledge; `NoticeKind` sets the accent. |

Two safety asymmetries are load-bearing and carried over from tview. `Confirm`'s countdown always resolves to No on timeout, regardless of `WithDefaultYes`; the countdown advertises the No label, the Enter default is advertised separately on the button. So an unattended destructive prompt cannot auto-apply just because Enter was set to Yes. And `Pager`'s Esc/`q` is an abort, not acceptance.

## StreamTask performance

`StreamTask` is the panel the streamed backup renders into, and it is the most performance-sensitive screen. The naive approach garbles or melts down, so it does three specific things.

Self-wrap with SoftWrap off. The bubbles viewport's own SoftWrap re-measures every retained line on every render, and `View`, `TotalLineCount`, and `GotoBottom` all trigger it, so with the spinner ticking around ten times a second it is O(N) per frame. Instead the viewport keeps `SoftWrap=false` and StreamTask wraps content itself: it keeps a raw ring plus a derived slice of already-wrapped display rows, wraps each new line once on arrival, and re-wraps the whole buffer only on a width change. `wrapLine` reproduces the viewport's fixed-column wrap byte-for-byte (an `ansi.Cut` over `[idx, idx+width)` chunks, grapheme-safe at the boundary, re-emitting SGR per chunk) so the output is identical, just cheaper.

Size before content, both in View. In `View()` the viewport is sized (`SetWidth`/`SetHeight`) first, then fed `SetContentLines` (pre-wrapped, newline-free rows), gated on a `dirty` flag so a bare spinner tick does not re-feed the whole buffer. Doing `SetContent` before sizing, or in `Update` instead of `View`, reproduces the live-garble bug fixed in commit `80704d7`. `Pager` follows the same idiom.

Non-blocking coalescing emit. `emit()` appends into a mutex-guarded slice and returns immediately, so UI backpressure never stalls the backup's `io.Copy` pump. A flusher goroutine drains into `StreamLinesMsg` batches on whichever comes first: 256 pending lines (via a one-deep coalescing wake channel) or a 60ms tick. `Close()` does a final flush and joins the goroutine before `StreamDoneMsg` is sent, and `emit` is a no-op after `Close`, so every line is delivered in order ahead of done and nothing races past it.

The raw ring is bounded at 5000 lines; older lines drop first with a `(showing last 5000 lines)` note, and a cap-driven drop sheds exactly the wrapped rows of the dropped lines (O(drop), not an O(cap) re-wrap). The `c` copy uses the raw ring, not the wrapped rows, so a pasted support log has the original logical lines. Auto-follow pins to the newest line and turns off the moment the user scrolls up, re-enabling at the bottom.

## Theme and the Status vocabulary

`theme.go` is a fixed dark-first Proxmox palette: orange `#E57000` for the border, titles and selection, plus green, red, yellow, blue, and a magenta that matches the Ctrl+C interrupt summary color. `Background` `#101010` is painted explicitly on every frame; there is no terminal-background detection, because serial and IPMI terminals answer background queries unreliably. Symbols are `✓ ✗ ⚠ ℹ ▸ → • ☑ ☐`.

`StatusColor`/`StatusSymbol` map status keywords: `success`/`ok`/`done`/`completed` to green `✓`; `error`/`failed`/`fail` to red `✗`; `warning`/`warn` to yellow `⚠`; `info`/`pending`/`running` to blue `ℹ` (note `pending` and `running` share the info color); anything else to light `•`.

Result screens across the daemon, install, and workflow code do not use this directly; they delegate to `orchestrator.RenderStatusLevel`, which is the one renderer for a styled `Status:` line and has four levels: Ok (green `✓`), Error (red `✗`), Warn (yellow `⚠`), and Neutral (yellow, no symbol). Neutral is a front-end-only pre-check state (`NOT CHECKED`); `NOT CONFIGURED` is a Warn keyword (with the `⚠`), not a Neutral one. Keeping every result screen on one renderer is what stops the Status look from drifting.

## Sanitization

lipgloss has none of tview's structural escape immunity: a raw ESC in a filename could restyle its own row. So every component constructor sanitizes its data strings, and `sanitize.go` offers three flavors:

- `SanitizeText` strips ANSI and control characters but keeps newlines and tabs, for multi-line messages.
- `SanitizeLine` also collapses newlines and tabs to spaces, for one-line labels, menu rows, and filenames.
- `sanitizeStreamLine` is the color-preserving one for the log panel: it keeps SGR escapes (`ESC[...m`) verbatim so log colors survive, drops every other escape (cursor moves, mode toggles, OSC), and flattens other controls. This is what lets a rogue log line color itself but not weaponize the terminal.

The one intentional bypass is `WithSelectorPromptStyled`, which renders a pre-styled prompt verbatim without sanitizing or wrapping. Anything placed there that is remote or attacker-influenceable (a GitHub release tag or notes, a server-minted magic link, a daemon version read from `.daemon_info.json`, an external error string) must be scrubbed by the caller first, with `SanitizeText`, `serverbot.SanitizeLoginURL`, `upgradeSafeToken`, or `sanitizeNotesLine`. That is why sanitization lives at both the component boundary and the orchestrator's styled-prompt builder. The shell also scrubs all chrome strings (including the untrusted `--config` path) with `cleanChrome` at `Start` and `Adopt`.

## The workflow bridge

`charmWorkflowUI` (`internal/orchestrator/workflow_ui_charm.go` and `_restore.go`) is a pure adapter for the backup, restore, and decrypt flows. It holds a `*shell.Session`, a logger, and an abort sentinel, and contains no rendering code: every engine prompt maps to a component plus a blocking `Ask`. `mapAbort` turns a shell abort (`IsAbort`) into the flow's canonical `ErrRestoreAborted`/`ErrDecryptAborted`, leaving other errors alone.

Restore-specific mappings worth knowing:

- Category selection is a `MultiSelect` with `WithMinSelected(1)`; Esc resolves a back-to-mode sentinel, not an abort.
- Confirmation is two `Confirm` asks: a `RESTORE` button with default focus, then a destructive `Overwrite and restore` guard with `WithDefaultYes(false)` and `WithDanger()`.
- The cluster SAFE/RECOVERY/Exit choice is a plain `Selector`, shown only when the plan needs a cluster restore.
- The restore plan is a `Pager` (Esc/`q` aborts).
- The network-commit prompt is a danger Confirm with a countdown whose default and focus are always the safe "let rollback run"; on timeout it does not commit.
- Workflow outcomes and errors render as a `Selector` with a `WithSelectorPromptStyled` `Status:` block (a single Continue item), not a `Notice`, so they match the daemon and check result screens.

The new-key flow is the exception: it uses a separate `agesetup` adapter via `runNewKeyTUI`, not `charmWorkflowUI`, though it still joins the Adopt handoff through `newAgeSetupSession`.

Every flow that owns a session swaps its console output to `io.Discard` for the session lifetime, because raw stdout corrupts the altscreen diff renderer, and defers `Close` so it runs LIFO (terminal restored before the writer comes back). The streamed backup instead redirects `os.Stdout` and captures both loggers into the panel's pipe, so the panel shows the same colored lines and blank-line spacers in the same order as the CLI.

## Writing a new screen or flow

To add a component screen:

1. Embed `shell.Resolver[T]` and implement `Screen` (`Init`, `Update`, `View`, `Title`, `Help`). Return `Resolve(value, err)`'s command from `Update` when the user answers.
2. Sanitize every data string in the constructor (`SanitizeLine` for one-liners, `SanitizeText` for multi-line). Do not put untrusted text through a styled-prompt path without scrubbing it first.
3. If the screen must keep working while buried under a later screen (a timer, a background task), implement `BackgroundMessageReceiver`.
4. Size the viewport before setting content, both in `View`, if you use one.

To add a flow, drive your screens with `shell.Ask` one at a time against a session from `newUISession` (so it participates in the dashboard handoff), map aborts through your flow's sentinel, and keep the console muted for the session's lifetime.

To add a dashboard action, prefer args-mutation: set the flag the action corresponds to and let the existing flow run, rather than re-implementing the mode in the menu path.

## Testing headlessly

The TUI is tested without a real terminal.

- `StartForTest` launches a renderless session (`tea.WithoutRenderer`, empty input, fixed window size), driven by `Session.Send` plus the key parser.
- `StartObservedForTest` forces `UseColor=false` for deterministic output, wires an `observeScreenPush` callback, and writes to a buffer. The push observer is the reliable signal that a screen reached the stack, because renderer output alone is racy: the cell-diff renderer emits nothing for an identical re-render.
- `KeyMsg(name)` parses `ctrl+`, `alt+`, `shift+` prefixes and special key names or a printable rune into a key message; `Keys(script)` splits a whitespace string into a sequence. These live in a non-test file so orchestrator and flow suites can share them.

Because bubbletea drives the Charm driver, keep UI tests serial and off a saturated CPU, and use the race-aware deadline in `internal/uitest`. Parallel UI tests produce false timeouts.
