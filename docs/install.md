# Install gg

`gg` is distributed as a user-local binary. The release installers do not
require Go, administrator privileges, or a system package manager. They
download the matching GitHub Release archive, validate its contents, and
replace the existing executable atomically.

## Inspect before installing

The shortest Unix installation is documented in the main README, but a
downloaded script is easier to audit before execution:

```bash
tmp="$(mktemp "${TMPDIR:-/tmp}/gg-install.XXXXXX")"
curl -fsSL --proto '=https' --tlsv1.2 \
  https://raw.githubusercontent.com/VedranJanjetovic/gg/main/install.sh \
  -o "$tmp"
less "$tmp"
bash "$tmp" --version 1.2.3
rm -f -- "$tmp"
```

Omit `--version` to install the latest release. Versions are written without
the `gg-v` tag prefix. Use `--prefix /absolute/path` to choose the destination,
`--url URL` to install an already inspected HTTPS archive, and `--help` for the
script's own usage text. The same values can be supplied with
`GG_INSTALL_PREFIX`, `GG_INSTALL_VERSION`, and `GG_INSTALL_URL`.
`GG_INSTALL_REPOSITORY` redirects both the release download and the skills
snapshot to another `owner/repo`; it defaults to `VedranJanjetovic/gg`.

The same version can be pinned in a piped installation by passing the flag
through to the script:

```bash
curl -fsSL --proto '=https' --tlsv1.2 \
  https://raw.githubusercontent.com/VedranJanjetovic/gg/main/install.sh \
  | bash -s -- --version 1.2.3
```

On Windows, use the equivalent inspect-first PowerShell flow:

```powershell
$url = 'https://raw.githubusercontent.com/VedranJanjetovic/gg/main/install.ps1'
Invoke-WebRequest -Uri $url -OutFile .\install.ps1
Get-Content .\install.ps1
Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass
.\install.ps1 -Version 1.2.3
Remove-Item .\install.ps1
```

`-Version latest` is the default. Use `-Prefix` to choose the destination and
`-Url` to install an already inspected HTTPS archive, or set
`GG_INSTALL_PREFIX`, `GG_INSTALL_VERSION`, and `GG_INSTALL_REPOSITORY`. Note
that `-Url` has no environment fallback: `GG_INSTALL_URL` is honored by
`install.sh` only. The process-scoped execution
policy change lasts only for the current PowerShell session; follow any
organization policy that restricts scripts rather than weakening a
machine-wide policy.

## Prefix and `PATH`

On Linux and macOS the default prefix is `$XDG_BIN_HOME` when set, otherwise
`$HOME/.local/bin`. On Windows it is `$HOME\.local\bin`. Neither installer
edits shell startup files. If `gg` is not found, add the selected directory to
the current user's `PATH` and then to the appropriate shell or user-environment
configuration.

For a Unix shell session:

```bash
export PATH="${XDG_BIN_HOME:-$HOME/.local/bin}:$PATH"
gg --help
```

For a PowerShell session:

```powershell
$env:Path = "$HOME\.local\bin;$env:Path"
gg --help
```

The shell installer accepts an absolute `--prefix`; the PowerShell installer
normalizes `-Prefix` to an absolute path. Both installers warn when the chosen
prefix is not on `PATH`.

## Supported release assets

Release tags use the `gg-vX.Y.Z` form. The six installer assets and their
archive roots are:

| Operating system | Architecture | Asset | Archive root |
| --- | --- | --- | --- |
| Linux | amd64 | `gg-linux-amd64.tar.gz` | `gg` |
| Linux | arm64 | `gg-linux-arm64.tar.gz` | `gg` |
| macOS | amd64 | `gg-darwin-amd64.tar.gz` | `gg` |
| macOS | arm64 | `gg-darwin-arm64.tar.gz` | `gg` |
| Windows | amd64 | `gg-windows-amd64.zip` | `gg.exe` |
| Windows | arm64 | `gg-windows-arm64.zip` | `gg.exe` |

The canonical release and build requirements are maintained in
[`release-contract.md`](../release-contract.md).

## Security checks

- A pipe-to-shell installation runs remote code before you inspect it. Prefer
  the flows above when script provenance matters, and verify the raw installer
  URL and requested release.
- Downloads use HTTPS. Release archives are fetched from the corresponding
  GitHub Release; check the tag and asset name when investigating provenance.
- The installer rejects a prefix, destination, or destination component that is
  a symlink on Unix or a reparse point on Windows. It also refuses a non-file
  destination.
- Before extraction, the Unix archive must be a valid gzip tar whose only entry
  is named `gg`; that entry is confirmed to be a regular file and not a symlink
  after it is unpacked into a temporary directory, before anything reaches the
  prefix. The Windows archive is checked entirely before extraction and must
  contain only the non-empty regular-file entry `gg.exe`.
- The executable is staged in the destination directory and moved into place
  only after validation. A failed download or archive check must not be used as
  an installed binary.

The installer also installs the repository's agent-skill assets. A checkout
installs from its own `skills/` tree; a remote run downloads a repository
snapshot — the `gg-vX.Y.Z` tag for an explicit `--version`, or `main` for
`latest`, so a `latest` installation pairs a released binary with skills from
`main`. See [`development.md`](development.md) for those destinations and rules.

`gg update` does not reuse a local copy of this script. It fetches the installer
for the release it is installing, pinned to that release's tag, so installer and
binary always match. See [`pipeline.md`](pipeline.md) for that flow.

## Troubleshooting

### The download fails

Check network access, TLS interception, and that the requested release has the
asset for the current operating system and architecture. Unsupported targets
are rejected before an archive is downloaded.

### `gg: command not found`

Add the installer's reported prefix to `PATH`, start a new shell, or invoke the
binary by its full path. The installer intentionally does not modify shell
startup files.

### A prefix or link-protection check fails

Choose a real absolute directory that you own. Remove a symlink or reparse
point from the destination path rather than bypassing the check.

### The PowerShell script is blocked

Use the process-scoped policy command only if permitted by your organization's
rules. If scripts are restricted by policy, use the approved administrative
process instead of weakening the machine policy.

### Archive validation fails

Do not run the downloaded file. Confirm the release asset name, tag, and archive
root against [`release-contract.md`](../release-contract.md), then retry with
the inspected non-pipe flow.
