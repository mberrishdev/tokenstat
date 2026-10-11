#!/bin/sh
# Installs the latest tokenstat release:
#   curl -fsSL https://raw.githubusercontent.com/tornikegomareli/tokenstat/main/install.sh | sh
# Set TOKENSTAT_INSTALL_DIR to install somewhere other than ~/.local/bin.
set -eu

repo="tornikegomareli/tokenstat"
dir="${TOKENSTAT_INSTALL_DIR:-$HOME/.local/bin}"
base="${TOKENSTAT_BASE_URL:-https://github.com/$repo/releases/latest/download}"

fail() {
	echo "tokenstat: $1" >&2
	exit 1
}

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
darwin | linux) ;;
*) fail "$os is not supported. Releases exist for macOS and Linux." ;;
esac

case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "$(uname -m) is not supported. Releases exist for x86-64 and arm64." ;;
esac

archive="tokenstat_${os}_${arch}.tar.gz"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading $archive"
curl -fsSL "$base/$archive" -o "$tmp/$archive" || fail "could not download $base/$archive"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" || fail "could not download the checksums"

want=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$tmp/$archive" | awk '{ print $1 }')
else
	got=$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')
fi
[ -n "$want" ] && [ "$want" = "$got" ] || fail "the checksum of $archive does not match. Nothing was installed."

tar -xzf "$tmp/$archive" -C "$tmp" tokenstat
mkdir -p "$dir"
mv "$tmp/tokenstat" "$dir/tokenstat"
chmod 755 "$dir/tokenstat"
echo "Installed tokenstat to $dir/tokenstat"

case ":$PATH:" in
*":$dir:"*) echo "Run: tokenstat" ;;
*) echo "$dir is not on your PATH. Add it, then run tokenstat:
  export PATH=\"$dir:\$PATH\"" ;;
esac
