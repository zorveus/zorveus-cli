# Release the CLI

GitHub Actions builds the CLI and publishes its binaries as GitHub Release assets. The website redirects the public `zorveus.com` download URLs to those assets.

Namecheap manages the domain's DNS. The live website currently reports Vercel hosting. Configure these paths in the website's Vercel project; DNS records cannot route individual URL paths.

## Connect the website once

The frontend's `next.config.ts` includes the three CLI download rules in its existing `redirects()` configuration. Deploy the frontend to activate them on `zorveus.com`.

These rules belong to the website project. The CLI repository needs GitHub Actions, not a Vercel deployment. Namecheap DNS can remain unchanged.

| Public URL | GitHub Release asset |
| --- | --- |
| `https://zorveus.com/install.sh` | Latest stable release's `install.sh` |
| `https://zorveus.com/cli/latest` | Latest stable release's `latest` file |
| `https://zorveus.com/cli/v0.1.0/SHA256SUMS` | Release `v0.1.0`'s `SHA256SUMS` |
| `https://zorveus.com/cli/v0.1.0/zorveus-linux-amd64` | Release `v0.1.0`'s Linux x86-64 executable |

The same versioned rule handles all six binaries. Redirects use temporary HTTP status codes so mutable URLs are not permanently pinned to one release. Keep previous releases and their assets for version-pinned installations.

The release repository must be public for these unauthenticated download URLs to work. If the source repository remains private, use a separate public distribution repository or public object storage instead. Do not embed a GitHub token in the installer.

GitHub documents [latest release asset links](https://docs.github.com/en/repositories/releasing-projects-on-github/linking-to-releases), and Vercel documents [configuration redirects](https://vercel.com/docs/routing/redirects).

## Publish a version

The [release workflow](../.github/workflows/release.yaml) runs when a version tag such as `v0.1.0` is pushed to `zorveus/zorveus-cli`. The tagged commit must include the workflow and packaging scripts. GitHub Actions must be enabled with permission to write repository contents.

The workflow:

1. Checks out the tagged commit, installs the Go version from `go.mod`, and tests self-updating on macOS, Linux, and Windows.
2. Runs race tests, vet, and installer tests.
3. Builds macOS, Linux, and Windows executables for x86-64 and ARM64.
4. Verifies the generated checksums and saves the website bundle as an Actions artifact for 14 days.
5. Uploads the installer, version file, checksums, and six executables to a GitHub Release.

The workflow uses the repository's built-in `GITHUB_TOKEN`. It needs no Namecheap, Vercel, or Zorveus API credentials.

An authorized maintainer can create and push a release tag after reviewing the commit:

```sh
git tag -a v0.1.0 -m "Zorveus CLI v0.1.0"
git push origin v0.1.0
```

These commands publish a release through the workflow. They are release operations, not part of installing or building the CLI.

Prerelease tags such as `v0.2.0-rc.1` are marked as prereleases and do not replace the stable installer default. They remain available through `--version`. Stable releases use GitHub's automatic latest-release selection.

Release creation uploads files through a draft before publishing. If an upload fails and leaves a draft, inspect that draft before retrying. The workflow does not overwrite an existing release. Do not move published tags or replace their assets; publish a new version for changes.

Actions artifacts retain the build bundle for inspection or direct hosting. Customer installation uses GitHub Release assets, so it does not depend on the artifact's retention period. See [GitHub's artifact retention documentation](https://docs.github.com/en/actions/tutorials/store-and-share-data#configuring-a-custom-artifact-retention-period).

## Verify the public URLs

After the website redirects and a stable release are published:

```sh
curl -fIL https://zorveus.com/install.sh
curl -fsSL https://zorveus.com/cli/latest
curl -fsSL https://zorveus.com/cli/v0.1.0/SHA256SUMS
```

Replace `v0.1.0` with the published version. Check that downloads follow HTTPS redirects and return release files rather than the website's HTML page. The version response must contain only the version and a newline.

To check installation without replacing your normal executable:

```sh
curl -fsSL https://zorveus.com/install.sh -o install-zorveus.sh
sh install-zorveus.sh --version v0.1.0 --bin-dir "$PWD/.cli-test/bin"
./.cli-test/bin/zorveus --help
```

Runtime verification, macOS signing and notarization, and Windows signing remain separate release checks. The current workflow cross-compiles unsigned binaries.

`zorveus update` uses the same `cli/latest`, versioned binaries, and `SHA256SUMS` paths as the installer. No additional website redirect is required. The command becomes available to installed users after they install a release containing it; it updates published executables rather than building unpublished source changes.
