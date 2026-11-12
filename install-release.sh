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
# The binary is several megabytes and comes from a different host than
# github.com, so resume a truncated transfer instead of starting over. A
# checksum mismatch below still fails the install, so partial data can never
# be executed.
download() {
  local name=$1 attempt code resume
  resume=1
  for attempt in 1 2 3 4 5; do
    local resume_args=()
    [[ $resume == 1 ]] && resume_args=(--continue-at -)
    code=0
    # The ${arr[@]+...} form keeps older Bash with `set -u` happy on an empty array.
    curl --fail --silent --show-error --location --connect-timeout 20 \
      ${resume_args[@]+"${resume_args[@]}"} "$base/$name" -o "$work/$name" || code=$?
    [[ $code -eq 0 ]] && return 0
    case $code in
      # The server answered 4xx: another attempt downloads the same error.
      22)
        echo "Cannot download $base/$name (HTTP error). Check the version and asset name." >&2
        return 1
        ;;
      # A proxy or mirror ignored Range and sent the whole body: start over.
      33)
        echo "Server cannot resume $name; restarting the download." >&2
        rm -f -- "$work/$name"
        resume=0
        ;;
      *)
        echo "Download interrupted (attempt $attempt/5, curl code $code): $name" >&2
        resume=1
        ;;
    esac
    sleep "$attempt"
  done
  echo "Cannot download $base/$name" >&2
  echo 'Check the network, or set a proxy with curl --proxy / HTTPS_PROXY and retry.' >&2
  return 1
}
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
