# gg release and installer contract

This is the public contract shared by `install.sh`,
`install.ps1`, release packaging, and the update documentation.

## Repository, tags, and URLs

- Repository: `VedranJanjetovic/gg`
- Release tag: `gg-vX.Y.Z` (for example, `gg-v1.2.3`)
- Latest release asset URL:
  `https://github.com/VedranJanjetovic/gg/releases/latest/download/ASSET`
- Versioned release asset URL:
  `https://github.com/VedranJanjetovic/gg/releases/download/gg-vX.Y.Z/ASSET`
- Latest-release API URL:
  `https://api.github.com/repos/VedranJanjetovic/gg/releases/latest`
- Installer source URLs use the `main` branch:
  `https://raw.githubusercontent.com/VedranJanjetovic/gg/main/install.sh`
  and `install.ps1`.

Installers accept `latest` or a semantic version without the `gg-v` tag
prefix. Release packaging and upload must use the `--artifacts` mode of
`build-release.sh`; the ordinary mode remains the compatibility
interface for producing one versioned binary and is not a release asset.

## Published assets

| Target | Asset | Archive root |
| --- | --- | --- |
| Linux amd64 | `gg-linux-amd64.tar.gz` | `gg` |
| Linux arm64 | `gg-linux-arm64.tar.gz` | `gg` |
| macOS amd64 | `gg-darwin-amd64.tar.gz` | `gg` |
| macOS arm64 | `gg-darwin-arm64.tar.gz` | `gg` |
| Windows amd64 | `gg-windows-amd64.zip` | `gg.exe` |
| Windows arm64 | `gg-windows-arm64.zip` | `gg.exe` |

The archive root is intentionally a single executable entry, with no
versioned directory. Asset names are intentionally independent of the tag;
the tag supplies the version in the GitHub Releases URL.

## Release build command

From the repository root, build the complete upload set locally:

```bash
./build-release.sh --artifacts gg-v1.2.3 dist/release
```

The builder only creates local files; it does not create or publish a GitHub
Release. An authorized maintainer or external CI publication step must upload
every file in `dist/release` to the `gg-v1.2.3` GitHub Release using the exact
names above. Do not upload the ordinary output `gg-1.2.3-<os>-<arch>` as an
installer asset.

## Build provenance

Release artifacts must be built from the exact commit pointed to by the release
tag. The commit embedded in `gg version` must equal the full result of
`git rev-parse <tag>^{commit}`. The release version, tag, embedded version, and
artifact publication must agree; asset names remain unchanged and the tag
continues to provide the version in the GitHub Releases URL.

For example, building from a later or otherwise different `HEAD` and naming the
output for `gg-v1.0.0` creates a provenance mismatch: the artifact may be
published under the `gg-v1.0.0` release while `gg version` identifies a
different commit (and potentially different source or version). The artifact
must not be published until it is rebuilt from the tag commit.

### Operator checklist

Before publishing a release:

1. Check out the exact release tag, not a later branch tip.
2. Confirm the tag commit and `HEAD` are identical.
3. Build with `--artifacts` and the same semantic version as the tag.
4. Inspect `gg version` from an extracted artifact and confirm its version and
   commit match the tag and `git rev-parse <tag>^{commit}`.
5. Publish only the six documented assets, with their existing names, to that
   tag's GitHub Release.

### Diagnosis commands

Substitute the release tag and an extracted artifact path as needed:

```bash
tag=gg-v1.0.0
git fetch --tags
git rev-parse "${tag}^{commit}"
git rev-parse HEAD
git diff --quiet "${tag}^{commit}" HEAD
git describe --exact-match --tags HEAD
./dist/release/gg version
```

The first two commit values must be identical, `git diff` must exit zero, and
`git describe` must print the selected tag. Compare the commit and version
printed by `gg version` with the tag commit and tag version; any disagreement
indicates a provenance mismatch, not a naming-only issue.
