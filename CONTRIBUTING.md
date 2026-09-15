# Contributing to ProxSave

## Source hygiene: the Trojan-source guard

ProxSave rejects deceptive Unicode in its tracked source. Bidirectional
controls, zero-width and invisible-format runes, and confusable homoglyph
letters (a Cyrillic "a" that looks like a Latin "a") can make source render
differently from the bytes the compiler reads, deceiving a human or an
automated reviewer (CVE-2021-42574, "Trojan Source").

CI enforces this: the `internal/sourceguard` package includes a test that scans
every tracked file and fails, naming `file:line`, on any deceptive rune. A
change that introduces one cannot merge.

To catch it locally before you commit, enable the pre-commit hook:

    make hooks

(or `git config core.hooksPath .githooks`). The hook scans your staged files
and blocks the commit if it finds a deceptive rune. In a genuine emergency you
can bypass it with `git commit --no-verify`, but CI will still reject the push.

If you legitimately need a non-ASCII rune in a `.go` file, write it as a `\u`
escape rather than a literal byte, so the source itself stays free of
deceptive characters.

## Release notes: written with the change, not at release time

Every commit that changes what an operator can observe adds its line to the
current unreleased entry in `internal/whatsnew/registry.go`, in that same
commit. What a change meant to the person running ProxSave is known while it is
being made and reconstructed badly weeks later, so batching the notes to
release time is how entries go missing.

The current unreleased entry is the last one in `notes`: the version above the
newest git tag. When the newest tag already covers that entry, append a new one
for the next version.

- `Lines` are what changed, from the operator's side. Screen 0 caps them at
  **8**. When the eight are full, do not quietly drop one to make room: say so,
  and let the maintainer decide what gives way.
- `Actions` are what the operator has to do about it, and are not capped.
- Every string is ASCII only (no em-dash, en-dash or emoji), 120 characters or
  fewer, and free of placeholder text.
- A change with nothing an operator can observe (a refactor, a test, a comment)
  gets no line. Say it got none rather than inventing one.

`TestRegistryWellFormed` enforces the mechanical rules on every PR, so run
`go test ./internal/whatsnew/` before you commit.
