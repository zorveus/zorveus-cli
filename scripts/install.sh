#!/bin/sh
set -eu

release_origin='https://zorveus.com/cli'
version=''
install_dir=''
download_dir=''
staged_binary=''
staged_completion=''
completion_enabled=true
completion_shell=${SHELL:-}
completion_shell=${completion_shell##*/}

fail() {
  printf 'zorveus installer: %s\n' "$1" >&2
  exit 1
}

cleanup() {
  if [ -n "$staged_binary" ]; then
    rm -f "$staged_binary"
  fi
  if [ -n "$staged_completion" ]; then
    rm -f "$staged_completion"
  fi
  if [ -n "$download_dir" ]; then
    rm -rf "$download_dir"
  fi
}

usage() {
  cat <<'EOF'
Install Zorveus for macOS or Linux.

Usage: sh install.sh [--version v1.2.3] [--bin-dir /absolute/path]
                     [--shell zsh|bash|fish] [--no-completion]

By default, install the latest release into $HOME/.local/bin.
No administrator access is required for the default directory.
Completion is configured for your login shell. Open a new terminal to enable it.
Use --shell to select a shell, or --no-completion to skip shell configuration.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      [ "$#" -ge 2 ] || fail '--version needs a release version.'
      version=$2
      shift 2
      ;;
    --bin-dir)
      [ "$#" -ge 2 ] || fail '--bin-dir needs an absolute directory path.'
      install_dir=$2
      shift 2
      ;;
    --shell)
      [ "$#" -ge 2 ] || fail '--shell needs zsh, bash, or fish.'
      case "$2" in
        zsh|bash|fish) completion_shell=$2 ;;
        *) fail '--shell needs zsh, bash, or fish.' ;;
      esac
      shift 2
      ;;
    --no-completion)
      completion_enabled=false
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *) fail "Unknown argument: $1" ;;
  esac
done

case "$(uname -s)" in
  Darwin) target_os='darwin' ;;
  Linux) target_os='linux' ;;
  *) fail 'Supported systems are macOS and Linux. Use the Windows binary on Windows.' ;;
esac

case "$(uname -m)" in
  x86_64|amd64) target_arch='amd64' ;;
  aarch64|arm64) target_arch='arm64' ;;
  *) fail 'Supported processors are x86-64 and ARM64.' ;;
esac

if [ -z "$install_dir" ]; then
  [ -n "${HOME:-}" ] || fail 'Set HOME or provide --bin-dir.'
  install_dir="$HOME/.local/bin"
fi
case "$install_dir" in
  /*) ;;
  *) fail '--bin-dir must be an absolute path.' ;;
esac

command -v curl >/dev/null 2>&1 || fail 'Install curl and CA certificates, then retry.'
if command -v sha256sum >/dev/null 2>&1; then
  checksum_command='sha256sum'
elif command -v shasum >/dev/null 2>&1; then
  checksum_command='shasum'
else
  fail 'Install sha256sum or shasum, then retry.'
fi

trap cleanup 0
trap 'exit 1' 1 2 15
download_dir=$(mktemp -d "${TMPDIR:-/tmp}/zorveus-install.XXXXXX") || fail 'Could not create a temporary directory.'

download() {
  # Keep both the initial request and redirects on HTTPS.
  curl --fail --silent --show-error --location \
    --proto '=https' --proto-redir '=https' \
    --connect-timeout 15 --max-time 300 \
    --output "$2" "$1" || fail "Could not download $1. Your current installation was not changed."
}

append_completion_loader() {
  startup_file=$1
  if [ -f "$startup_file" ] && grep -Fq '# >>> Zorveus completion >>>' "$startup_file"; then
    return 0
  fi
  mkdir -p "${startup_file%/*}" || return 1
  # Append only our loader; preserve existing content and startup-file symlinks.
  cat "$download_dir/completion-loader" >> "$startup_file" || return 1
}

install_completion() {
  [ -n "${HOME:-}" ] || return 1
  case "$completion_shell" in
    zsh)
      completion_directory="${XDG_DATA_HOME:-$HOME/.local/share}/zorveus/completions"
      completion_file="$completion_directory/zorveus.zsh"
      ;;
    bash)
      completion_directory="${XDG_DATA_HOME:-$HOME/.local/share}/bash-completion/completions"
      completion_file="$completion_directory/zorveus"
      ;;
    fish)
      completion_directory="${XDG_CONFIG_HOME:-$HOME/.config}/fish/completions"
      completion_file="$completion_directory/zorveus.fish"
      ;;
    *) return 1 ;;
  esac
  case "$completion_directory" in
    /*) ;;
    *) return 1 ;;
  esac
  # Generate completion only after the downloaded executable has been verified.
  "$install_dir/zorveus" completion "$completion_shell" < /dev/null > "$download_dir/completion" || return 1
  [ -s "$download_dir/completion" ] || return 1
  mkdir -p "$completion_directory" || return 1
  [ ! -d "$completion_file" ] || return 1
  staged_completion=$(mktemp "$completion_directory/.zorveus-completion.XXXXXX") || return 1
  cp "$download_dir/completion" "$staged_completion" || return 1
  chmod 644 "$staged_completion" || return 1
  mv -f "$staged_completion" "$completion_file" || return 1
  staged_completion=''

  case "$completion_shell" in
    zsh)
      cat > "$download_dir/completion-loader" <<'EOF' || return 1

# >>> Zorveus completion >>>
if [ -r "${XDG_DATA_HOME:-$HOME/.local/share}/zorveus/completions/zorveus.zsh" ]; then
  if ! command -v compdef >/dev/null 2>&1; then
    autoload -Uz compinit
    compinit
  fi
  . "${XDG_DATA_HOME:-$HOME/.local/share}/zorveus/completions/zorveus.zsh"
fi
# <<< Zorveus completion <<<
EOF
      append_completion_loader "${ZDOTDIR:-$HOME}/.zshrc" || return 1
      ;;
    bash)
      cat > "$download_dir/completion-loader" <<'EOF' || return 1

# >>> Zorveus completion >>>
if [ -n "${BASH_VERSION:-}" ]; then
  if ! declare -F _init_completion >/dev/null && ! declare -F _get_comp_words_by_ref >/dev/null; then
    for completion_support in \
      /usr/share/bash-completion/bash_completion \
      /etc/bash_completion \
      /opt/homebrew/etc/profile.d/bash_completion.sh \
      /usr/local/etc/profile.d/bash_completion.sh \
      /opt/homebrew/etc/bash_completion \
      /usr/local/etc/bash_completion
    do
      if [ -r "$completion_support" ]; then
        . "$completion_support"
        break
      fi
    done
    unset completion_support
  fi
  if declare -F _init_completion >/dev/null || declare -F _get_comp_words_by_ref >/dev/null; then
    if [ -r "${XDG_DATA_HOME:-$HOME/.local/share}/bash-completion/completions/zorveus" ]; then
      . "${XDG_DATA_HOME:-$HOME/.local/share}/bash-completion/completions/zorveus"
    fi
  fi
fi
# <<< Zorveus completion <<<
EOF
      append_completion_loader "$HOME/.bashrc" || return 1
      if [ -f "$HOME/.bash_profile" ]; then
        login_startup="$HOME/.bash_profile"
      elif [ -f "$HOME/.bash_login" ]; then
        login_startup="$HOME/.bash_login"
      elif [ -f "$HOME/.profile" ]; then
        login_startup="$HOME/.profile"
      else
        login_startup="$HOME/.bash_profile"
      fi
      append_completion_loader "$login_startup" || return 1
      ;;
  esac
  printf 'Completion configured for %s. Open a new terminal to enable it.\n' "$completion_shell"
  if [ "$completion_shell" = bash ]; then
    printf 'Bash completion requires the bash-completion package.\n'
  fi
}

if [ -z "$version" ]; then
  download "$release_origin/latest" "$download_dir/latest"
  version=$(cat "$download_dir/latest")
fi

# Validate the version before using it as part of a download path.
case "$version" in
  ''|*[!a-zA-Z0-9.-]*) fail 'Expected a release version such as v1.2.3 or v1.2.3-rc.1.' ;;
esac
printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?$' ||
  fail 'Expected a release version such as v1.2.3 or v1.2.3-rc.1.'

artifact="zorveus-$target_os-$target_arch"
release_url="$release_origin/$version"
printf 'Downloading Zorveus %s for %s/%s...\n' "$version" "$target_os" "$target_arch"
download "$release_url/SHA256SUMS" "$download_dir/SHA256SUMS"
download "$release_url/$artifact" "$download_dir/$artifact"

expected_checksum=$(awk -v artifact="$artifact" '$2 == artifact { print $1 }' "$download_dir/SHA256SUMS")
case "$expected_checksum" in
  ''|*[!a-f0-9]*) fail 'The release does not contain a valid checksum for this binary.' ;;
esac
[ "${#expected_checksum}" -eq 64 ] || fail 'The release checksum is invalid or duplicated.'

if [ "$checksum_command" = 'sha256sum' ]; then
  actual_checksum=$(sha256sum "$download_dir/$artifact")
else
  actual_checksum=$(shasum -a 256 "$download_dir/$artifact")
fi
actual_checksum=${actual_checksum%% *}
[ "$actual_checksum" = "$expected_checksum" ] || fail 'Checksum verification failed. Your current installation was not changed.'

mkdir -p "$install_dir" || fail "Could not create $install_dir. Choose a writable --bin-dir."
[ ! -d "$install_dir/zorveus" ] || fail 'The installation destination is a directory.'

# Stage beside the destination so replacement stays on the same filesystem.
staged_binary=$(mktemp "$install_dir/.zorveus.XXXXXX") || fail "Cannot write to $install_dir. Choose a writable --bin-dir."
cp "$download_dir/$artifact" "$staged_binary"
chmod 755 "$staged_binary"
mv -f "$staged_binary" "$install_dir/zorveus"
staged_binary=''

printf 'Installed Zorveus %s at %s/zorveus\n' "$version" "$install_dir"
if [ "$completion_enabled" = true ]; then
  if ! install_completion; then
    printf 'Zorveus is installed, but shell completion was not configured. Run zorveus completion --help.\n' >&2
  fi
fi
case ":${PATH:-}:" in
  *":$install_dir:"*) printf 'Run zorveus --help to get started.\n' ;;
  *) printf 'Add %s to your PATH, then run zorveus --help.\n' "$install_dir" ;;
esac
