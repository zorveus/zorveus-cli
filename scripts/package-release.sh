#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
  printf 'Usage: sh scripts/package-release.sh v1.2.3\n' >&2
  exit 1
fi
version=$1
case "$version" in
  ''|*[!a-zA-Z0-9.-]*) printf 'Invalid release version. Use a version such as v1.2.3.\n' >&2; exit 1 ;;
esac
printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?$' || {
  printf 'Invalid release version. Use a version such as v1.2.3.\n' >&2
  exit 1
}

cd "$(dirname "$0")/.."
bundle="dist/release-$version"
if [ -e "$bundle" ]; then
  printf 'Release bundle already exists: %s\n' "$bundle" >&2
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  checksum_command='sha256sum'
elif command -v shasum >/dev/null 2>&1; then
  checksum_command='shasum'
else
  printf 'Install sha256sum or shasum before packaging a release.\n' >&2
  exit 1
fi

sh scripts/check-release-builds.sh

binary_dir="$bundle/cli/$version"
mkdir -p "$binary_dir"
cp scripts/install.sh "$bundle/install.sh"
printf '%s\n' "$version" > "$bundle/cli/latest"

for target_os in darwin linux windows; do
  for target_arch in amd64 arm64; do
    filename="zorveus-$target_os-$target_arch"
    if [ "$target_os" = 'windows' ]; then
      filename="$filename.exe"
    fi
    cp "dist/$filename" "$binary_dir/$filename"
  done
done

(
  cd "$binary_dir"
  for filename in zorveus-*; do
    if [ "$checksum_command" = 'sha256sum' ]; then
      sha256sum "$filename"
    else
      shasum -a 256 "$filename"
    fi
  done > SHA256SUMS
)

printf 'Prepared release website files in %s\n' "$bundle"
printf 'Publish the version directory first, then install.sh, then cli/latest.\n'
