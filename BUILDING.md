# Building Zorveus

This guide is for developing the CLI and building executables from source. For installation from a prebuilt binary and everyday usage, see the [CLI README](README.md).

## Requirements

- Go 1.26 or newer.
- A checkout of this repository.
- An available OS credential store for login and live provisioning: Keychain on macOS, Secret Service on Linux, or Credential Manager on Windows.

Run the following commands from the repository root.

## Build and run

```sh
go build -o bin/zorveus ./cmd/zorveus
./bin/zorveus --help
```

To use the README's `zorveus` commands with your local build, add its directory to your current shell's `PATH`:

```sh
export PATH="$PWD/bin:$PATH"
zorveus --help
```

On Windows, build `bin/zorveus.exe` and add the `bin` directory to your PowerShell session's `Path`.

## Connect to a local backend

Start the management API on port 8000 and dashboard on port 3000, then run:

```sh
./bin/zorveus login --local
./bin/zorveus whoami
./bin/zorveus apps list
```

`--local` selects `http://localhost:8000` and `http://localhost:3000`. Commands then use the saved login's API origin. Login requests all CLI management permissions by default. Use `--scope` to request a smaller set, as shown in the [README](README.md#sign-in).

To override either origin, use `--api-url` or `login --dashboard-url`. Custom HTTP origins must be loopback addresses and require `--allow-insecure-loopback`. Use consistent browser hostnames for the dashboard and API.

The CLI reads provider secrets from environment variables and does not load `.env` files automatically.

## Run checks

```sh
go test -race ./...
go vet ./...
python3 scripts/test_installer.py
```

Unit tests use injected credential stores and local HTTP servers. They do not need a running Zorveus backend or modify your real OS credential store. Live verification uses real backend resources and credentials and requires separate setup.

Installer tests require Python 3. They simulate platform detection and downloads in temporary directories without accessing the network or your installed CLI. Coverage includes supported platforms, pinned versions, checksum rejection, failed updates, and cleanup.

## Build platform binaries

```sh
sh scripts/check-release-builds.sh
```

The script builds with `CGO_ENABLED=0` and writes these executables to `dist/`:

| System | amd64 | arm64 |
| --- | --- | --- |
| macOS | `zorveus-darwin-amd64` | `zorveus-darwin-arm64` |
| Linux | `zorveus-linux-amd64` | `zorveus-linux-arm64` |
| Windows | `zorveus-windows-amd64.exe` | `zorveus-windows-arm64.exe` |

Recipients do not need Go installed. Cross-compilation checks that each binary builds; browser login, TLS trust, and credential-store behavior still require runtime verification on each platform.

This script creates local binaries. It does not publish releases or configure a download service.

## Publish release files

Prepare a website bundle for a release version:

```sh
sh scripts/package-release.sh v0.1.0
```

Replace `v0.1.0` with the version being released. The command cross-compiles all six binaries, generates `SHA256SUMS`, and copies the installer into `dist/release-v0.1.0/`. It refuses to overwrite an existing bundle.

For automated releases, the [GitHub Actions workflow](.github/workflows/release.yaml) runs on version-tag pushes, uploads the bundle as a workflow artifact, and publishes its files as GitHub Release assets.

The [release guide](docs/releasing.md) describes tag publication and the frontend's download redirects that connect `zorveus.com` to those assets. The redirects are configured in the website's `next.config.ts`; deploy that website to activate them. Namecheap DNS does not need a change for these paths.

You can also host the local bundle directly by preserving its `install.sh`, `cli/latest`, and `cli/<version>/...` paths. Publish versioned binaries and checksums first, then the installer, then `cli/latest`. Keep old versions available for pinned installs.

The shell installer supports macOS and Linux on x86-64 and ARM64. Linux binaries use `CGO_ENABLED=0` and do not require a glibc-specific build. Linux login still needs Secret Service through D-Bus, and downloads need a trusted CA certificate store. Cross-builds and simulated installer tests do not establish runtime compatibility with every Linux distribution.

After publishing, verify downloads and install on each supported platform. macOS signing and notarization, Windows signing, and platform credential-store verification remain separate release work.

## Configuration reference

- [Manifest fields and update rules](docs/manifest-v1.md)
- [Manifest JSON Schema](internal/manifest/schema/v1.json)
- [Backend API contract](docs/backend-follow-up.md)
