#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
APP_DIR="${ROOT}/macos/Agentower"
TARGET="${APP_DIR}/Agentower"
RESOURCES_DIR="${TARGET}/Resources"
BUILD_DIR="${APP_DIR}/build"
DIST_DIR="${ROOT}/dist"
APP_BUNDLE="${DIST_DIR}/Agentower.app"

echo "==> Compilando binario Go (arm64)…"
mkdir -p "${RESOURCES_DIR}"
go build -trimpath -ldflags="-s -w" -o "${RESOURCES_DIR}/remote-bot" "${ROOT}/cmd/remote-bot"
chmod +x "${RESOURCES_DIR}/remote-bot"
echo "    ok: $(file "${RESOURCES_DIR}/remote-bot")"

echo "==> Generando iconos…"
bash "${APP_DIR}/scripts/make-icon.sh"

echo "==> Verificando XcodeGen…"
if ! command -v xcodegen >/dev/null 2>&1; then
  echo "    instalando XcodeGen vía Homebrew…"
  brew install xcodegen
fi

echo "==> Generando Xcode project…"
(cd "${APP_DIR}" && xcodegen generate)

echo "==> Compilando con xcodebuild…"
rm -rf "${BUILD_DIR}"
xcodebuild \
  -project "${APP_DIR}/Agentower.xcodeproj" \
  -scheme Agentower \
  -configuration Release \
  -derivedDataPath "${BUILD_DIR}" \
  -destination 'generic/platform=macOS' \
  ARCHS=arm64 \
  ONLY_ACTIVE_ARCH=NO \
  CODE_SIGN_IDENTITY=- \
  CODE_SIGNING_REQUIRED=NO \
  CODE_SIGNING_ALLOWED=NO \
  build

echo "==> Empaquetando .app…"
rm -rf "${APP_BUNDLE}"
mkdir -p "${DIST_DIR}"
cp -R "${BUILD_DIR}/Build/Products/Release/Agentower.app" "${APP_BUNDLE}"

echo "==> Re-firmando el bundle (ad-hoc con todos los recursos)…"
codesign --force --deep --sign - "${APP_BUNDLE}"
spctl --assess --verbose "${APP_BUNDLE}" 2>&1 || true

echo "==> Listo."
echo "    App: ${APP_BUNDLE}"
echo
echo "Para abrirla (la primera vez requiere desbloqueo Gatekeeper):"
echo "    xattr -dr com.apple.quarantine \"${APP_BUNDLE}\" && open \"${APP_BUNDLE}\""
echo
echo "Para instalarla:"
echo "    cp -R \"${APP_BUNDLE}\" /Applications/"