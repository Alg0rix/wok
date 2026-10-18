#!/bin/sh
# Install the Wok CLI from GitHub releases.
#
#   curl -fsSL https://raw.githubusercontent.com/alg0rix/wok/main/install.sh | sh
#
# WOK_VERSION=v0.1.0 pins a release (default: latest).
# WOK_INSTALL_DIR sets the destination (default: $HOME/.local/bin).
set -eu

repo="alg0rix/wok"
version="${WOK_VERSION:-latest}"
dir="${WOK_INSTALL_DIR:-$HOME/.local/bin}"

fail() { echo "wok install: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required"; }

need uname
need tar
if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL "$1" -o "$2"; }
	fetch_url() { curl -fsSLI -o /dev/null -w '%{url_effective}' "$1"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -qO "$2" "$1"; }
	fetch_url() { wget -S --spider "$1" 2>&1 | sed -n 's/^ *Location: *//p' | tail -n 1 | tr -d '\r'; }
else
	fail "curl or wget is required"
fi

case "$(uname -s)" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	MINGW* | MSYS* | CYGWIN*) fail "use install.ps1 on Windows" ;;
	*) fail "unsupported OS $(uname -s); Wok supports Linux, macOS and Windows" ;;
esac
case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) fail "unsupported architecture $(uname -m)" ;;
esac

if [ "$version" = latest ]; then
	url="$(fetch_url "https://github.com/$repo/releases/latest")"
	version="${url##*/}"
	case "$version" in v*) ;; *) fail "could not resolve latest release" ;; esac
fi

name="wok_${version#v}_${os}_${arch}"
base="https://github.com/$repo/releases/download/$version"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "Downloading Wok $version ($os/$arch)"
fetch "$base/$name.tar.gz" "$tmp/$name.tar.gz" || fail "download failed: $base/$name.tar.gz"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || fail "checksum download failed"

expected="$(awk -v f="$name.tar.gz" '$2 == f { print $1 }' "$tmp/checksums.txt")"
[ -n "$expected" ] || fail "no checksum for $name.tar.gz"
if command -v sha256sum >/dev/null 2>&1; then
	actual="$(sha256sum "$tmp/$name.tar.gz" | awk '{ print $1 }')"
else
	need shasum
	actual="$(shasum -a 256 "$tmp/$name.tar.gz" | awk '{ print $1 }')"
fi
[ "$expected" = "$actual" ] || fail "checksum mismatch for $name.tar.gz"

tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
mkdir -p "$dir"
install -m 0755 "$tmp/$name/wok" "$dir/wok" 2>/dev/null || {
	cp "$tmp/$name/wok" "$dir/wok"
	chmod 0755 "$dir/wok"
}

echo "Installed $("$dir/wok" --version) to $dir/wok"
case ":$PATH:" in
	*":$dir:"*) ;;
	*) echo "Add $dir to PATH: export PATH=\"$dir:\$PATH\"" ;;
esac
echo "Chromium headless shell downloads automatically on first 'wok start'."
