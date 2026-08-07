#!/bin/zsh

set -eu

app_dir="${CODEX_MODEL_CATALOG_APP_DIR:-$HOME/Applications/Codex Model Catalog.app}"
wrapper_path="${CODEX_MODEL_CATALOG_WRAPPER:-$HOME/.codex/bin/codex-model-catalog}"
codex_app="${CODEX_APP_PATH:-/Applications/Codex.app}"
contents_dir="$app_dir/Contents"
launcher_path="$contents_dir/MacOS/codex-model-catalog-launcher"

if [[ ! -x "$wrapper_path" ]]; then
  print -u2 "wrapper is not executable: $wrapper_path"
  exit 1
fi

if [[ ! -d "$codex_app" ]]; then
  print -u2 "Codex App not found: $codex_app"
  exit 1
fi

/bin/mkdir -p "$contents_dir/MacOS"

/bin/cat >"$contents_dir/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleDisplayName</key>
  <string>Codex Model Catalog</string>
  <key>CFBundleExecutable</key>
  <string>codex-model-catalog-launcher</string>
  <key>CFBundleIdentifier</key>
  <string>local.codex-model-catalog</string>
  <key>CFBundleInfoDictionaryVersion</key>
  <string>6.0</string>
  <key>CFBundleName</key>
  <string>Codex Model Catalog</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>CFBundleShortVersionString</key>
  <string>1.0</string>
  <key>CFBundleVersion</key>
  <string>1</string>
  <key>LSMinimumSystemVersion</key>
  <string>13.0</string>
  <key>NSHighResolutionCapable</key>
  <true/>
</dict>
</plist>
PLIST

/bin/cat >"$launcher_path" <<'LAUNCHER'
#!/bin/zsh

set -eu

wrapper_path="${CODEX_MODEL_CATALOG_WRAPPER:-$HOME/.codex/bin/codex-model-catalog}"
codex_app="${CODEX_APP_PATH:-/Applications/Codex.app}"
codex_process="$codex_app/Contents/MacOS/ChatGPT"

if [[ ! -x "$wrapper_path" ]]; then
  /usr/bin/osascript -e 'display alert "Codex Model Catalog 不完整" message "找不到可执行的路由程序，请先构建 codex-model-catalog。" as critical buttons {"好"} default button "好"'
  exit 1
fi

if [[ ! -d "$codex_app" ]]; then
  /usr/bin/osascript -e 'display alert "找不到 Codex App" message "请确认 Codex 已安装在 /Applications，或设置 CODEX_APP_PATH。" as critical buttons {"好"} default button "好"'
  exit 1
fi

if /bin/ps -axo comm= | /usr/bin/grep -Fqx "$codex_process"; then
  /usr/bin/osascript -e 'display alert "请先完全退出 Codex" message "按 Command-Q 完全退出后，再打开 Codex Model Catalog。" buttons {"好"} default button "好"'
  exit 1
fi

exec /usr/bin/open -n --env "CODEX_CLI_PATH=$wrapper_path" "$codex_app"
LAUNCHER

/bin/chmod 755 "$launcher_path"
/usr/bin/plutil -lint "$contents_dir/Info.plist" >/dev/null
/usr/bin/codesign --force --deep --sign - "$app_dir" >/dev/null
/usr/bin/touch "$app_dir"

print "Installed: $app_dir"
