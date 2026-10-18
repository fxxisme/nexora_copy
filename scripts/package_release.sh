#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

TARGET_OS="${TARGET_OS:-linux}"
TARGET_ARCH="${TARGET_ARCH:-amd64}"
APP_NAME="nexora"
PACKAGE_NAME="${APP_NAME}-${TARGET_OS}-${TARGET_ARCH}"
BIN_PATH="bin/${PACKAGE_NAME}"
RELEASE_DIR="release/${PACKAGE_NAME}"
TARBALL="release/${PACKAGE_NAME}.tar.gz"
BUILD_WEB=0
SKIP_TESTS=0
CURL_IMPERSONATE_VERSION="v0.6.1"

usage() {
  cat <<'EOF'
Usage: scripts/package_release.sh [options]

Options:
  --web         Rebuild frontend before packaging.
  --skip-tests  Skip go test ./....
  -h, --help    Show this help.

Environment:
  TARGET_OS     Target OS. Supported: linux, darwin. Default: linux.
  TARGET_ARCH   Target architecture. Supported: linux/amd64, linux/arm64, darwin/amd64. Default: amd64.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --web)
      BUILD_WEB=1
      shift
      ;;
    --skip-tests)
      SKIP_TESTS=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

require_path() {
  local path="$1"
  local label="$2"
  if [[ ! -e "$path" ]]; then
    echo "Missing ${label}: ${path}" >&2
    exit 1
  fi
}

curl_asset_for_target() {
  case "${TARGET_OS}/${TARGET_ARCH}" in
    linux/amd64)
      echo "curl-impersonate-${CURL_IMPERSONATE_VERSION}.x86_64-linux-gnu.tar.gz"
      ;;
    linux/arm64)
      echo "curl-impersonate-${CURL_IMPERSONATE_VERSION}.aarch64-linux-gnu.tar.gz"
      ;;
    darwin/amd64)
      echo "curl-impersonate-${CURL_IMPERSONATE_VERSION}.x86_64-macos.tar.gz"
      ;;
    *)
      echo "Unsupported package target: ${TARGET_OS}/${TARGET_ARCH}. Supported targets are linux/amd64, linux/arm64, and darwin/amd64." >&2
      exit 2
      ;;
  esac
}

if [[ "$SKIP_TESTS" -eq 0 ]]; then
  echo "==> Running Go tests"
  go test ./...
fi

if [[ "$BUILD_WEB" -eq 1 ]]; then
  echo "==> Building frontend"
  make web
fi

require_path "web_dist" "frontend build output"
require_path "start.sh" "start script"
require_path "README.md" "README"
require_path "GO_MIGRATION.md" "GO_MIGRATION.md"
require_path "LICENSE" "LICENSE"

mkdir -p bin release

CURL_ASSET="$(curl_asset_for_target)"
CURL_URL="https://github.com/lwthiker/curl-impersonate/releases/download/${CURL_IMPERSONATE_VERSION}/${CURL_ASSET}"

rm -rf "$RELEASE_DIR"
mkdir -p "$RELEASE_DIR/bin" "$RELEASE_DIR/data/bin/curl-impersonate"

echo "==> Building ${TARGET_OS}/${TARGET_ARCH} binary"
GOOS="$TARGET_OS" GOARCH="$TARGET_ARCH" CGO_ENABLED=0 \
  go build -trimpath -ldflags='-s -w' -o "$BIN_PATH" ./cmd/server

cp "$BIN_PATH" "$RELEASE_DIR/bin/${APP_NAME}"
cp "$BIN_PATH" "$RELEASE_DIR/${APP_NAME}"
cp -R web_dist "$RELEASE_DIR/web_dist"

echo "==> Downloading curl-impersonate: ${CURL_ASSET}"
curl -fsSL "$CURL_URL" | tar -xz -C "$RELEASE_DIR/data/bin/curl-impersonate"
if [[ -z "$(find "$RELEASE_DIR/data/bin/curl-impersonate" -type f \( -name 'curl_edge101' -o -name 'curl_chrome116' -o -name 'curl-impersonate-chrome' -o -name 'curl-impersonate' \) -print -quit)" ]]; then
  echo "Missing curl-impersonate executable in downloaded package" >&2
  exit 1
fi

cp start.sh "$RELEASE_DIR/start.sh"
for file in README.md GO_MIGRATION.md LICENSE THIRD_PARTY_NOTICES.md VERSION; do
  if [[ -f "$file" ]]; then
    cp "$file" "$RELEASE_DIR/"
  fi
done

cat > "$RELEASE_DIR/config.example.json" <<'JSON'
{
  "server": {
    "addr": ":3000",
    "admin_key": "change-me"
  }
}
JSON

cat > "$RELEASE_DIR/README_RELEASE.md" <<EOF
# ${PACKAGE_NAME}

This archive contains:

- The ${TARGET_OS}/${TARGET_ARCH} server binary.
- The built frontend files in web_dist/.
- A matching curl-impersonate binary bundle in data/bin/curl-impersonate/.

## Run

\`\`\`bash
./${APP_NAME}
\`\`\`

If start.sh is supported by your shell, you can also run:

\`\`\`bash
./start.sh --port 3000
\`\`\`

## Use bundled curl-impersonate

\`\`\`bash
CHATGPT2API_UPSTREAM_TRANSPORT=curl-impersonate \\
CHATGPT2API_CURL_IMPERSONATE_BIN="\$PWD/data/bin/curl-impersonate/curl_edge101" \\
./${APP_NAME}
\`\`\`

If curl_edge101 is not present in this target bundle, use one of the curl_* launcher scripts or curl-impersonate executables in data/bin/curl-impersonate/.
EOF

chmod +x "$RELEASE_DIR/start.sh" "$RELEASE_DIR/${APP_NAME}" "$RELEASE_DIR/bin/${APP_NAME}"
find "$RELEASE_DIR/data/bin/curl-impersonate" -type f -exec chmod +x {} +

test -d "$RELEASE_DIR/web_dist"
test -f "$RELEASE_DIR/bin/${APP_NAME}"
test -n "$(find "$RELEASE_DIR/data/bin/curl-impersonate" -type f -print -quit)"

rm -f "$TARBALL"
echo "==> Creating tarball"
(
  cd release
  tar -czf "${PACKAGE_NAME}.tar.gz" "$PACKAGE_NAME"
)

SHA256="$(sha256sum "$TARBALL" | awk '{print $1}')"
BIN_SIZE="$(ls -lh "$BIN_PATH" | awk '{print $5}')"
TARBALL_SIZE="$(ls -lh "$TARBALL" | awk '{print $5}')"
FILE_COUNT="$(tar -tzf "$TARBALL" | wc -l | tr -d ' ')"

cat <<EOF
==> Package complete
Binary:   ${BIN_PATH} (${BIN_SIZE})
Tarball:  ${TARBALL} (${TARBALL_SIZE})
SHA256:   ${SHA256}
Files:    ${FILE_COUNT}
EOF
