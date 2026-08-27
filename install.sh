#!/usr/bin/env bash
set -euo pipefail

# Install the user-local gg binary from a GitHub Release.
readonly repository="${GG_INSTALL_REPOSITORY:-VedranJanjetovic/gg}"
version="${GG_INSTALL_VERSION:-latest}"
prefix="${GG_INSTALL_PREFIX:-${XDG_BIN_HOME:-${HOME:-}/.local/bin}}"
override_url="${GG_INSTALL_URL:-}"
temp_dir=""
staged_file=""
# Root of the repository tree this run installs from (a source checkout or the
# fetched snapshot); persist_installer takes its trusted copy from here when the
# script itself is not a file on disk, as with a curl-piped install.
source_root=""

fail() { printf 'gg installer: error: %s\n' "$*" >&2; exit 1; }
usage() { cat <<'EOF'
Usage: install.sh [--version VERSION] [--prefix DIR] [--url URL]

Install the latest user-local gg release, or a selected semantic version.
  --version VERSION  version without the gg-v tag prefix (default: latest)
  --prefix DIR       absolute destination directory
  --url URL          inspected HTTPS archive URL override
  -h, --help         show this help

The default destination is $XDG_BIN_HOME or $HOME/.local/bin.
EOF
}
parse_args() {
    while (($#)); do
        case "$1" in
            --version) (($# > 1)) || fail '--version needs a value'; version=$2; shift 2 ;;
            --prefix) (($# > 1)) || fail '--prefix needs a value'; prefix=$2; shift 2 ;;
            --url) (($# > 1)) || fail '--url needs a value'; override_url=$2; shift 2 ;;
            -h|--help) usage; exit 0 ;;
            *) fail "unknown argument: $1" ;;
        esac
    done
}
validate_inputs() {
    [[ "$version" =~ ^(latest|[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?)$ ]] || fail "invalid version: $version"
    [[ -n "${HOME:-}" ]] || fail 'HOME is not set'
    [[ "$prefix" = /* && "$prefix" != *$'\n'* ]] || fail 'prefix must be an absolute path without newlines'
}
select_target() {
    case "$(uname -s)" in Linux) os=linux ;; Darwin) os=darwin ;; *) fail 'unsupported operating system' ;; esac
    case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) fail 'unsupported architecture' ;; esac
    o=$os; a=$arch; asset="gg-$o-$a.tar.gz"
}
check_dependencies() {
    command -v curl >/dev/null 2>&1 || fail 'curl is required'
    command -v tar >/dev/null 2>&1 || fail 'tar is required'
}
assert_no_symlink_components() {
    local path=$1 component rest
    rest=${path#/}; component=/
    while [[ -n "$rest" ]]; do
        component="${component%/}/${rest%%/*}"
        [[ ! -L "$component" ]] || fail "refusing symlink destination component: $component"
        if [[ "$rest" == */* ]]; then rest=${rest#*/}; else rest=; fi
    done
}
prepare_prefix() {
    assert_no_symlink_components "$prefix"
    [[ ! -e "$prefix" || -d "$prefix" ]] || fail 'prefix is not a directory'
    mkdir -p -- "$prefix"
    [[ ! -L "$prefix" ]] || fail 'refusing symlink destination'
    destination="$prefix/gg"
    [[ ! -L "$destination" ]] || fail 'refusing symlink destination executable'
    [[ ! -e "$destination" || -f "$destination" ]] || fail 'destination executable is not a regular file'
}
archive_url() {
    if [[ -n "$override_url" ]]; then url=$override_url
    elif [[ "$version" == latest ]]; then url="https://github.com/$repository/releases/latest/download/$asset"
    else url="https://github.com/$repository/releases/download/gg-v$version/$asset"; fi
    [[ "$url" =~ ^https://[^[:space:]]+$ ]] || fail 'download URL must use https'
}
cleanup() {
    [[ -z "$staged_file" || ! -e "$staged_file" && ! -L "$staged_file" ]] || rm -f -- "$staged_file"
    [[ -z "$temp_dir" || ! -d "$temp_dir" ]] || rm -rf -- "$temp_dir"
}
install_archive() {
    local archive listing
    temp_dir=$(mktemp -d "${TMPDIR:-/tmp}/gg-install.XXXXXX") || fail 'could not create temporary directory'
    trap cleanup EXIT HUP INT TERM
    archive="$temp_dir/$asset"
    curl -fsSL --proto '=https' --tlsv1.2 --output "$archive" "$url" || fail 'download failed'
    [[ -s "$archive" ]] || fail 'downloaded archive is empty'
    listing=$(tar -tzf "$archive") || fail 'invalid gzip tar archive'
    [[ "$listing" == gg || "$listing" == "gg"$'\n' ]] || fail 'archive must contain exactly root entry gg'
    tar -xzf "$archive" -C "$temp_dir" --no-same-owner --no-same-permissions || fail 'archive extraction failed'
    [[ -f "$temp_dir/gg" && ! -L "$temp_dir/gg" ]] || fail 'archive entry is not a regular file'
    chmod 0755 "$temp_dir/gg"
    staged_file="$prefix/.gg-install.$$"
    [[ ! -e "$staged_file" && ! -L "$staged_file" ]] || fail 'temporary destination exists'
    cp -- "$temp_dir/gg" "$staged_file" || fail 'staging install failed'
    [[ ! -L "$destination" ]] || fail 'refusing to replace symlink destination'
    [[ ! -e "$destination" || -f "$destination" ]] || fail 'destination executable is not a regular file'
    mv -f -- "$staged_file" "$destination" || fail 'atomic install failed'
    staged_file=
}
# --- Agent skills ------------------------------------------------------------
# Install gg's agent skills (canonical phase contracts, methodology skills,
# and the coding-patterns reference) into the user-level Claude Code and Codex
# configuration. Every file lands under a gg- prefix (path and frontmatter
# name), so nothing collides with user-managed skills, and shared files such
# as CLAUDE.md, AGENTS.md, and instructions.md are never touched. The
# directories are created even when claude/codex are not installed yet, so
# the skills are ready the moment those agents arrive. gg's prompts reference
# skills by gg-* name (loaded by the agent from these directories) and the
# coding-patterns file by its absolute path at ~/.gg/gg-coding-patterns.md.

# install_skill_file: copy src to dst, rewriting the first frontmatter
# `name:` field to the collision-free gg- identity.
install_skill_file() {
    local src=$1 dst=$2 name=$3
    mkdir -p -- "$(dirname "$dst")"
    awk -v ggname="gg-$name" '
        NR == 1 && $0 == "---" { in_front = 1 }
        in_front && !renamed && $0 ~ /^name:[ \t]*/ { print "name: " ggname; renamed = 1; next }
        in_front && NR > 1 && $0 == "---" { in_front = 0 }
        { print }
    ' "$src" > "$dst.gg-tmp.$$" && mv -f -- "$dst.gg-tmp.$$" "$dst"
}

# install_skills_from: install every skill from a skills/ asset tree.
# The tool-adapted variant (claude/, codex/) wins for a skill name; canonical
# covers the rest (the phase contracts have no adaptations).
install_skills_from() {
    local dir=$1 name claude_src codex_src count=0
    [[ -d "$dir/canonical" ]] || { printf 'gg installer: skill assets missing at %s\n' "$dir" >&2; return 1; }
    for skill_dir in "$dir/canonical/"*/; do
        [[ -d "$skill_dir" ]] || continue
        name=$(basename "$skill_dir")
        [[ -f "$skill_dir$name.md" ]] || continue
        claude_src="$dir/claude/commands/$name.md"
        [[ -f "$claude_src" ]] || claude_src="$skill_dir$name.md"
        codex_src="$dir/codex/skills/$name/SKILL.md"
        [[ -f "$codex_src" ]] || codex_src="$skill_dir$name.md"
        install_skill_file "$claude_src" "$HOME/.claude/commands/gg-$name.md" "$name"
        install_skill_file "$claude_src" "$HOME/.claude/skills/gg-$name/SKILL.md" "$name"
        install_skill_file "$codex_src" "$HOME/.codex/skills/gg-$name/SKILL.md" "$name"
        count=$((count + 1))
    done
    if [[ -f "$dir/core/coding-patterns.md" ]]; then
        local wrapped
        wrapped=$(mktemp "${TMPDIR:-/tmp}/gg-coding-patterns.XXXXXX")
        {
            printf '%s\n' '---' 'name: gg-coding-patterns' 'description: Coding patterns — SOLID, encapsulation, dependency injection, design patterns, bounded concurrency, testability' '---' ''
            cat "$dir/core/coding-patterns.md"
        } > "$wrapped"
        install_skill_file "$wrapped" "$HOME/.claude/commands/gg-coding-patterns.md" "coding-patterns"
        install_skill_file "$wrapped" "$HOME/.claude/skills/gg-coding-patterns/SKILL.md" "coding-patterns"
        install_skill_file "$wrapped" "$HOME/.codex/skills/gg-coding-patterns/SKILL.md" "coding-patterns"
        rm -f -- "$wrapped"
        # The raw reference gg's prompts point code-touching phases at by
        # absolute path (no frontmatter — it is read as a document).
        mkdir -p -- "$HOME/.gg"
        cp -- "$dir/core/coding-patterns.md" "$HOME/.gg/gg-coding-patterns.md"
        count=$((count + 1))
    fi
    printf 'gg installer: installed %d agent skills (~/.claude/{commands,skills}/gg-*, ~/.codex/skills/gg-*, ~/.gg/gg-coding-patterns.md)\n' "$count"
}

# install_skills: use the source checkout's skill assets when this script runs
# from one; a curl-piped install fetches the repository snapshot matching the
# requested version instead.
install_skills() {
    local script_dir snapshot_ref snapshot_url snapshot_dir
    script_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-.}")" 2>/dev/null && pwd || true)"
    if [[ -n "$script_dir" && -d "$script_dir/skills/canonical" ]]; then
        source_root="$script_dir"
        install_skills_from "$script_dir/skills"
        return
    fi
    snapshot_ref="refs/heads/main"
    [[ "$version" == latest ]] || snapshot_ref="refs/tags/gg-v$version"
    snapshot_url="https://github.com/$repository/archive/$snapshot_ref.tar.gz"
    [[ -n "$temp_dir" && -d "$temp_dir" ]] || { temp_dir=$(mktemp -d "${TMPDIR:-/tmp}/gg-install.XXXXXX"); trap cleanup EXIT HUP INT TERM; }
    snapshot_dir="$temp_dir/skills-src"
    mkdir -p -- "$snapshot_dir"
    curl -fsSL --proto '=https' --tlsv1.2 --output "$temp_dir/skills-src.tar.gz" "$snapshot_url" || { printf 'gg installer: skill snapshot download failed\n' >&2; return 1; }
    tar -xzf "$temp_dir/skills-src.tar.gz" -C "$snapshot_dir" --no-same-owner --no-same-permissions || { printf 'gg installer: skill snapshot extraction failed\n' >&2; return 1; }
    local assets
    assets=$(find "$snapshot_dir" -mindepth 2 -maxdepth 2 -type d -name skills | head -1)
    [[ -n "$assets" ]] || { printf 'gg installer: skill assets not found in repository snapshot\n' >&2; return 1; }
    source_root="$(dirname "$assets")"
    install_skills_from "$assets"
}

# persist_installer: keep a trusted copy of this installer at ~/.gg/install.sh
# so `gg update` runs a script the user already executed deliberately instead of
# requiring GG_INSTALLER_PATH or downloading shell text at update time. A
# curl-piped run has no script file on disk, so the copy comes from the
# repository tree this run installed from.
persist_installer() {
    local self source="" dir="$HOME/.gg" dest="$HOME/.gg/install.sh" staged
    self="${BASH_SOURCE[0]:-}"
    if [[ -n "$self" && -f "$self" && -r "$self" ]]; then
        source="$self"
    elif [[ -n "$source_root" && -f "$source_root/install.sh" ]]; then
        source="$source_root/install.sh"
    fi
    [[ -n "$source" ]] || { printf 'gg installer: no installer source to persist; gg update will need GG_INSTALLER_PATH\n' >&2; return 1; }
    mkdir -p -- "$dir"
    assert_no_symlink_components "$dir"
    [[ ! -L "$dest" ]] || fail 'refusing symlink installer destination'
    [[ ! -e "$dest" || -f "$dest" ]] || fail 'installer destination is not a regular file'
    staged="$dest.gg-tmp.$$"
    cp -- "$source" "$staged" || { rm -f -- "$staged"; printf 'gg installer: could not stage the trusted installer copy\n' >&2; return 1; }
    chmod 0644 "$staged"
    mv -f -- "$staged" "$dest" || { rm -f -- "$staged"; printf 'gg installer: could not persist the trusted installer copy\n' >&2; return 1; }
    printf 'gg installer: trusted installer copy at %s (used by gg update)\n' "$dest"
}

main() {
    parse_args "$@"; validate_inputs; select_target; check_dependencies; prepare_prefix; archive_url; install_archive
    printf 'gg installer: installed %s\n' "$destination"
    install_skills || printf 'gg installer: agent skill installation failed; re-run install.sh from a repository checkout\n' >&2
    # A failure here leaves the binary installed and reports its own reason; gg
    # update then falls back to GG_INSTALLER_PATH.
    persist_installer || true
    case ":${PATH:-}:" in *:"$prefix":*) ;; *) printf 'gg installer: add %s to PATH\n' "$prefix" >&2 ;; esac
}
[[ "${BASH_SOURCE[0]:-$0}" != "$0" ]] || main "$@"
