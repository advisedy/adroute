#!/usr/bin/env bash
set -euo pipefail

# Pinned release: this script and every downloaded file belong to one version.
version=0.1.0
base=${ADROUTE_RELEASE_BASE_URL:-https://github.com/advisedy/adroute/releases/download/v$version}
[[ $(uname -s) == Linux ]] || { echo 'Only Linux/WSL is supported by this installer.' >&2; exit 1; }
case $(uname -m) in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo 'Unsupported CPU architecture.' >&2; exit 1 ;;
esac
command -v curl >/dev/null || { echo 'curl is required.' >&2; exit 1; }
command -v sha256sum >/dev/null || { echo 'sha256sum is required.' >&2; exit 1; }
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
download() { curl --fail --silent --show-error --location --retry 2 "$base/$1" -o "$work/$1"; }
asset=adroute_${version}_linux_${arch}
download SHA256SUMS
for name in "$asset" install.sh; do
  download "$name"
  expected=$(awk -v name="$name" '$2 == name {print $1}' "$work/SHA256SUMS")
  [[ $expected =~ ^[0-9a-fA-F]{64}$ ]] || { echo "Missing or invalid checksum: $name" >&2; exit 1; }
  printf '%s  %s\n' "$expected" "$name" | (cd "$work" && sha256sum --check --status) || { echo "Checksum mismatch: $name" >&2; exit 1; }
done
chmod 0755 "$work/$asset"
[[ $("$work/$asset" --version) == "$version" ]] || { echo 'Release version mismatch.' >&2; exit 1; }
# Source installer retains its tested atomic update and shell PATH behavior.
bash "$work/install.sh" --binary "$work/$asset" "$@"
