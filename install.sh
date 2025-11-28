#!/bin/bash
##
# Proxmox Backup System - Unified Installer
# File: install.sh
# Version: 2.0.5
# Purpose: Error Handling & Interactive Prompt Improvements
##

set -euo pipefail

# ---------------------------------------------------------------------------
# Initialization
# ---------------------------------------------------------------------------

parse_args() {
    VERBOSE_MODE=false
    INSTALL_BRANCH=""

    for arg in "$@"; do
        case "$arg" in
            --verbose)
                VERBOSE_MODE=true
                ;;
            *)
                if [[ "$arg" == "old" ]]; then
                    INSTALL_BRANCH="old"
                else
                    echo -e "\033[0;31m[ERROR]\033[0m This installer is deprecated and only supports the 'old' branch."
                    echo "Please use the new installer and follow the instructions at:"
                    echo "  https://github.com/tis24dev/proxmox-backup"
                    exit 1
                fi
                ;;
        esac
    done

    INSTALL_BRANCH="${INSTALL_BRANCH:-old}"
}

init_constants() {
    RED='\033[0;31m'
    GREEN='\033[0;32m'
    YELLOW='\033[0;33m'
    BLUE='\033[0;34m'
    PURPLE='\033[0;35m'
    CYAN='\033[0;36m'
    BOLD='\033[1m'
    RESET='\033[0m'

    SCRIPT_NAME="Proxmox Backup Installer"
    INSTALLER_VERSION="2.0.5"
    REPO_URL="https://github.com/tis24dev/proxmox-backup"
    INSTALL_DIR="/opt/proxmox-backup"

    GITHUB_RAW_BASE="https://raw.githubusercontent.com/tis24dev/proxmox-backup/${INSTALL_BRANCH}"

    BACKUP_ARCHIVE_PATH=""
    BACKUP_README_PATH=""
    TEMP_PRESERVE_PATH=""
    IS_UPDATE=false
}

print_status()  { echo -e "${BLUE}[INFO]${RESET} $1"; }
print_success() { echo -e "${GREEN}[SUCCESS]${RESET} $1"; }
print_warning() { echo -e "${YELLOW}[WARNING]${RESET} $1"; }
print_error()   { echo -e "${RED}[ERROR]${RESET} $1"; }

print_header() {
    echo -e "${BOLD}${CYAN}============================================${RESET}"
    echo -e "${BOLD}${CYAN}  $SCRIPT_NAME v$INSTALLER_VERSION${RESET}"
    echo -e "${BOLD}${CYAN}============================================${RESET}"
    echo
    echo -e "${BOLD}${BLUE}Selected branch: ${INSTALL_BRANCH}${RESET}"
    if [[ "$INSTALL_BRANCH" == "old" ]]; then
        echo -e "${BOLD}${YELLOW}WARNING: Legacy branch selected (no longer maintained)${RESET}"
    fi
    if [[ "$VERBOSE_MODE" == "true" ]]; then
        echo -e "${BOLD}${YELLOW}Verbose mode enabled${RESET}"
    else
        echo -e "${BOLD}${BLUE}Silent mode - use --verbose for detailed output${RESET}"
    fi
    echo
}

# ---------------------------------------------------------------------------
# Preconditions
# ---------------------------------------------------------------------------

check_root() {
    if [[ $EUID -ne 0 ]]; then
        print_error "This script must be run as root (use sudo)"
        exit 1
    fi
}

confirm_legacy_install() {
    echo
    print_warning "You are about to install a LEGACY version of Proxmox Backup System."
    print_warning "This version is no longer maintained and should not be used for new installations."
    echo
    print_status "For the new installer and up-to-date documentation, please visit:"
    echo "  https://github.com/tis24dev/proxmox-backup"
    echo
    read -p "Continue anyway with the legacy installer? (y/N): " -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        print_error "Installation cancelled by user (legacy installer)."
        exit 0
    fi
    print_success "User confirmed legacy installation"
}

check_remote_branch() {
    local branch="$1"
    print_status "Verifying existence of branch '$branch'..."

    # Check if git is installed
    if ! command -v git >/dev/null 2>&1; then
        print_error "git command not found. Please install git first or run install_dependencies()"
        print_status "This is likely a bug - install_dependencies() should have been called before this function"
        return 1
    fi

    if ! git ls-remote --heads "$REPO_URL" "$branch" 2>/dev/null | grep -q "refs/heads/$branch"; then
        print_error "Branch '$branch' not found on remote repository"
        print_status "Available branches:"
        git ls-remote --heads "$REPO_URL" 2>/dev/null | sed 's/.*refs\/heads\//  - /' || echo "  Unable to list branches"
        return 1
    fi

    print_success "Branch '$branch' exists on remote repository"
    return 0
}

confirm_dev_branch() {
    if [[ "$INSTALL_BRANCH" != "dev" ]]; then
        return
    fi

    echo
    print_warning "You are about to use the DEVELOPMENT branch"
    print_warning "This may contain untested features and bugs"
    echo
    read -p "Continue with dev branch installation? (y/N): " -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        print_error "Installation cancelled by user"
        exit 0
    fi
    print_success "User confirmed development branch"
}

check_requirements() {
    print_status "Checking system requirements..."

    local PVE_DETECTED=false
    local PBS_DETECTED=false

    if [[ -f "/etc/pve/version" ]] || [[ -f "/etc/pve/.version" ]] || [[ -d "/etc/pve" ]]; then
        PVE_DETECTED=true
        print_success "Proxmox VE detected"
    fi

    if [[ -f "/etc/proxmox-backup/version" ]] || [[ -f "/etc/proxmox-backup/.version" ]] || [[ -d "/etc/proxmox-backup" ]]; then
        PBS_DETECTED=true
        print_success "Proxmox Backup Server detected"
    fi

    if [[ -f "/etc/debian_version" ]] && (grep -q "proxmox" /etc/hostname 2>/dev/null || grep -Eq "pve|pbs" /etc/hostname 2>/dev/null); then
        if [[ "$PVE_DETECTED" == false && "$PBS_DETECTED" == false ]]; then
            print_warning "Hostname hints that this may be a Proxmox system"
            PVE_DETECTED=true
        fi
    fi

    if systemctl is-active --quiet pveproxy 2>/dev/null || systemctl is-active --quiet pbs 2>/dev/null; then
        if [[ "$PVE_DETECTED" == false && "$PBS_DETECTED" == false ]]; then
            print_success "Proxmox services detected as running"
            PVE_DETECTED=true
        fi
    fi

    if dpkg -l | grep -q "proxmox-ve\|proxmox-backup-server" 2>/dev/null; then
        if [[ "$PVE_DETECTED" == false && "$PBS_DETECTED" == false ]]; then
            print_success "Proxmox packages detected"
            PVE_DETECTED=true
        fi
    fi

    if [[ "$PVE_DETECTED" == false && "$PBS_DETECTED" == false ]]; then
        print_warning "System does not appear to be Proxmox VE/PBS"
        read -p "Continue anyway? (y/N): " -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            exit 1
        fi
    fi

    local BASH_VERSION_SHORT
    BASH_VERSION_SHORT=$(bash --version | head -n1 | awk '{print $4}' | cut -d'.' -f1-2)
    local REQUIRED_VERSION="4.4"

    if [[ "$(printf '%s\n' "$REQUIRED_VERSION" "$BASH_VERSION_SHORT" | sort -V | head -n1)" != "$REQUIRED_VERSION" ]]; then
        print_error "Bash $REQUIRED_VERSION or higher required. Current: $BASH_VERSION_SHORT"
        exit 1
    fi

    print_success "System requirements satisfied"
}

install_dependencies() {
    print_status "Checking dependencies..."

    # Update package lists first
    print_status "Updating package lists..."
    local apt_output
    if [[ "$VERBOSE_MODE" == "true" ]]; then
        if ! apt update; then
            print_error "Failed to update package lists"
            return 1
        fi
    else
        if ! apt_output=$(apt update 2>&1); then
            print_error "Failed to update package lists"
            echo "$apt_output"
            return 1
        fi
    fi

    # Check and upgrade all packages to latest available versions
    local PACKAGES="curl wget git jq tar gzip xz-utils zstd pigz iptables net-tools iproute2"
    local MISSING_PACKAGES=""
    local UPGRADABLE_PACKAGES=""

    for pkg in $PACKAGES; do
        if ! dpkg -l | grep -q "^ii  $pkg "; then
            MISSING_PACKAGES="$MISSING_PACKAGES $pkg"
        else
            # Check if package has upgradable version
            if apt list --upgradable 2>/dev/null | grep -q "^$pkg/"; then
                UPGRADABLE_PACKAGES="$UPGRADABLE_PACKAGES $pkg"
            fi
        fi
    done

    # Install missing packages
    if [[ -n "$MISSING_PACKAGES" ]]; then
        print_status "Installing missing packages:$MISSING_PACKAGES"
        if [[ "$VERBOSE_MODE" == "true" ]]; then
            if ! apt install -y $MISSING_PACKAGES; then
                print_error "Failed to install missing packages:$MISSING_PACKAGES"
                return 1
            fi
        else
            if ! apt_output=$(apt install -y $MISSING_PACKAGES 2>&1); then
                print_error "Failed to install missing packages:$MISSING_PACKAGES"
                echo "$apt_output"
                return 1
            fi
        fi
        print_success "Missing packages installed"
    fi

    # Upgrade existing packages to latest version
    if [[ -n "$UPGRADABLE_PACKAGES" ]]; then
        print_status "Upgrading packages to latest version:$UPGRADABLE_PACKAGES"
        if [[ "$VERBOSE_MODE" == "true" ]]; then
            if ! apt install --only-upgrade -y $UPGRADABLE_PACKAGES; then
                print_error "Failed to upgrade packages:$UPGRADABLE_PACKAGES"
                return 1
            fi
        else
            if ! apt_output=$(apt install --only-upgrade -y $UPGRADABLE_PACKAGES 2>&1); then
                print_error "Failed to upgrade packages:$UPGRADABLE_PACKAGES"
                echo "$apt_output"
                return 1
            fi
        fi
        print_success "Packages upgraded to latest version"
    fi

    if [[ -z "$MISSING_PACKAGES" && -z "$UPGRADABLE_PACKAGES" ]]; then
        print_success "All required packages already at latest version"
    fi

    # Install/update rclone
    print_status "Checking rclone installation..."

    # Download and execute rclone install script which handles version checks
    local temp_rclone_script
    temp_rclone_script=$(mktemp)

    if ! curl -fsSL https://rclone.org/install.sh -o "$temp_rclone_script"; then
        print_error "Failed to download rclone installer script"
        rm -f "$temp_rclone_script"
        return 1
    fi

    # Execute the installer (it will check if update is needed)
    local rclone_status=0
    local rclone_output
    if [[ "$VERBOSE_MODE" == "true" ]]; then
        bash "$temp_rclone_script" || rclone_status=$?
    else
        rclone_output=$(bash "$temp_rclone_script" 2>&1) || rclone_status=$?
    fi

    rm -f "$temp_rclone_script"

    # Handle exit codes from rclone installer
    # 0 = success (installed/updated)
    # 3 = already at latest version
    # other = error
    case $rclone_status in
        0)
            print_success "rclone installed/updated successfully"
            ;;
        3)
            print_success "rclone is already at the latest version"
            ;;
        *)
            print_error "rclone installation failed with exit code $rclone_status"
            if [[ -n "$rclone_output" ]]; then
                echo "$rclone_output"
            fi
            return 1
            ;;
    esac

    # Install/update rsync
    local rsync_needs_install=false
    local rsync_target_version="3.4.1"
    if ! command -v rsync >/dev/null 2>&1; then
        print_status "rsync not found, will install"
        rsync_needs_install=true
    else
        local current_rsync_version=""
        current_rsync_version=$(rsync --version 2>/dev/null | head -n1 | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -n1) || true
        if [[ -n "$current_rsync_version" ]]; then
            print_status "Current rsync version: $current_rsync_version"
            if [[ "$current_rsync_version" != "$rsync_target_version" ]]; then
                print_status "Updating rsync from $current_rsync_version to $rsync_target_version..."
                rsync_needs_install=true
            else
                print_success "rsync is already at target version $rsync_target_version"
            fi
        else
            print_status "Cannot detect rsync version, will reinstall"
            rsync_needs_install=true
        fi
    fi

    if [[ "$rsync_needs_install" == "true" ]]; then
        print_status "Installing rsync from source..."
        local temp_dir
        temp_dir=$(mktemp -d)
        pushd "$temp_dir" >/dev/null

        # Install build dependencies
        print_status "Installing build dependencies..."
        local build_deps="build-essential gawk autoconf automake python3-cmarkgfm acl libacl1-dev attr libattr1-dev libxxhash-dev libssl-dev libzstd-dev liblz4-dev"
        local build_output
        if [[ "$VERBOSE_MODE" == "true" ]]; then
            if ! apt install -y $build_deps; then
                print_error "Failed to install build dependencies"
                popd >/dev/null
                rm -rf "$temp_dir"
                return 1
            fi
        else
            if ! build_output=$(apt install -y $build_deps 2>&1); then
                print_error "Failed to install build dependencies"
                echo "$build_output"
                popd >/dev/null
                rm -rf "$temp_dir"
                return 1
            fi
        fi

        # Download latest rsync
        print_status "Downloading rsync $rsync_target_version..."
        local rsync_tar="rsync-${rsync_target_version}.tar.gz"
        local rsync_url="https://download.samba.org/pub/rsync/src/${rsync_tar}"

        local wget_output
        if [[ "$VERBOSE_MODE" == "true" ]]; then
            if ! wget "$rsync_url"; then
                print_error "Failed to download rsync source from $rsync_url"
                popd >/dev/null
                rm -rf "$temp_dir"
                return 1
            fi
        else
            if ! wget_output=$(wget -q "$rsync_url" 2>&1); then
                print_error "Failed to download rsync source from $rsync_url"
                echo "$wget_output"
                popd >/dev/null
                rm -rf "$temp_dir"
                return 1
            fi
        fi

        if [[ ! -s "$rsync_tar" ]]; then
            print_error "Downloaded rsync archive is empty or missing"
            popd >/dev/null
            rm -rf "$temp_dir"
            return 1
        fi

        local tar_output
        if [[ "$VERBOSE_MODE" == "true" ]]; then
            if ! tar -xzf "$rsync_tar"; then
                print_error "Failed to extract rsync archive"
                popd >/dev/null
                rm -rf "$temp_dir"
                return 1
            fi
        else
            if ! tar_output=$(tar -xzf "$rsync_tar" 2>&1); then
                print_error "Failed to extract rsync archive"
                echo "$tar_output"
                popd >/dev/null
                rm -rf "$temp_dir"
                return 1
            fi
        fi

        cd "rsync-${rsync_target_version}"

        # Compile and install
        print_status "Compiling rsync..."
        local compile_output
        if [[ "$VERBOSE_MODE" == "true" ]]; then
            if ! ./configure; then
                print_error "rsync ./configure failed"
                popd >/dev/null
                rm -rf "$temp_dir"
                return 1
            fi
            if ! make; then
                print_error "rsync make failed"
                popd >/dev/null
                rm -rf "$temp_dir"
                return 1
            fi
            if ! make install; then
                print_error "rsync make install failed"
                popd >/dev/null
                rm -rf "$temp_dir"
                return 1
            fi
        else
            if ! compile_output=$(./configure 2>&1); then
                print_error "rsync ./configure failed"
                echo "$compile_output"
                popd >/dev/null
                rm -rf "$temp_dir"
                return 1
            fi
            if ! compile_output=$(make 2>&1); then
                print_error "rsync make failed"
                echo "$compile_output"
                popd >/dev/null
                rm -rf "$temp_dir"
                return 1
            fi
            if ! compile_output=$(make install 2>&1); then
                print_error "rsync make install failed"
                echo "$compile_output"
                popd >/dev/null
                rm -rf "$temp_dir"
                return 1
            fi
        fi

        popd >/dev/null
        rm -rf "$temp_dir"

        print_success "rsync installed/updated to version $rsync_target_version"
    fi
}

# ---------------------------------------------------------------------------
# Backup handling
# ---------------------------------------------------------------------------

create_backup() {
    if [[ ! -d "$INSTALL_DIR" ]]; then
        print_status "No existing installation detected; backup not required"
        BACKUP_ARCHIVE_PATH=""
        BACKUP_README_PATH=""
        return 0
    fi

    print_status "Preparing backup of existing installation..."

    if command -v chattr >/dev/null 2>&1; then
        chattr -R -i "$INSTALL_DIR" 2>/dev/null || true
    fi

    local timestamp
    timestamp=$(date +%Y%m%d_%H%M%S)
    BACKUP_ARCHIVE_PATH="/tmp/proxmox-backup-full-${timestamp}.tar.gz"
    BACKUP_README_PATH="/tmp/proxmox-backup-restore-${timestamp}.txt"

    print_status "Creating compressed backup archive..."
    local tar_output
    local tar_status=0
    if [[ "$VERBOSE_MODE" == "true" ]]; then
        tar czf "$BACKUP_ARCHIVE_PATH" -C "$(dirname "$INSTALL_DIR")" "$(basename "$INSTALL_DIR")" || tar_status=$?
    else
        tar_output=$(tar czf "$BACKUP_ARCHIVE_PATH" -C "$(dirname "$INSTALL_DIR")" "$(basename "$INSTALL_DIR")" 2>&1) || tar_status=$?
    fi

    if [[ $tar_status -eq 0 ]]; then
        local size
        size=$(du -h "$BACKUP_ARCHIVE_PATH" | cut -f1)
        print_success "Backup archive created: $BACKUP_ARCHIVE_PATH ($size)"
    else
        print_error "Failed to create backup archive"
        if [[ -n "$tar_output" ]]; then
            echo "$tar_output"
        fi
        BACKUP_ARCHIVE_PATH=""
        return 1
    fi

    cat > "$BACKUP_README_PATH" <<EOF
================================================================================
PROXMOX BACKUP SYSTEM - TEMPORARY BACKUP ARCHIVE
================================================================================
Created: $(date)
Backup File: $BACKUP_ARCHIVE_PATH
Original Location: $INSTALL_DIR

To restore manually:
  rm -rf $INSTALL_DIR
  tar xzf "$BACKUP_ARCHIVE_PATH" -C /opt/
  chown -R root:root $INSTALL_DIR
  chmod -R 755 $INSTALL_DIR
  chmod 600 $INSTALL_DIR/env/backup.env
  ln -sf $INSTALL_DIR/script/proxmox-backup.sh /usr/local/bin/proxmox-backup
  ln -sf $INSTALL_DIR/script/security-check.sh /usr/local/bin/proxmox-backup-security
  ln -sf $INSTALL_DIR/script/fix-permissions.sh /usr/local/bin/proxmox-backup-permissions
  ln -sf $INSTALL_DIR/script/proxmox-restore.sh /usr/local/bin/proxmox-restore
================================================================================
EOF

    print_success "Backup instructions saved: $BACKUP_README_PATH"
    return 0
}

verify_backup() {
    local archive="$1"
    if [[ -z "$archive" || ! -f "$archive" ]]; then
        print_warning "No backup archive to verify"
        return 0
    fi

    print_status "Verifying backup integrity..."
    if ! tar -tzf "$archive" >/dev/null 2>&1; then
        print_error "Backup archive is corrupted or unreadable"
        return 1
    fi

    print_success "Archive integrity test passed"

    local contents
    contents=$(tar -tzf "$archive" 2>/dev/null)
    local missing=0

    if echo "$contents" | grep -F "env/backup.env" >/dev/null 2>&1; then
        print_success "Found env/backup.env"
    else
        print_warning "env/backup.env missing from archive"
        missing=$((missing + 1))
    fi

    if echo "$contents" | grep -F "script/" >/dev/null 2>&1; then
        print_success "Found script/ directory"
    else
        print_warning "script/ directory missing from archive"
        missing=$((missing + 1))
    fi

    if [[ $missing -gt 0 ]]; then
        print_warning "Backup missing some critical files"
        read -p "Continue anyway? (y/N): " -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            return 1
        fi
    fi

    local backup_count original_count
    backup_count=$(echo "$contents" | wc -l)
    original_count=$(find "$INSTALL_DIR" -type f 2>/dev/null | wc -l)

    print_status "Original files: $original_count; archive entries: $backup_count"
    if [[ $backup_count -lt $((original_count / 2)) ]]; then
        print_warning "Archive contains significantly fewer entries than source"
        read -p "Continue anyway? (y/N): " -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            return 1
        fi
    fi

    print_success "Backup verification completed"
    return 0
}

# ---------------------------------------------------------------------------
# Update preservation helpers
# ---------------------------------------------------------------------------

safe_remove_installation() {
    print_status "Preparing update: backing up live files for preservation..."

    cd /tmp
    TEMP_PRESERVE_PATH=$(mktemp -d)

    print_status "Removing immutable attributes before copy..."
    if command -v chattr >/dev/null 2>&1; then
        chattr -R -i "$INSTALL_DIR" 2>/dev/null || true
    fi

    print_status "Creating full copy for selective restore..."
    if cp -a "$INSTALL_DIR" "$TEMP_PRESERVE_PATH/backup"; then
        print_success "Preservation copy created at $TEMP_PRESERVE_PATH/backup"
    else
        print_error "Failed to create preservation copy"
        rm -rf "$TEMP_PRESERVE_PATH"
        exit 1
    fi

    print_status "Removing original installation directory..."
    if rm -rf "$INSTALL_DIR"; then
        print_success "Original installation removed"
    else
        print_error "Failed to remove installation directory"
        exit 1
    fi
}

restore_preserved_files() {
    if [[ -z "$TEMP_PRESERVE_PATH" ]]; then
        return
    fi

    local source_dir="$TEMP_PRESERVE_PATH/backup"
    if [[ ! -d "$source_dir" ]]; then
        return
    fi

    print_status "Restoring preserved files from previous installation..."

    local PRESERVE_PATHS=(
        "config/.server_identity"
        "config/server_id"
        "env/backup.env"
        "secure_account"
        "backup"
        "log"
        "lock"
        "tec-tool"
    )

    for path in "${PRESERVE_PATHS[@]}"; do
        local src="$source_dir/$path"
        if [[ -e "$src" ]]; then
            mkdir -p "$INSTALL_DIR/$(dirname "$path")"
            rm -rf "$INSTALL_DIR/$path" 2>/dev/null || true
            if cp -a "$src" "$INSTALL_DIR/$path"; then
                print_status "Restored: $path"
            else
                print_warning "Failed to restore: $path"
            fi
        fi
    done

    print_success "Preserved files restored"
}

cleanup_temp_artifacts() {
    if [[ -n "${TEMP_PRESERVE_PATH:-}" ]]; then
        rm -rf "$TEMP_PRESERVE_PATH" 2>/dev/null || true
    fi

    rm -f /tmp/proxmox_backup_was_update 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# Reinstall helpers
# ---------------------------------------------------------------------------

remove_existing_installation() {
    if [[ ! -d "$INSTALL_DIR" ]]; then
        return
    fi

    print_status "Removing existing installation (full purge)..."

    if crontab -l 2>/dev/null | grep -q "proxmox-backup"; then
        print_status "Removing cron jobs..."
        crontab -l 2>/dev/null | grep -v "proxmox-backup" | crontab - || true
    fi

    print_status "Removing system symlinks..."
    rm -f /usr/local/bin/proxmox-backup 2>/dev/null || true
    rm -f /usr/local/bin/proxmox-backup-security 2>/dev/null || true
    rm -f /usr/local/bin/proxmox-backup-permissions 2>/dev/null || true
    rm -f /usr/local/bin/proxmox-restore 2>/dev/null || true

    print_status "Cleaning immutable attributes..."
    if command -v chattr >/dev/null 2>&1; then
        find "$INSTALL_DIR" -type f -exec chattr -i {} \; 2>/dev/null || true
    fi

    chown -R root:root "$INSTALL_DIR" 2>/dev/null || true

    if rm -rf "$INSTALL_DIR"; then
        print_success "Installation directory removed"
    else
        print_error "Failed to remove installation directory"
        exit 1
    fi
}

# ---------------------------------------------------------------------------
# Repository / configuration
# ---------------------------------------------------------------------------

clone_repository() {
    print_status "Cloning repository (branch: $INSTALL_BRANCH)..."
    local git_output
    if [[ "$VERBOSE_MODE" == "true" ]]; then
        if ! git clone -b "$INSTALL_BRANCH" "$REPO_URL" "$INSTALL_DIR"; then
            print_error "Repository clone failed"
            exit 1
        fi
    else
        if ! git_output=$(git clone -q -b "$INSTALL_BRANCH" "$REPO_URL" "$INSTALL_DIR" 2>&1); then
            print_error "Repository clone failed"
            echo "$git_output"
            exit 1
        fi
    fi
    chmod 744 "$INSTALL_DIR/install.sh" 2>/dev/null || true

    if [[ ! -d "$INSTALL_DIR" ]]; then
        print_error "Repository clone failed - directory not created"
        exit 1
    fi
    if [[ ! -d "$INSTALL_DIR/.git" ]]; then
        print_error "Repository clone incomplete (missing .git directory)"
        exit 1
    fi

    pushd "$INSTALL_DIR" >/dev/null
    local cloned_branch
    cloned_branch=$(git branch --show-current)
    if [[ "$cloned_branch" != "$INSTALL_BRANCH" ]]; then
        print_warning "Expected branch $INSTALL_BRANCH but got $cloned_branch"
    fi
    popd >/dev/null

    print_success "Repository cloned"
}

add_storage_monitoring_config() {
    local config_file="$INSTALL_DIR/env/backup.env"
    [[ -f "$config_file" ]] || return 0

    if grep -q "STORAGE_WARNING_THRESHOLD_PRIMARY" "$config_file"; then
        return 0
    fi

    print_status "Adding storage monitoring configuration..."
    cp "$config_file" "${config_file}.backup.$(date +%Y%m%d_%H%M%S)"

    awk '
        /^# 3\. PATHS AND STORAGE CONFIGURATION$/ { found=1 }
        found && /^# =+$/ && !inserted {
            print
            print ""
            print "# ---------- Storage Monitoring ----------"
            print "STORAGE_WARNING_THRESHOLD_PRIMARY=\"90\""
            print "STORAGE_WARNING_THRESHOLD_SECONDARY=\"90\""
            inserted=1
            next
        }
        { print }
    ' "$config_file" > "${config_file}.tmp" && mv "${config_file}.tmp" "$config_file"

    print_success "Storage thresholds added"
}

update_blacklist_config() {
    local config_file="$INSTALL_DIR/env/backup.env"
    [[ -f "$config_file" ]] || return 0

    local required_entries=("/root/.npm" "/root/.dotnet" "/root/.local" "/root/.gnupg")
    local all_present=true
    for entry in "${required_entries[@]}"; do
        if ! grep -q "^${entry}\$" "$config_file"; then
            all_present=false
            break
        fi
    done

    if [[ "$all_present" == "true" ]]; then
        return 0
    fi

    print_status "Updating blacklist configuration..."
    cp "$config_file" "${config_file}.backup.$(date +%Y%m%d_%H%M%S)"

    awk '
        /^\/root\/\.\*$/ { next }
        /^\/root\/\.npm$/ { next }
        /^\/root\/\.dotnet$/ { next }
        /^\/root\/\.local$/ { next }
        /^\/root\/\.gnupg$/ { next }
        /^\/root\/\.cache$/ {
            print
            print "/root/.npm"
            print "/root/.dotnet"
            print "/root/.local"
            print "/root/.gnupg"
            next
        }
        { print }
    ' "$config_file" > "${config_file}.tmp" && mv "${config_file}.tmp" "$config_file"

    print_success "Blacklist configuration updated"
}

update_email_config() {
    local config_file="$INSTALL_DIR/env/backup.env"
    [[ -f "$config_file" ]] || return 0

    if grep -q '# Email delivery method: "relay" (Cloud relay) or "sendmail" (local SMTP)' "$config_file"; then
        return 0
    fi

    print_status "Updating email configuration..."
    cp "$config_file" "${config_file}.backup.$(date +%Y%m%d_%H%M%S)"

    local existing_recipient
    existing_recipient=$(grep "^EMAIL_RECIPIENT=" "$config_file" 2>/dev/null | sed -n 's/^EMAIL_RECIPIENT="\([^#"]*\).*/\1/p' | sed 's/[[:space:]]*$//' || true)

    awk -v recipient="$existing_recipient" '
        /^# ---------- Email Configuration ----------$/ {
            print "# ---------- Email Configuration ----------"
            print "# Email delivery method: \"relay\" (Cloud relay) or \"sendmail\" (local SMTP)"
            print "# Note: \"ses\" is deprecated but still supported"
            print "EMAIL_DELIVERY_METHOD=\"relay\""
            print ""
            print "# Fallback to sendmail if cloud relay fails"
            print "EMAIL_FALLBACK_SENDMAIL=\"true\""
            print ""
            print "# Email recipient (if empty, uses root email from Proxmox)"
            print "EMAIL_RECIPIENT=\"" recipient "\""
            print ""
            print "# Email sender (configured in Worker)"
            print "EMAIL_FROM=\"no-reply@proxmox.tis24.it\""
            print ""
            print "# Email subject prefix"
            print "EMAIL_SUBJECT_PREFIX=\"[Proxmox-Backup]\""
            print ""
            skip=1
            next
        }
        skip {
            if (/^# =============/ || /^# ---------- Prometheus Configuration ----------$/) {
                skip=0
                print
            }
            next
        }
        { print }
    ' "$config_file" > "${config_file}.tmp" && mv "${config_file}.tmp" "$config_file"

    print_success "Email configuration updated"
}

ensure_cloud_timeout_config() {
    local config_file="$INSTALL_DIR/env/backup.env"
    [[ -f "$config_file" ]] || return 0

    if grep -q "^CLOUD_CONNECTIVITY_TIMEOUT=" "$config_file"; then
        return 0
    fi

    print_status "Adding cloud connectivity timeout placeholder..."
    cp "$config_file" "${config_file}.backup.$(date +%Y%m%d_%H%M%S)"

    awk '
        /^[[:space:]]*RCLONE_FLAGS=/ {
            print
            print ""
            print "# Timeout (in seconds) for cloud connectivity tests"
            print "CLOUD_CONNECTIVITY_TIMEOUT=30"
            inserted=1
            next
        }
        { print }
        END {
            if (!inserted) {
                print ""
                print "# Timeout (in seconds) for cloud connectivity tests"
                print "CLOUD_CONNECTIVITY_TIMEOUT=30"
            }
        }
    ' "$config_file" > "${config_file}.tmp" && mv "${config_file}.tmp" "$config_file"

    print_success "Default cloud connectivity timeout added"
}

remove_packages_config() {
    local config_file="$INSTALL_DIR/env/backup.env"
    [[ -f "$config_file" ]] || return 0

    # Check if REQUIRED_PACKAGES or OPTIONAL_PACKAGES still exist in config
    if ! grep -q "^REQUIRED_PACKAGES=" "$config_file" && ! grep -q "^OPTIONAL_PACKAGES=" "$config_file"; then
        return 0
    fi

    print_status "Removing obsolete package configuration from backup.env..."
    cp "$config_file" "${config_file}.backup.$(date +%Y%m%d_%H%M%S)"

    awk '
        /^# Required packages for system operation$/ { next }
        /^REQUIRED_PACKAGES=/ { next }
        /^OPTIONAL_PACKAGES=/ { next }
        { print }
    ' "$config_file" > "${config_file}.tmp" && mv "${config_file}.tmp" "$config_file"

    print_success "Package configuration removed from backup.env"
}

get_reference_config_header() {
    cat <<'EOF'
#!/bin/bash
# ============================================================================
# PROXMOX BACKUP SYSTEM - MAIN CONFIGURATION
# File: backup.env
# Version: 1.3.1
# Last Modified: 2025-11-05
# Changes: Cloud Connectivity Timeout Improvements
# ============================================================================
# Main configuration file for Proxmox backup system
# This file contains all configurations needed for automated backup
# of PVE (Proxmox Virtual Environment) and PBS (Proxmox Backup Server)
#
# IMPORTANT:
# - This file must have 600 permissions and be owned by root
# - Always verify configuration before running backups in production
# - Keep backup copies of this configuration file
# ============================================================================
EOF
}

update_config_header() {
    local config_file="$INSTALL_DIR/env/backup.env"
    [[ -f "$config_file" ]] || return 0

    if ! grep -q "# 1. GENERAL SYSTEM CONFIGURATION" "$config_file"; then
        print_warning "Configuration format not recognized; skipping header update"
        return 0
    fi

    local reference_header
    reference_header=$(get_reference_config_header)

    local current_header
    current_header=$(head -n 20 "$config_file")
    if [[ "$current_header" == "$reference_header" ]]; then
        return 0
    fi

    print_status "Updating configuration header..."
    cp "$config_file" "${config_file}.backup.$(date +%Y%m%d_%H%M%S)"

    awk '
        /^# ============================================================================$/ {
            separator_line = $0
            separator_lineno = NR
        }
        /^# 1\. GENERAL SYSTEM CONFIGURATION$/ {
            # Print blank line before the separator section
            print ""
            # Print the separator that came before this line
            if (separator_lineno == NR - 1) {
                print separator_line
            }
            found=1
        }
        found { print }
    ' "$config_file" > "${config_file}.body.tmp"

    if [[ ! -s "${config_file}.body.tmp" ]]; then
        print_error "Failed to extract configuration body"
        rm -f "${config_file}.body.tmp"
        return 1
    fi

    get_reference_config_header > "${config_file}.tmp"

    cat "${config_file}.body.tmp" >> "${config_file}.tmp"
    mv "${config_file}.tmp" "$config_file"
    rm -f "${config_file}.body.tmp"

    print_success "Configuration header updated"
}

create_default_configuration() {
    local config_file="$INSTALL_DIR/env/backup.env"
    if [[ -f "$config_file" ]]; then
        return
    fi

    print_warning "Configuration file missing; creating default template"
    mkdir -p "$INSTALL_DIR/env"
    get_reference_config_header > "$config_file"
    cat >> "$config_file" <<'EOF'

# ============================================================================
# 1. GENERAL SYSTEM CONFIGURATION
# ============================================================================

# Minimum required Bash version
MIN_BASH_VERSION="4.4.0"

# Debug level: "standard", "advanced" (-v), "extreme" (-x)
DEBUG_LEVEL="standard"

# Disable colors in output (useful for logs or terminals that don't support colors)
DISABLE_COLORS="false"

# ============================================================================
# 2. MAIN FEATURES - ENABLE/DISABLE
# ============================================================================

# ---------- Backup Features ----------
# General system backup
BACKUP_INSTALLED_PACKAGES="true"        # List of installed packages
BACKUP_SCRIPT_DIR="true"                # Scripts directory
BACKUP_CRONTABS="true"                  # Cron tables
BACKUP_ZFS_CONFIG="true"                # ZFS configuration
BACKUP_CRITICAL_FILES="true"            # Critical system files
BACKUP_NETWORK_CONFIG="true"            # Network configuration
BACKUP_REMOTE_CFG="true"                # Remote configurations

# PVE-specific backup (Proxmox Virtual Environment)
BACKUP_CLUSTER_CONFIG="true"            # Cluster configuration /etc/pve
BACKUP_COROSYNC_CONFIG="true"           # Corosync configuration
BACKUP_PVE_FIREWALL="true"              # PVE firewall rules
BACKUP_VM_CONFIGS="true"                # VM/Container configurations
BACKUP_VZDUMP_CONFIG="true"             # vzdump configuration
BACKUP_CEPH_CONFIG="true"               # Ceph configuration (if present)

# PVE job information backup
BACKUP_PVE_JOBS="true"                  # PVE backup job information
BACKUP_PVE_SCHEDULES="true"             # Scheduled tasks and cron jobs
BACKUP_PVE_REPLICATION="true"           # Replication information

# PBS backup (Proxmox Backup Server)
BACKUP_PXAR_FILES="true"                # PXAR files
BACKUP_SMALL_PXAR="true"                # Small PXAR files
BACKUP_PVE_BACKUP_FILES="true"          # Detailed analysis of PVE backup files
BACKUP_SMALL_PVE_BACKUPS="false"        # Copy small PVE backup files (enable only if needed)

# ---------- Storage Features ----------
# Multiple backups
ENABLE_SECONDARY_BACKUP="false"         # Local secondary backup
ENABLE_CLOUD_BACKUP="false"             # Cloud backup
SECONDARY_BACKUP_REQUIRED="false"       # Secondary backup mandatory
CLOUD_BACKUP_REQUIRED="false"           # Cloud backup mandatory

# Parallel processing
MULTI_STORAGE_PARALLEL="false"          # Parallel processing on multiple storage

# ---------- Security Features ----------
# Security checks
ABORT_ON_SECURITY_ISSUES="false"        # Abort backup if security issues found
AUTO_UPDATE_HASHES="true"               # Automatically update hashes
REMOVE_UNAUTHORIZED_FILES="false"       # Remove unauthorized files
CHECK_NETWORK_SECURITY="false"          # Verify network security
CHECK_FIREWALL="false"                  # Verify firewall configuration
CHECK_OPEN_PORTS="false"                # Check open ports
FULL_SECURITY_CHECK="true"              # Complete security check

# ---------- Advanced Compression Features ----------
# Deduplication - replaces duplicate files with symlinks to save space
# WARNING: Set to "false" if experiencing backup issues
ENABLE_DEDUPLICATION="false"

# Preprocessor - optimizes files before compression
# Processes text files, logs, JSON and configuration files to improve compression
ENABLE_PREFILTER="true"

# Smart chunking - splits very large files to improve compression
# Useful for databases and large binary files but may slow down backup
ENABLE_SMART_CHUNKING="true"

# ---------- Monitoring Features ----------
# Log management
ENABLE_LOG_MANAGEMENT="true"            # Automatic log management
ENABLE_EMOJI_LOG="true"                 # Emojis in logs for better readability

# Notifications
TELEGRAM_ENABLED="true"                 # Telegram notifications
EMAIL_ENABLED="true"                    # Email notifications

# Metrics
PROMETHEUS_ENABLED="true"               # Prometheus metrics export

# ---------- Permission Management ----------
SET_BACKUP_PERMISSIONS="true"           # Set backup permissions

# ============================================================================
# 3. PATHS AND STORAGE CONFIGURATION
# ============================================================================

# ---------- Storage Monitoring ----------
# Warning thresholds for storage space usage (percentage)
# Script will generate warnings and set EXIT_CODE=1 when storage usage exceeds these thresholds
STORAGE_WARNING_THRESHOLD_PRIMARY="90"
STORAGE_WARNING_THRESHOLD_SECONDARY="90"

# ---------- Automatic Detection ----------
# Automatic detection of datastores from PBS and PVE systems
# Set to "false" to use only manual PBS_DATASTORE_PATH configuration
AUTO_DETECT_DATASTORES="true"

# ---------- Backup Paths ----------
# Local backup path (primary)
LOCAL_BACKUP_PATH="${BASE_DIR}/backup"

# Secondary backup path (external) # Write your secondary path
SECONDARY_BACKUP_PATH=""

# Cloud backup path
CLOUD_BACKUP_PATH="/proxmox-backup/backup"

# ---------- Log Paths ----------
# Local log path
LOCAL_LOG_PATH="${BASE_DIR}/log/"

# Secondary log path # Write your secondary path
SECONDARY_LOG_PATH=""

# Cloud log path
CLOUD_LOG_PATH="/proxmox-backup/log"

# ---------- Retention Policy ----------
# Maximum number of backups to keep
MAX_LOCAL_BACKUPS=20
MAX_SECONDARY_BACKUPS=20
MAX_CLOUD_BACKUPS=20

# Maximum number of logs to keep
MAX_LOCAL_LOGS=20
MAX_SECONDARY_LOGS=20
MAX_CLOUD_LOGS=20

# ---------- Custom Paths ----------
# Custom PBS and PVE paths
PBS_DATASTORE_PATH=""
PVE_CONFIG_PATH="/etc/pve"
PVE_CLUSTER_PATH="/var/lib/pve-cluster"
COROSYNC_CONFIG_PATH="/etc/corosync"
VZDUMP_CONFIG_PATH="/etc/vzdump.conf"
CEPH_CONFIG_PATH="/etc/ceph"

# ============================================================================
# 4. COMPRESSION CONFIGURATION
# ============================================================================

# Compression type
# - "zstd": Fast, good speed/compression balance
# - "xz": Better compression, slower
# - "gzip"/"pigz": Compatible, standard
COMPRESSION_TYPE="xz"

# Compression level (1=fast, 9=maximum compression)
COMPRESSION_LEVEL="9"

# Compression mode
# - "fast": Fast, basic compression
# - "standard": Balanced
# - "maximum": Maximum compression, slower
# - "ultra": Extreme compression, very slow
COMPRESSION_MODE="ultra"

# Compression threads
# - 0: Automatic (uses all available cores)
# - 1: Single-thread
# - N: Specific number of threads
COMPRESSION_THREADS="0"

# ============================================================================
# 5. CLOUD AND RCLONE CONFIGURATION
# ============================================================================

# ---------- rclone Configuration ----------
# Configured rclone remote name
RCLONE_REMOTE="gdrive"

# Bandwidth limit for rclone
RCLONE_BANDWIDTH_LIMIT="10M"

# Additional rclone flags
RCLONE_FLAGS="--transfers=16 --checkers=4 --stats=0 --drive-use-trash=false --drive-pacer-min-sleep=10ms --drive-pacer-burst=100"

# Timeout (in seconds) for test
CLOUD_CONNECTIVITY_TIMEOUT=30

# ---------- Cloud Upload Mode ----------
# Upload mode: "parallel" (recommended) or "sequential" (traditional)
# - Parallel: Uploads backup, checksum and log simultaneously
# - Sequential: Uploads files one by one
CLOUD_UPLOAD_MODE="parallel"

# Maximum number of parallel jobs for cloud upload (recommended: 3)
# Higher values may cause rate limiting on cloud providers
CLOUD_PARALLEL_MAX_JOBS="3"

# Parallel verification of uploaded files
# - true: Verify all files simultaneously (faster)
# - false: Verify files sequentially (slower but more reliable)
CLOUD_PARALLEL_VERIFICATION="true"

# Timeout for parallel uploads (in seconds)
CLOUD_PARALLEL_UPLOAD_TIMEOUT="600"

# ---------- Cloud Upload Verification ----------
# Skip upload verification (use only if experiencing persistent verification issues)
# - true: Completely disable verification (faster but less reliable)
# - false: Perform verification with retry logic (default, recommended)
SKIP_CLOUD_VERIFICATION="false"

# ============================================================================
# 6. NOTIFICATIONS CONFIGURATION
# ============================================================================

# ---------- Telegram Configuration ----------
# Tokens
TELEGRAM_BOT_TOKEN="" # For personal mode
TELEGRAM_CHAT_ID="" # For personal mode

# Bot type: "personal" or "centralized"
BOT_TELEGRAM_TYPE="centralized"

# Custom Telegram API server
TELEGRAM_SERVER_API_HOST="https://bot.tis24.it:1443" # Port 1443 must be opened on 433 of telegram server

# ---------- Email Configuration ----------
# Email delivery method: "relay" (Cloud relay) or "sendmail" (local SMTP)
# Note: "ses" is deprecated but still supported for backward compatibility (treated as "relay")
EMAIL_DELIVERY_METHOD="relay"

# Fallback to sendmail if cloud relay fails (rate limit, network error, etc.)
EMAIL_FALLBACK_SENDMAIL="true"

# Email recipient (if empty, uses root email from Proxmox)
EMAIL_RECIPIENT=""

# Email sender (configured in Worker, cannot be overridden)
# This value is informational only - Worker uses no-reply@proxmox.tis24.it
EMAIL_FROM="no-reply@proxmox.tis24.it"

# Email subject prefix
EMAIL_SUBJECT_PREFIX="[Proxmox-Backup]"

# ============================================================================
# 7. PROMETHEUS CONFIGURATION
# ============================================================================

# Directory for Prometheus node-exporter text files
PROMETHEUS_TEXTFILE_DIR="/var/lib/prometheus/node-exporter"

# ============================================================================
# 8. USERS AND PERMISSIONS CONFIGURATION
# ============================================================================

# Backup user and group
BACKUP_USER="backup"
BACKUP_GROUP="backup"

# ============================================================================
# 9. CUSTOM CONFIGURATIONS
# ============================================================================

# ---------- Custom Backup Paths ----------
# List of additional paths to include in backup
# One path per line, enclosed in quotes
CUSTOM_BACKUP_PATHS="
/root/.config/rclone/rclone.conf
/etc/apt/
/etc/gshadow
/etc/shadow
/etc/group
/root
"

# ---------- Backup Blacklist ----------
# Paths to exclude from backup
# One path per line, supports patterns and variables
# Supported formats:
#   - Exact path: /root/.cache (excludes /root/.cache and all subdirectories)
#   - Glob pattern: /root/.* (excludes all hidden files/folders in /root)
#   - Wildcard: *_cacache* (excludes any path containing "_cacache")
#   - Variables: ${BASE_DIR}/log/ (variables are expanded)
BACKUP_BLACKLIST="
/etc/proxmox-backup/.debug
/etc/proxmox-backup/.tmp
/etc/proxmox-backup/.lock
/etc/proxmox-backup/tasks
/root/.bash_history
/root/.cache
/root/.npm
/root/.dotnet
/root/.local
/root/.gnupg
${BASE_DIR}/log/
${BASE_DIR}/backup/
"

# ---------- PXAR Options ----------
# Maximum size for small PXAR files
MAX_PXAR_SIZE="50M"

# Pattern to include specific PXAR files
PXAR_INCLUDE_PATTERN="vm/100,vm/101"

# ---------- PVE Backup Options ----------
# Maximum size for small PVE backup files to copy
MAX_PVE_BACKUP_SIZE="100M"

# Pattern to include specific PVE backup files (e.g., "vm-100-", "ct-101-")
PVE_BACKUP_INCLUDE_PATTERN=""

# ============================================================================
# 10. ANSI COLORS FOR OUTPUT
# ============================================================================
# Color definitions for terminal output
# Used to improve readability of logs and messages

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
PURPLE='\033[0;35m'
CYAN='\033[0;36m'
GRAY='\033[0;37m'
PINK='\033[38;5;213m'
BOLD='\033[1m'
RESET='\033[0m'

# ============================================================================
# END OF CONFIGURATION
# ============================================================================
# For additional support and documentation, consult:
# - Project README.md
# - Official Proxmox documentation
# - rclone documentation for cloud configurations
# ============================================================================

#END
EOF

    print_success "Default configuration created"
}

setup_configuration() {
    pushd "$INSTALL_DIR" >/dev/null

    if [[ "$IS_UPDATE" == true ]]; then
        print_status "Applying configuration migrations..."
        update_config_header
        add_storage_monitoring_config
        update_blacklist_config
        update_email_config
        ensure_cloud_timeout_config
        remove_packages_config
    fi

    create_default_configuration

    popd >/dev/null
    print_success "Configuration setup completed"
}

# ---------------------------------------------------------------------------
# Finalization helpers
# ---------------------------------------------------------------------------

set_permissions() {
    print_status "Setting permissions..."
    pushd "$INSTALL_DIR" >/dev/null
    chmod +x script/*.sh 2>/dev/null || true
    chmod +x lib/*.sh 2>/dev/null || true
    chmod 600 env/backup.env 2>/dev/null || true
    mkdir -p backup log config secure_account lock
    chown -R root:root "$INSTALL_DIR"
    popd >/dev/null
    print_success "Permissions configured"
}

run_fix_permissions() {
    print_status "Running fix-permissions script..."
    pushd "$INSTALL_DIR" >/dev/null
    if [[ -f "script/fix-permissions.sh" ]]; then
        if [[ "$VERBOSE_MODE" == "true" ]]; then
            ./script/fix-permissions.sh
        else
            ./script/fix-permissions.sh >/dev/null
        fi
        print_success "Permissions fixed"
    else
        print_warning "fix-permissions.sh not found; skipping"
    fi
    popd >/dev/null
}

run_security_check() {
    print_status "Running security check..."
    pushd "$INSTALL_DIR" >/dev/null
    if [[ -f "script/security-check.sh" ]]; then
        if [[ "$VERBOSE_MODE" == "true" ]]; then
            if ./script/security-check.sh; then
                print_success "Security check passed"
            else
                print_warning "Security issues detected; continuing installation"
            fi
        else
            if ./script/security-check.sh >/dev/null; then
                print_success "Security check passed"
            else
                print_warning "Security issues detected; continuing installation"
            fi
        fi
    else
        print_warning "security-check.sh not found; skipping"
    fi
    popd >/dev/null
}

setup_cron() {
    print_status "Configuring cron job..."
    if crontab -l 2>/dev/null | grep -q "proxmox-backup"; then
        print_warning "Cron job already present; skipping"
        return
    fi

    local temp_cron
    temp_cron=$(mktemp)
    crontab -l 2>/dev/null > "$temp_cron" || true
    echo "0 2 * * * /usr/local/bin/proxmox-backup >/dev/null 2>&1" >> "$temp_cron"
    crontab "$temp_cron"
    rm -f "$temp_cron"

    print_success "Cron job installed (daily at 02:00)"
}

create_symlinks() {
    print_status "Creating system symlinks..."
    ln -sf "$INSTALL_DIR/script/proxmox-backup.sh" /usr/local/bin/proxmox-backup
    ln -sf "$INSTALL_DIR/script/security-check.sh" /usr/local/bin/proxmox-backup-security
    ln -sf "$INSTALL_DIR/script/fix-permissions.sh" /usr/local/bin/proxmox-backup-permissions
    ln -sf "$INSTALL_DIR/script/proxmox-restore.sh" /usr/local/bin/proxmox-restore
    print_success "Symlinks created"
}

run_first_backup() {
    print_status "Running initial dry-run backup..."
    pushd "$INSTALL_DIR" >/dev/null
    if [[ -f "script/proxmox-backup.sh" ]]; then
        if [[ "$VERBOSE_MODE" == "true" ]]; then
            if ./script/proxmox-backup.sh --dry-run; then
                print_success "Dry-run backup completed"
            else
                print_warning "Dry-run encountered issues (expected for fresh setup)"
            fi
        else
            if ./script/proxmox-backup.sh --dry-run >/dev/null; then
                print_success "Dry-run backup completed"
            else
                print_warning "Dry-run encountered issues (expected for fresh setup)"
            fi
        fi
    else
        print_warning "Main backup script missing; skip dry-run"
    fi
    popd >/dev/null
}

protect_identity_file() {
    local identity_file="$INSTALL_DIR/config/.server_identity"
    if [[ -f "$identity_file" ]] && command -v chattr >/dev/null 2>&1; then
        print_status "Protecting server identity file..."
        if chattr +i "$identity_file"; then
            print_success "Server identity set to immutable"
        else
            print_warning "Failed to protect server identity file"
        fi
    fi
}

show_completion() {
    local action="$1"
    echo
    echo "================================================"
    if [[ "$action" == "update" ]]; then
        print_success "🎉 UPDATE COMPLETED SUCCESSFULLY 🎉"
    else
        print_success "🎉 FRESH INSTALLATION COMPLETED 🎉"
    fi
    echo "================================================"
    echo
    echo -e "${BOLD}${GREEN}Next steps:${RESET}"
    echo -e "1. ${CYAN}Edit configuration:${RESET} nano $INSTALL_DIR/env/backup.env"
    echo -e "2. ${CYAN}Run first backup:${RESET} $INSTALL_DIR/script/proxmox-backup.sh"
    echo -e "3. ${CYAN}Check logs:${RESET} tail -f $INSTALL_DIR/log/*.log"

    local unique_code=""
    if [[ -f "$INSTALL_DIR/config/.server_identity" ]]; then
        local encoded
        encoded=$(grep "SYSTEM_CONFIG_DATA=" "$INSTALL_DIR/config/.server_identity" 2>/dev/null | cut -d'"' -f2)
        if [[ -n "$encoded" ]]; then
            local decoded
            decoded=$(echo "$encoded" | base64 -d 2>/dev/null || true)
            if [[ -n "$decoded" ]]; then
                unique_code=$(echo "$decoded" | cut -d':' -f1)
            fi
        fi
    fi

    if [[ -n "$unique_code" ]]; then
        echo -e "4. ${CYAN}Telegram:${RESET} Open @ProxmoxAN_bot and enter code: ${BOLD}${YELLOW}$unique_code${RESET}"
    else
        echo -e "4. ${CYAN}Telegram:${RESET} Open @ProxmoxAN_bot and enter your unique code"
    fi
    echo
    echo -e "${BOLD}${YELLOW}Documentation:${RESET}"
    echo -e "- $INSTALL_DIR/doc/README.md"
    echo -e "- $INSTALL_DIR/doc/CONFIGURATION.md"
    echo
    echo -e "${BOLD}${PURPLE}Quick commands:${RESET}"
    echo -e "- Backup: ${CYAN}proxmox-backup${RESET}"
    echo -e "- Test mode: ${CYAN}proxmox-backup --dry-run${RESET}"
    echo -e "- Security: ${CYAN}proxmox-backup-security${RESET}"
    echo -e "- Permissions: ${CYAN}proxmox-backup-permissions${RESET}"
    echo -e "- Restore: ${CYAN}proxmox-restore${RESET}"
    echo
    if [[ -n "$BACKUP_ARCHIVE_PATH" && -f "$BACKUP_ARCHIVE_PATH" ]]; then
        local size
        size=$(du -h "$BACKUP_ARCHIVE_PATH" 2>/dev/null | cut -f1)
        echo -e "${BOLD}${YELLOW}Temporary backup archive:${RESET} $BACKUP_ARCHIVE_PATH (${size:-unknown})"
        echo -e "${BOLD}${YELLOW}Restore instructions:${RESET} ${BACKUP_README_PATH:-N/A}"
        echo -e "${RED}Backup stored in /tmp/; copy it elsewhere to keep it after reboot.${RESET}"
    fi
    echo
}

# ---------------------------------------------------------------------------
# Interaction helpers
# ---------------------------------------------------------------------------

detect_install_state() {
    if [[ -d "$INSTALL_DIR" ]]; then
        echo "existing"
    else
        echo "fresh"
    fi
}

prompt_install_action() {
    local choice
    while true; do
        echo >/dev/tty
        print_status "Choose installation mode:" >/dev/tty
        echo "  [1] Update existing installation (preserve data)" >/dev/tty
        echo "  [2] Reinstall from scratch (REMOVE-EVERYTHING)" >/dev/tty
        echo "  [3] Exit" >/dev/tty
        read -p "Select option (1-3): " -r choice </dev/tty
        case "$choice" in
            1|2|3)
                echo "$choice"
                return
                ;;
            *)
                print_warning "Invalid choice. Please select 1, 2 or 3." >/dev/tty
                ;;
        esac
    done
}

confirm_update() {
    read -p "Confirm update preserving data? (y/N): " -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        print_error "Update cancelled by user"
        exit 1
    fi
}

confirm_reinstall() {
    echo -e "${BOLD}${RED}Type REMOVE-EVERYTHING to confirm full reinstall:${RESET}"
    read -p "Confirmation: " -r confirmation
    echo
    if [[ "$confirmation" != "REMOVE-EVERYTHING" ]]; then
        print_error "Reinstall cancelled - incorrect confirmation"
        exit 1
    fi
}

prompt_telegram_notifications() {
    local config_file="$INSTALL_DIR/env/backup.env"

    # Skip if config file doesn't exist
    [[ -f "$config_file" ]] || return 0

    echo
    print_status "Telegram Notifications Setup"
    read -p "Enable Telegram notifications? (takes only a few seconds) (y/N): " -r || true
    echo

    # Create backup before modification
    local backup_file="${config_file}.backup.$(date +%Y%m%d_%H%M%S)"
    cp "$config_file" "$backup_file" 2>/dev/null || true

    if [[ $REPLY =~ ^[Yy]$ ]]; then
        # Enable Telegram
        sed -i 's/^TELEGRAM_ENABLED=.*/TELEGRAM_ENABLED="true"/' "$config_file"
        print_success "Telegram notifications enabled"
    else
        # Disable Telegram
        sed -i 's/^TELEGRAM_ENABLED=.*/TELEGRAM_ENABLED="false"/' "$config_file"
        print_status "Telegram notifications disabled"
    fi
}

# ---------------------------------------------------------------------------
# Error handling
# ---------------------------------------------------------------------------

error_handler() {
    local cmd="$1"
    print_error "Installation failed at: $cmd"
    print_error "Check the output above for more information"
    cleanup_temp_artifacts
    exit 1
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

main() {
    parse_args "$@"
    init_constants
    print_header

    trap 'error_handler "${BASH_COMMAND}"' ERR

    check_root
    confirm_legacy_install

    if ! cd "$(dirname "$INSTALL_DIR")" 2>/dev/null; then
        cd /
    fi
    print_status "Working directory: $(pwd)"

    check_requirements
    install_dependencies

    if ! check_remote_branch "$INSTALL_BRANCH"; then
        exit 1
    fi

    local state action
    state=$(detect_install_state)

    if [[ "$state" == "existing" ]]; then
        action=$(prompt_install_action)
        case "$action" in
            1)
                confirm_update
                ;;
            2)
                confirm_reinstall
                ;;
            3)
                print_warning "Operation cancelled by user"
                exit 0
                ;;
        esac
    else
        action=2
        print_status "No existing installation detected - proceeding with fresh install"
    fi


    if ! create_backup; then
        print_error "Backup creation failed. Aborting."
        exit 1
    fi

    if ! verify_backup "$BACKUP_ARCHIVE_PATH"; then
        print_error "Backup verification failed. Aborting."
        exit 1
    fi

    if [[ "$state" == "existing" && "$action" == "1" ]]; then
        IS_UPDATE=true
        touch /tmp/proxmox_backup_was_update
        safe_remove_installation
    else
        remove_existing_installation
    fi

    clone_repository

    if [[ "$IS_UPDATE" == true ]]; then
        restore_preserved_files
    fi

    setup_configuration
    prompt_telegram_notifications
    set_permissions
    run_fix_permissions
    run_security_check
    setup_cron
    create_symlinks
    run_first_backup
    protect_identity_file

    if [[ "$IS_UPDATE" == true ]]; then
        show_completion "update"
    else
        show_completion "fresh"
    fi

    cleanup_temp_artifacts
}

main "$@"
