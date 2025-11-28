# Release Process for Proxmox Backup (Go Version)

This document explains the complete, correct, and safe procedure for creating a new release of the **Proxmox Backup Go tool**, using the GitHub workflow you have configured.

It ensures:
- stable development flow
- clean merges
- safe tagging
- correct triggering of GoReleaser
- predictable, reproducible releases

---

# ?? Overview of the Release Flow

The release flow always follows this order:

```
dev ? Pull Request ? main ? tag vX.Y.Z ? GoReleaser builds binaries ? GitHub Release
```

There are **no binaries** in any branch — GoReleaser handles everything.

---

# 1. ?? Work on the `dev` Branch

All new features, fixes, and changes must be developed on the `dev` branch.

### Steps:
1. Open GitHub Desktop
2. Select branch: `dev`
3. Make your changes
4. Commit using **Conventional Commit** messages:
   - `feat: ...` ? new feature
   - `fix: ...` ? bug fix
   - `refactor: ...` ? code refactor
   - `feat!: ...` ? breaking change
5. Push: **Push origin**

> ? No binaries should ever be committed.

---

# 2. ?? Create a Pull Request (PR) from `dev` to `main`

In GitHub Desktop:

- Click **"Create Pull Request"** (when the banner appears)

This opens GitHub with the PR ready.

### PR Guidelines:
- Title must follow Conventional Commit rules
- Description optional but recommended
- Automatic checks (Staticcheck / GoSec / CodeQL) will run

Merge the PR **only when all checks pass**.

---

# 3. ? Merge the PR into `main`

On GitHub (web):

- Click **Merge Pull Request**
- Choose either:
  - **Merge Commit** (recommended), or
  - **Squash and Merge** (clean history)

### Important:
Merging the PR creates **a new commit on `main`**. This is the commit you will tag.

---

# 4. ??? Create a Tag on the `main` Branch

A tag is what triggers GoReleaser. No tag ? no release.

### In GitHub Desktop:

1. Switch branch: **main**
2. Pull latest changes: **Pull origin**
3. Go to the **History** tab
4. Locate the **most recent commit**, typically:

```
Merge pull request #XX from dev
```

5. Right-click that commit ? **Create Tag…**
6. Enter the version:
   - Stable release: `v1.6.0`
   - Beta: `v1.6.0-beta1`
   - Release candidate: `v1.6.0-rc1`

7. Confirm
8. Push the tag: **Push origin**

---

# 5. ?? GoReleaser Builds the Release

As soon as the tag is pushed, GitHub Actions will:

- Validate the tag
- Run GoReleaser
- Build binaries
- Build tar.gz packages
- Build standalone binaries
- Generate SBOMs (CycloneDX)
- Generate SHA256SUMS
- Generate detailed changelog
- Publish everything in the GitHub Release
- Mark it as **pre-release** automatically if the tag contains `-beta` or `-rc`

You don't have to do anything else.

---

# 6. ?? What appears in the Release Assets

For example, on tag `v1.6.0` you will see:

```
proxmox-backup_v1.6.0_linux_amd64
proxmox-backup_v1.6.0_linux_amd64.tar.gz
proxmox-backup_v1.6.0_linux_arm64
proxmox-backup_v1.6.0_linux_arm64.tar.gz
proxmox-backup_v1.6.0_linux_amd64.sbom.cdx.json
SHA256SUMS
```

Everything is automatic.

---

# 7. ?? Notes About Prereleases (beta / rc)

GoReleaser automatically marks a release as **Pre-release** if the version tag contains:

- `-beta`
- `-rc`
- any other suffix after the patch

Examples:
- `v1.8.0-beta1` ? prerelease
- `v2.0.0-rc1` ? prerelease
- `v3.1.0` ? stable release

---

# 8. ?? Versioning Rules (SemVer)

Use:

- `fix:` ? patch ? `v1.6.1`
- `feat:` ? minor ? `v1.7.0`
- `feat!:` or `BREAKING CHANGE:` ? major ? `v2.0.0`

These rules will be used in the future by the autotag system (currently disabled).

---

# 9. ?? What **NOT** to do

? Do **not** commit binaries  
? Do **not** push tags from `dev`  
? Do **not** create releases manually in GitHub  
? Do **not** tag old commits — always tag the latest commit from a merged PR  

---

# 10. ?? Optional: Using `install.sh`

If present, users can install your tool with:

```
curl -s https://raw.githubusercontent.com/tis24dev/proxmox-backup/main/install.sh | bash
```

(Optional, but recommended for user convenience.)

---

# 11. Future Automation: Auto-tagging

An `autotag.yml` workflow exists but is currently disabled.
When enabled, it will:

- read commits on `main`
- determine patch/minor/major
- automatically create tags like `v1.6.2`, `v1.7.0` etc.
- trigger GoReleaser automatically

For now, tagging remains manual for maximum control.

---

# ? Summary

The release flow is:

```
1. Work on dev
2. Create PR ? merge to main
3. Create tag on main
4. Push tag
5. GoReleaser builds release
```