# Release Process (Proxsave Go)

This document describes the safe, repeatable procedure to publish a new **Proxsave** release using the repository’s GitHub Actions workflows.

## Overview

Release flow:

```
dev → Pull Request → main → tag vX.Y.Z → GitHub Actions (GoReleaser) → GitHub Release
```

No binaries should ever be committed to the repository. All release artifacts are produced by GoReleaser.

## Branch model

- `dev`: day-to-day development (features, fixes, refactors)
- `main`: stable branch; releases are tagged from here

After a PR is merged into `main`, the `dev` branch is automatically synced to `main` by `.github/workflows/sync-dev.yml`.

## Step-by-step

### 1) Work on `dev`

- Make changes on `dev`
- Commit using Conventional Commits (examples):
  - `feat: ...` (new feature)
  - `fix: ...` (bug fix)
  - `refactor: ...` (refactor)
  - `feat!: ...` or `BREAKING CHANGE:` (breaking change)
- Push `dev` to origin

### 2) Open a PR `dev` → `main`

- Create a Pull Request from `dev` to `main`
- Wait for all checks to pass (CI/security/static analysis)

### 3) Merge into `main`

Merge the PR only when checks are green. The resulting commit on `main` (merge commit or squash commit) is what you will tag.

### 4) Create and push a SemVer tag on `main`

The release workflow triggers on tag pushes matching `v*` and validates a SemVer-like format:

- Stable: `vMAJOR.MINOR.PATCH` (example: `v1.6.0`)
- Prerelease: `vMAJOR.MINOR.PATCH-rc1` / `-beta1` / etc. (example: `v1.6.0-rc1`)

CLI example:

```bash
git checkout main
git pull --ff-only
git tag v1.6.0
git push origin v1.6.0
```

### 5) GitHub Actions builds and publishes the release

Once the tag is pushed:

- Workflow: `.github/workflows/release.yml`
- GoReleaser config: `.github/.goreleaser.yml`
- Output: GitHub Release with binaries, archives, checksums, and SBOMs
- Provenance: `actions/attest-build-provenance` generates provenance for artifacts matching `build/proxsave_*`

## What you should see in Release assets

For tag `v1.6.0` (GoReleaser strips the leading `v` for the version in filenames), typical assets include:

```
proxsave_1.6.0_linux_amd64
proxsave_1.6.0_linux_amd64.tar.gz
proxsave_1.6.0_linux_amd64.tar.gz.sbom.cdx.json
SHA256SUMS
```

## Install script (optional)

Users can install the latest `main` version using:

```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/main/install.sh)"
```

## What not to do

- Do not commit binaries or the `build/` directory
- Do not tag commits on `dev`
- Do not create GitHub Releases manually (the workflow does it)
- Do not tag old commits; tag the exact commit you want to ship on `main`

## Notes on future automation (auto-tagging)

An auto-tag workflow exists at `.github/workflows/autotag.yml` but is currently disabled (`if: false`). If enabled, it can create tags automatically based on Conventional Commit messages.
