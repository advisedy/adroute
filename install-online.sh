#!/usr/bin/env bash
# Fixed entry point for: curl -fsSL .../install-online.sh | bash
(
  set -euo pipefail
  command -v curl >/dev/null || { echo 'curl is required.' >&2; exit 1; }
  command -v sha256sum >/dev/null || { echo 'sha256sum is required.' >&2; exit 1; }
  repository=${ADROUTE_INSTALL_REPOSITORY_URL:-https://github.com/advisedy/adroute}
  release=$(curl -fsSL --retry 2 --output /dev/null --write-out '%{url_effective}' "$repository/releases/latest")
  version=${release##*/}
  [[ $release == "$repository/releases/tag/$version" && $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || {
    echo 'Cannot determine the latest stable AdRoute release.' >&2; exit 1;
  }
  base="$repository/releases/download/$version"
  work=$(mktemp -d)
  trap 'rm -rf -- "$work"' EXIT
  for name in SHA256SUMS install-release.sh; do
    curl -fsSL --retry 2 "$base/$name" -o "$work/$name"
  done
  expected=$(awk '$2 == "install-release.sh" {print $1}' "$work/SHA256SUMS")
  [[ $expected =~ ^[0-9a-fA-F]{64}$ ]] || { echo 'Missing or invalid installer checksum.' >&2; exit 1; }
  printf '%s  install-release.sh\n' "$expected" | (cd "$work" && sha256sum --check --status) || {
    echo 'Installer checksum mismatch.' >&2; exit 1;
  }
  echo "Installing AdRoute $version"
  # Pin every download to the release resolved above, even if latest changes.
  ADROUTE_RELEASE_BASE_URL="$base" bash "$work/install-release.sh" \
    --prefix "${ADROUTE_INSTALL_DIR:-$HOME/.local}" --configure-path
  echo 'Open a new terminal, then run: adroute start'
)
