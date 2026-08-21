#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
REPO_ROOT="${SCRIPT_DIR}"
MODULE_ROOT="${SCRIPT_DIR}"
PACKAGE="github.com/VedranJanjetovic/gg/internal/version"
usage(){ cat <<'EOF'
Usage: ./build-release.sh [VERSION] [OUTPUT]
       ./build-release.sh --artifacts VERSION [OUTPUT_DIR]

Builds ./cmd/gg with reproducible release metadata. VERSION may also be
provided as GG_VERSION or VERSION. OUTPUT defaults to dist/gg-VERSION-GOOS-GOARCH.
--artifacts produces installer assets for Linux, macOS, and Windows on amd64 and
arm64: gg-{linux,darwin}-{amd64,arm64}.tar.gz and gg-windows-{amd64,arm64}.zip.
Use --artifacts for GitHub Release uploads. The ordinary mode remains the
single-binary compatibility interface and is not an installer asset.
EOF
}
fail(){ printf 'build-release: error: %s\n' "$*" >&2; exit 1; }
command -v go >/dev/null 2>&1 || fail "Go is required (install Go 1.22 or newer)"
command -v git >/dev/null 2>&1 || fail "git is required"
[[ -d "${MODULE_ROOT}" && -f "${MODULE_ROOT}/go.mod" ]] || fail "Go module not found: ${MODULE_ROOT}"
artifacts=0; if [[ "${1:-}" == --artifacts ]]; then artifacts=1; shift; fi
case "${1:-}" in -h|--help) usage; exit 0;; esac
version="${1:-${GG_VERSION:-${VERSION:-}}}"
if [[ -z "${version}" ]]; then version="$(git -C "${REPO_ROOT}" describe --tags --match 'gg-v*' --abbrev=0 2>/dev/null || true)"; fi
version="${version#gg-v}"; version="${version#v}"
[[ -n "${version}" ]] || fail "version is required (pass VERSION or GG_VERSION)"
[[ "${version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$ ]] || fail "invalid semantic version: ${version}"
commit="$(git -C "${REPO_ROOT}" rev-parse HEAD 2>/dev/null)" || fail "not a Git worktree: ${REPO_ROOT}"
[[ "${commit}" =~ ^[0-9a-f]{40}$ ]] || fail "could not determine full Git commit"
commit_epoch="$(git -C "${REPO_ROOT}" show -s --format=%ct HEAD 2>/dev/null)" || fail "could not determine Git commit timestamp"
if build_date="$(date -u -d "@${commit_epoch}" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null)"; then :; elif build_date="$(date -u -r "${commit_epoch}" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null)"; then :; else fail "could not normalize Git commit timestamp to UTC"; fi
ldflags="-s -w -X ${PACKAGE}.Version=${version} -X ${PACKAGE}.Commit=${commit} -X ${PACKAGE}.Date=${build_date}"
build_one(){ local goos="$1" goarch="$2" output="$3"; mkdir -p "$(dirname -- "$output")"; printf 'build-release: target=%s/%s\n' "$goos" "$goarch"; (cd "${MODULE_ROOT}" && GOOS="$goos" GOARCH="$goarch" CGO_ENABLED="${CGO_ENABLED:-0}" go build -trimpath -buildvcs=false -ldflags "${ldflags}" -o "${output}" ./cmd/gg); chmod 0755 "$output"; }
if (( artifacts )); then
 artifact_names=(gg-linux-amd64.tar.gz gg-linux-arm64.tar.gz gg-darwin-amd64.tar.gz gg-darwin-arm64.tar.gz gg-windows-amd64.zip gg-windows-arm64.zip)
 command -v tar >/dev/null 2>&1 || fail "tar is required for artifact mode"; command -v python3 >/dev/null 2>&1 || fail "python3 is required for Windows zip artifacts"
 out_dir="${2:-${REPO_ROOT}/dist}"; [[ "$out_dir" = /* ]] || out_dir="$(pwd -P)/$out_dir"; mkdir -p "$out_dir"
 tmp="$(mktemp -d "${TMPDIR:-/tmp}/gg-release.XXXXXX")"; trap 'rm -rf -- "$tmp"' EXIT
 for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  goos="${target%/*}"; goarch="${target#*/}"; bin="${tmp}/${goos}-${goarch}/gg"; [[ "$goos" == windows ]] && bin="${bin}.exe"; build_one "$goos" "$goarch" "$bin"
  if [[ "$goos" == windows ]]; then
   python3 -c 'import sys,zipfile; from pathlib import Path; source,archive=map(Path,sys.argv[1:]); z=zipfile.ZipFile(archive,"w",compression=zipfile.ZIP_DEFLATED); info=zipfile.ZipInfo("gg.exe",(1980,1,1,0,0,0)); info.external_attr=0o100755<<16; z.writestr(info,source.read_bytes()); z.close()' "$bin" "${out_dir}/gg-windows-${goarch}.zip"
  else
   python3 - "$bin" "${out_dir}/gg-${goos}-${goarch}.tar.gz" <<'PY'
import gzip
import sys
import tarfile
from pathlib import Path

source, archive = map(Path, sys.argv[1:])
with archive.open("wb") as raw, gzip.GzipFile(fileobj=raw, mode="wb", mtime=0) as compressed:
    with tarfile.open(fileobj=compressed, mode="w", format=tarfile.USTAR_FORMAT) as tar:
        info = tar.gettarinfo(str(source), arcname="gg")
        info.uid = 0
        info.gid = 0
        info.uname = ""
        info.gname = ""
        info.mtime = 0
        info.mode = 0o755
        with source.open("rb") as payload:
            tar.addfile(info, payload)
PY
  fi
 done
 printf 'build-release: wrote installer artifacts under %s\n' "$out_dir"; exit 0
fi
goos="${GOOS:-$(go env GOOS)}"; goarch="${GOARCH:-$(go env GOARCH)}"; output="${2:-${REPO_ROOT}/dist/gg-${version}-${goos}-${goarch}}"; [[ "$output" = /* ]] || output="$(pwd -P)/$output"; build_one "$goos" "$goarch" "$output"; printf 'build-release: wrote %s\n' "$output"
