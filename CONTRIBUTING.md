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
