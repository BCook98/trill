#!/bin/sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"
# xcrun/xcodebuild honor DEVELOPER_DIR when set, otherwise xcode-select.
# Do not change the global selection or infer a private checkout toolchain.
SDK_VERSION=$(xcrun --sdk iphonesimulator --show-sdk-version)
if ! awk -v version="$SDK_VERSION" 'BEGIN { split(version, v, "."); exit !(v[1] > 27 || (v[1] == 27 && v[2] >= 1)) }'; then
  echo "Trill requires the iOS 27.1 SDK or later for Duo navigation. Select an Xcode installation providing that SDK via DEVELOPER_DIR or xcode-select." >&2
  exit 1
fi
xcodebuild -project ios/Trill.xcodeproj -scheme Trill \
  -destination 'generic/platform=iOS Simulator' \
  -derivedDataPath .build/ios -clonedSourcePackagesDirPath .build/packages \
  -skipPackagePluginValidation build
