# Test strategy (proxsave)

## Progressive suite review and the `*_audited_test.go` convention

The existing test suite is being reviewed progressively to make sure no test
crystallizes a bug (that is, asserts as "correct" a behavior of the untested code that
is actually wrong). To tell reviewed tests from not-yet-reviewed ones at a glance, we
use a **filename suffix**:

- **`*_test.go`** (no marker) = **legacy, not yet reviewed**.
- **`*_audited_test.go`** = **reviewed**, in one of two cases:
  - it was **written after the baseline** (with the code under test already known good), or
  - it is a legacy file that was **reread and approved** (no bug crystallized).

Go only requires the file to end in `_test.go`, so `*_audited_test.go` is collected by
`go test` like any other test: the convention has no runtime cost.

### Operating rules

- When you write **new** tests, name them `<something>_audited_test.go` directly.
- When you **finish reviewing** a legacy file, rename it with `git mv`:
  `git mv foo_test.go foo_audited_test.go` (history is preserved; the rename is itself
  the review log, so make one atomic commit per file or area).
- For huge files reviewed in pieces, use **per-function** granularity with a dated tag
  comment above the `func Test...`:
  `// audited: 2026-06-09 verified it does not crystallize a bug`
- **Do not** bulk-rename the legacy files: a rename happens only once a review is
  actually complete.

### Progress

```bash
# how many are left to review
find . -name '*_test.go' -not -name '*_audited_test.go' -not -path './vendor/*' | wc -l
# how many are already reviewed
find . -name '*_audited_test.go' -not -path './vendor/*' | wc -l
# next ones to do in a package
find internal/orchestrator -name '*_test.go' -not -name '*_audited_test.go'
```

As of this writing there are 347 legacy files still to review and 30 audited files.

### Git baseline

The tag **`tests-audit-baseline`** (on `3222a30`, 2026-06-09) marks the state of the
suite at the start of the review. To see *what* changed in the tests relative to that
point:

```bash
git diff --name-only tests-audit-baseline -- '*_test.go'
```

The `_audited_` suffix answers "reviewed / to review"; the tag answers "created
before/after the baseline". Together they cover both questions.

## UI driver tests (`internal/uitest`)

`internal/uitest` holds test-only helpers for the Charm/bubbletea driver tests. It is
imported only from `_test.go` files and is never linked into the production binary.

The driver tests poll a render buffer until a screen or line appears. Under the race
detector the bubbletea event loop runs roughly an order of magnitude slower, so a fixed
wall-clock deadline can fire spuriously even when the logic is correct.
`uitest.Deadline(base)` scales a base render-poll timeout by a **race-aware factor**
(`raceScale` = 8 under `-race`, 1 otherwise, selected by build tag). Because those polls
return as soon as the condition is met, a wider deadline is free on the success path: it
only adds headroom before a genuine hang is reported. Use it **only** for UI-render
polling deadlines, never for a test that asserts an operation's own timeout behavior.

The driver tests are also sensitive to CPU saturation: run them **one package at a
time** rather than fanning out `go test` (or `-race`) across packages in parallel, or the
Charm driver can still hit spurious render-poll timeouts under load. Always read the real
exit code rather than piping the output through `tail` or similar.

## Context

The review grew out of the pre-test coverage and correctness audit of 2026-06-09
(`diagnostics/coverage-audit-2026-06-09.md`, dev @ `3222a30`, the same commit as the
`tests-audit-baseline` tag), which surveyed how much of the code we were about to test
was still unvetted. Writing tests over unvetted code risks freezing its defects in
place, so the rule is: verify the code is correct first, then write the test, which is
born `_audited`.
