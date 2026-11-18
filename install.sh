#!/bin/sh
# Installs the newest memorable release for this machine.
#
#   curl -fsSL https://raw.githubusercontent.com/constantine-kostikas/memorable/main/install.sh | sh
#
# Environment:
#   MEMORABLE_VERSION      release tag to install (default: newest release, pre-releases included)
#   MEMORABLE_INSTALL_DIR  target directory (default: ~/.local/bin)
set -eu

repo="constantine-kostikas/memorable"
install_dir="${MEMORABLE_INSTALL_DIR:-$HOME/.local/bin}"

fail() {
	echo "memorable install: $*" >&2
	exit 1
}

need() {
	command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

need curl
need tar
need uname

os=$(uname -s)
arch=$(uname -m)
case "$os/$arch" in
Linux/x86_64 | Linux/aarch64 | Darwin/x86_64 | Darwin/arm64) ;;
Linux/arm64) arch=aarch64 ;;
Darwin/aarch64) arch=arm64 ;;
Linux/amd64 | Darwin/amd64) arch=x86_64 ;;
*) fail "no release for $os/$arch; build from source with \`make build\`" ;;
esac

if command -v sha256sum >/dev/null 2>&1; then
	sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
	sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
	fail "sha256sum or shasum is required"
fi

highest_semver() {
	awk '
	function ident_cmp(a, b,  an, bn) {
		an = (a ~ /^[0-9]+$/); bn = (b ~ /^[0-9]+$/)
		if (an && bn) return (a + 0 < b + 0) ? -1 : (a + 0 > b + 0)
		if (an != bn) return an ? -1 : 1
		return (a < b) ? -1 : (a > b)
	}
	function semver_cmp(a, b,  ac, bc, ap, bp, x, y, n, m, i, c) {
		sub(/^v/, "", a); sub(/^v/, "", b)
		sub(/\+.*/, "", a); sub(/\+.*/, "", b)
		ac = a; ap = ""; if ((i = index(a, "-")) > 0) { ac = substr(a, 1, i - 1); ap = substr(a, i + 1) }
		bc = b; bp = ""; if ((i = index(b, "-")) > 0) { bc = substr(b, 1, i - 1); bp = substr(b, i + 1) }
		split(ac, x, "."); split(bc, y, ".")
		for (i = 1; i <= 3; i++) if ((c = ident_cmp(x[i] + 0, y[i] + 0)) != 0) return c
		if (ap == "" || bp == "") return (ap == bp) ? 0 : (ap == "" ? 1 : -1)
		n = split(ap, x, "."); m = split(bp, y, ".")
		for (i = 1; i <= n && i <= m; i++) if ((c = ident_cmp(x[i], y[i])) != 0) return c
		return (n < m) ? -1 : (n > m)
	}
	/^v?[0-9]+\.[0-9]+\.[0-9]+/ { if (best == "" || semver_cmp($0, best) > 0) best = $0 }
	END { print best }
	'
}

tag="${MEMORABLE_VERSION:-}"
if [ -z "$tag" ]; then
	# The "latest release" endpoint skips pre-releases and the list is not in version order,
	# so pick the highest semver tag.
	tag=$(curl -fsSL "https://api.github.com/repos/$repo/releases?per_page=100" |
		sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | highest_semver)
	[ -n "$tag" ] || fail "could not find the newest release of $repo"
fi

archive="memorable_${os}_${arch}.tar.gz"
base="https://github.com/$repo/releases/download/$tag"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "Downloading memorable $tag for $os/$arch"
curl -fsSL -o "$tmp/$archive" "$base/$archive" || fail "download failed: $base/$archive"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || fail "download failed: $base/checksums.txt"

want=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || fail "$archive is not listed in checksums.txt"
[ "$(sha256 "$tmp/$archive")" = "$want" ] || fail "checksum mismatch for $archive"

tar -xzf "$tmp/$archive" -C "$tmp" memorable
mkdir -p "$install_dir"
mv "$tmp/memorable" "$install_dir/memorable"
chmod 0755 "$install_dir/memorable"

echo "Installed $("$install_dir/memorable" --version) to $install_dir/memorable"
case ":$PATH:" in
*":$install_dir:"*) ;;
*) echo "Add $install_dir to your PATH, for example: export PATH=\"$install_dir:\$PATH\"" ;;
esac
echo "Next: memorable setup"
