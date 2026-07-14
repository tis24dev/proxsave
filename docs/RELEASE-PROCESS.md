# Release Process (Proxsave Go)

This document describes the safe, repeatable procedure to publish a new **Proxsave** release using the repository's GitHub Actions workflows.

## Overview

You do not tag `main` by hand. A release is started by pushing a lightweight trigger tag on `dev`, and the automation opens the release PR, creates the authoritative version tag after the merge, and publishes the release:

```
push pr-vX.Y.Z on dev
  -> release-intake.yml opens a dev -> main PR (marker: <!-- release-tag: vX.Y.Z -->)
  -> release-guard.yml validates the PR
  -> squash-merge the PR into main
  -> post-merge-release.yml force-moves dev and creates the vX.Y.Z tag on the squash commit
  -> release.yml (GoReleaser) builds, signs, and publishes the GitHub Release
```

No binaries are ever committed to the repository. All release artifacts are produced by GoReleaser.

## Branch model

- `dev`: day-to-day development (features, fixes, refactors). Releases are started from here.
- `main`: the released branch. The `vX.Y.Z` tag is created on the squash commit that lands on `main`.

After a dev -> main PR merges, `post-merge-release.yml` force-moves `dev` onto the squash commit with `--force-with-lease` (this replaced the old `sync-dev.yml`). The squash commit is not a descendant of the old `dev`, so the move is forced, not a fast-forward; a tree-equality check first guarantees the squash content matches `dev` exactly, so nothing is lost. Maintenance (non-release) dev -> main PRs are allowed too: they carry no release marker, so `dev` is synced but no release is published.

## Step-by-step

### 1) Work on `dev`

- Make changes on `dev`.
- Commit using Conventional Commits (`feat:`, `fix:`, `refactor:`, `feat!:` / `BREAKING CHANGE:` for breaking changes).
- Push `dev` to origin.

### 2) Start the release: push a `pr-vX.Y.Z` trigger tag on `dev`

The trigger tag is `pr-` plus the version you want to publish. It is unprotected and deliberately does NOT match the `v*` glob, so it never triggers `release.yml` and can be created and deleted freely:

```bash
git checkout dev
git pull --ff-only
git tag pr-v1.6.0        # or pr-v1.6.0-rc1 / pr-v1.6.0-beta1 for a prerelease
git push origin pr-v1.6.0
```

`release-intake.yml` then:

- checks the trigger tag points to the current HEAD of `dev`;
- refuses if the version `v1.6.0` already exists as a tag or release (immutable versions), and fails closed on a transient lookup error;
- refuses if a DIFFERENT dev -> main PR is already open (re-pushing the same `pr-v1.6.0` while its release PR is already open is a no-op);
- deletes the `pr-v1.6.0` trigger tag (its job is done);
- opens a PR `dev -> main` titled `Release v1.6.0` whose body carries the marker `<!-- release-tag: v1.6.0 -->`.

### 3) Let the guard validate the PR

`release-guard.yml` runs on the PR and enforces its shape: the base is `main`, the head is `dev`, the PR comes from this same repository, and (when a release marker is present) the tag is a well-formed `vX.Y.Z`. Wait for CI, security, and static-analysis checks to pass.

### 4) Squash-merge the PR into `main`

Merge the PR using a SQUASH merge once checks are green. The merge must be a squash: `post-merge-release.yml` refuses a non-squash merge.

### 5) Automation creates the tag and publishes the release

`post-merge-release.yml` runs on the merged PR and, for a release PR:

- force-moves `dev` onto the squash commit with `--force-with-lease` (safe because the squash content equals `dev`);
- creates the authoritative annotated `v1.6.0` tag ONCE on that squash commit and pushes it (a tag creation, which the tag-immutability ruleset allows). It never force-pushes or moves a `v*` tag.

Pushing the `v1.6.0` tag re-triggers `release.yml`, which:

- gates the release on the tag being reachable from `origin/main` (a stray `v*` tag on a dev commit is not released);
- validates the SemVer tag;
- validates the signing key against the pinned public key BEFORE publishing (so a missing or wrong signing secret fails before anything goes live);
- runs GoReleaser (`.github/.goreleaser.yml`) to build, archive, generate the SBOM and checksums, and create the GitHub Release;
- signs `SHA256SUMS` with the project ECDSA P-256 key, verifies it against the pinned key, and uploads `SHA256SUMS.sig`;
- attests build provenance for `build/proxsave_*` via `actions/attest-build-provenance`.

## What you should see in Release assets

Only `linux/amd64` is built and published. For tag `v1.6.0` (GoReleaser drops the leading `v` in filenames) the assets are:

```
proxsave_1.6.0_linux_amd64
proxsave_1.6.0_linux_amd64.tar.gz
proxsave_1.6.0_linux_amd64.tar.gz.sbom.cdx.json
SHA256SUMS
SHA256SUMS.sig
```

`proxsave_1.6.0_linux_amd64` is the uncompressed binary (GoReleaser archive id `binary`); the `.tar.gz` is the same binary plus `LICENSE` and `README.md`.

`SHA256SUMS.sig` is **mandatory**: the install and upgrade flow refuses to proceed without a verifiable release signature (`install.sh` and `proxsave --upgrade` download and verify it), and `release.yml` validates the signing key before publishing. See [PROVENANCE_VERIFICATION.md](PROVENANCE_VERIFICATION.md).

## Install script

Users install the latest published version with:

```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/main/install.sh)"
```

## What not to do

- Do not commit binaries or the `build/` directory.
- Do not push a bare `v*` tag on `dev`. The trigger you push on `dev` is the `pr-v*` tag; the real `v*` tag is created by the automation on the squash commit on `main`.
- Do not create GitHub Releases manually (the workflow does it).
- Do not try to move or re-tag a published `v*` tag: version tags are immutable, and the automation only ever creates one once.

### Legacy escape hatch

`release.yml` also releases a `v*` tag pushed directly onto a commit that is already on `main` (it passes the reachable-from-main gate immediately). This is a fallback only; the normal path is the `pr-v*` trigger tag on `dev`.
