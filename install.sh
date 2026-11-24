#!/bin/sh
# Install the enrichednow command-line tool on Linux or macOS.
#
#   curl -fsSL https://raw.githubusercontent.com/enrichednow/enrichednow-cli/main/install.sh | sh
#
# It downloads the release archive for this machine, checks it against the release's checksums.txt, and puts the
# binary in a directory on the PATH. Nothing is run as root unless you ask for a directory that needs it.
#
# Environment:
#   ENRICHEDNOW_VERSION       the version to install, e.g. 0.4.0 (default: the latest release)
#   ENRICHEDNOW_INSTALL_DIR   where to put the binary (default: /usr/local/bin if writable, else ~/.local/bin)
#   ENRICHEDNOW_REPO          the GitHub repository the releases are in (default: enrichednow/enrichednow-cli)
#   ENRICHEDNOW_DOWNLOAD_BASE where the release files are, instead of GitHub (with it, ENRICHEDNOW_VERSION is needed)
#
# On Windows, download the .zip of a release and put enrichednow.exe on the PATH.
set -eu

REPO="${ENRICHEDNOW_REPO:-enrichednow/enrichednow-cli}"
VERSION="${ENRICHEDNOW_VERSION:-}"
INSTALL_DIR="${ENRICHEDNOW_INSTALL_DIR:-}"
BASE="${ENRICHEDNOW_DOWNLOAD_BASE:-}"

say() { printf '%s\n' "$*"; }
fail() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }
# A download that stalls is given up after 20 seconds without data and tried again, three times at most.
fetch() { curl -fsSL --retry 3 --retry-delay 1 --connect-timeout 15 --speed-limit 1024 --speed-time 20 "$@"; }

have curl || fail "curl is needed"
have tar || fail "tar is needed"

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "this script installs on Linux and macOS; on $(uname -s), download a release by hand" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "there is no build for $(uname -m)" ;;
esac

if [ -z "$VERSION" ]; then
  [ -z "$BASE" ] || fail "set ENRICHEDNOW_VERSION when ENRICHEDNOW_DOWNLOAD_BASE is set"
  # The "latest" page redirects to the newest release's tag.
  latest=$(fetch -I -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest") ||
    fail "could not find the latest release of $REPO"
  VERSION="${latest##*/}"
  case "$VERSION" in v[0-9]*) ;; *) fail "could not find the latest release of $REPO" ;; esac
fi
VERSION="${VERSION#v}"
[ -n "$BASE" ] || BASE="https://github.com/$REPO/releases/download/v$VERSION"

archive="enrichednow_${VERSION}_${os}_${arch}.tar.gz"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading enrichednow $VERSION for $os/$arch"
fetch -o "$tmp/$archive" "$BASE/$archive" || fail "could not download $BASE/$archive"
fetch -o "$tmp/checksums.txt" "$BASE/checksums.txt" || fail "could not download $BASE/checksums.txt"

want=$(awk -v name="$archive" '$2 == name { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || fail "checksums.txt does not list $archive"
if have sha256sum; then
  got=$(sha256sum "$tmp/$archive" | awk '{ print $1 }')
elif have shasum; then
  got=$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')
else
  fail "sha256sum or shasum is needed to check the download"
fi
[ "$got" = "$want" ] || fail "the checksum of $archive is wrong: got $got, want $want"

tar -xzf "$tmp/$archive" -C "$tmp" enrichednow || fail "could not unpack $archive"

if [ -z "$INSTALL_DIR" ]; then
  if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
    INSTALL_DIR=/usr/local/bin
  else
    INSTALL_DIR="$HOME/.local/bin"
  fi
fi
mkdir -p "$INSTALL_DIR" || fail "could not create $INSTALL_DIR"
[ -w "$INSTALL_DIR" ] || fail "cannot write to $INSTALL_DIR; set ENRICHEDNOW_INSTALL_DIR to a directory you can write to"
cp "$tmp/enrichednow" "$INSTALL_DIR/enrichednow.new"
chmod 755 "$INSTALL_DIR/enrichednow.new"
mv -f "$INSTALL_DIR/enrichednow.new" "$INSTALL_DIR/enrichednow"

say "Installed $("$INSTALL_DIR/enrichednow" --version) to $INSTALL_DIR/enrichednow"
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) say "Add it to your PATH:  export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac
say "Next:  enrichednow login"
