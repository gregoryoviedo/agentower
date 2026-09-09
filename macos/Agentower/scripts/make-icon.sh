#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
ASSETS_DIR="${ROOT}/macos/Agentower/Agentower/Assets.xcassets"
APPICON_DIR="${ASSETS_DIR}/AppIcon.appiconset"
MENUBAR_DIR="${ASSETS_DIR}/MenuBarIcon.imageset"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

BG_REMOVE="${ROOT}/macos/Agentower/assets/menubar-icon.png"
SOURCE_JPEG="${ROOT}/macos/Agentower/assets/app-icon.jpeg"

if [[ ! -f "${BG_REMOVE}" ]]; then
  echo "No se encontró ${BG_REMOVE}" >&2
  exit 1
fi
if [[ ! -f "${SOURCE_JPEG}" ]]; then
  echo "No se encontró ${SOURCE_JPEG}" >&2
  exit 1
fi

echo "Generando AppIcon (macOS) desde macos/Agentower/assets/app-icon.jpeg …"
ICONSET="${TMP_DIR}/AppIcon.iconset"
mkdir -p "${ICONSET}"
WORK_JPEG="${TMP_DIR}/work.png"
sips -s format png "${SOURCE_JPEG}" --out "${WORK_JPEG}" >/dev/null
sips -z 16 16     "${WORK_JPEG}" --out "${ICONSET}/icon_16x16.png"      >/dev/null
sips -z 32 32     "${WORK_JPEG}" --out "${ICONSET}/icon_16x16@2x.png"    >/dev/null
sips -z 32 32     "${WORK_JPEG}" --out "${ICONSET}/icon_32x32.png"      >/dev/null
sips -z 64 64     "${WORK_JPEG}" --out "${ICONSET}/icon_32x32@2x.png"    >/dev/null
sips -z 128 128   "${WORK_JPEG}" --out "${ICONSET}/icon_128x128.png"    >/dev/null
sips -z 256 256   "${WORK_JPEG}" --out "${ICONSET}/icon_128x128@2x.png"  >/dev/null
sips -z 256 256   "${WORK_JPEG}" --out "${ICONSET}/icon_256x256.png"    >/dev/null
sips -z 512 512   "${WORK_JPEG}" --out "${ICONSET}/icon_256x256@2x.png"  >/dev/null
sips -z 512 512   "${WORK_JPEG}" --out "${ICONSET}/icon_512x512.png"    >/dev/null
sips -z 1024 1024 "${WORK_JPEG}" --out "${ICONSET}/icon_512x512@2x.png"  >/dev/null

cp "${ICONSET}/icon_16x16.png"     "${APPICON_DIR}/icon_16x16.png"
cp "${ICONSET}/icon_16x16@2x.png"  "${APPICON_DIR}/icon_16x16@2x.png"
cp "${ICONSET}/icon_32x32.png"     "${APPICON_DIR}/icon_32x32.png"
cp "${ICONSET}/icon_32x32@2x.png"  "${APPICON_DIR}/icon_32x32@2x.png"
cp "${ICONSET}/icon_128x128.png"   "${APPICON_DIR}/icon_128x128.png"
cp "${ICONSET}/icon_128x128@2x.png" "${APPICON_DIR}/icon_128x128@2x.png"
cp "${ICONSET}/icon_256x256.png"   "${APPICON_DIR}/icon_256x256.png"
cp "${ICONSET}/icon_256x256@2x.png" "${APPICON_DIR}/icon_256x256@2x.png"
cp "${ICONSET}/icon_512x512.png"   "${APPICON_DIR}/icon_512x512.png"
cp "${ICONSET}/icon_512x512@2x.png" "${APPICON_DIR}/icon_512x512@2x.png"

rm -f "${APPICON_DIR}/icon.icns"

echo "Generando MenuBarIcon (template, bordes crisp) …"

TMP_MENU_1X="${TMP_DIR}/MenuBarIcon@1x.png"
TMP_MENU_2X="${TMP_DIR}/MenuBarIcon@2x.png"
TMP_MENU_3X="${TMP_DIR}/MenuBarIcon@3x.png"

sips -z 18 18  "${BG_REMOVE}" --out "${TMP_MENU_1X}" >/dev/null
sips -z 36 36  "${BG_REMOVE}" --out "${TMP_MENU_2X}" >/dev/null
sips -z 54 54  "${BG_REMOVE}" --out "${TMP_MENU_3X}" >/dev/null

python3 - "${TMP_MENU_1X}" "${MENUBAR_DIR}/MenuBarIcon@1x.png" "${TMP_MENU_2X}" "${MENUBAR_DIR}/MenuBarIcon@2x.png" "${TMP_MENU_3X}" "${MENUBAR_DIR}/MenuBarIcon@3x.png" <<'PY'
from PIL import Image
import sys
pairs = [(sys.argv[i], sys.argv[i+1]) for i in range(1, len(sys.argv), 2)]
for src, dst in pairs:
    im = Image.open(src).convert('RGBA')
    alpha = im.split()[-1]
    mask = alpha.point(lambda v: 255 if v > 48 else 0)
    out = Image.new('RGBA', im.size, (0, 0, 0, 0))
    solid = Image.new('RGBA', im.size, (255, 255, 255, 255))
    out.paste(solid, mask=mask)
    out.putalpha(mask)
    out.save(dst)
    opaque = sum(1 for p in out.getdata() if p[3] > 0)
    print(f'{dst}: {opaque} opaque px (full alpha)')
PY

echo "Iconos listos en Assets.xcassets."