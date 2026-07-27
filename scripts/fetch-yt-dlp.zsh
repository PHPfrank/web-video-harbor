#!/bin/zsh
set -euo pipefail
unsetopt BG_NICE

script_dir="${0:A:h}"
repo_root="${script_dir:h}"
repo_real="${repo_root:A}"
manifest="$repo_root/third_party/yt-dlp.env"
binary_url="https://github.com/yt-dlp/yt-dlp/releases/download/2026.07.04/yt-dlp_macos"
license_url="https://raw.githubusercontent.com/yt-dlp/yt-dlp/2026.07.04/THIRD_PARTY_LICENSES.txt"

fail() {
  print -u2 -- "$1"
  exit 1
}

for required_command in curl shasum mktemp rg; do
  command -v "$required_command" >/dev/null 2>&1 || fail "缺少获取命令：$required_command"
done
[[ -f "$manifest" && ! -L "$manifest" ]] || fail "缺少安全的 third_party/yt-dlp.env"
if rg -v '^(YTDLP_VERSION=[0-9]{4}\.[0-9]{2}\.[0-9]{2}|YTDLP_MACOS_SHA256=[0-9a-f]{64}|YTDLP_LICENSE_SHA256=[0-9a-f]{64})$' \
  "$manifest" | rg -q .; then
  fail "yt-dlp 固定清单含无效字段"
fi
[[ "$(rg -c '^YTDLP_' "$manifest")" == "3" ]] || fail "yt-dlp 固定清单字段数量错误"
source "$manifest"
[[ "$YTDLP_VERSION" == "2026.07.04" ]] || fail "yt-dlp 固定版本不受支持"
[[ "$YTDLP_MACOS_SHA256" == "498bd0dae17855c599d371d68ec5bafc439a9d8640e838be25c765a9792f261b" ]] || \
  fail "yt-dlp 固定二进制哈希不受支持"
[[ "$YTDLP_LICENSE_SHA256" == "b085c65586a953cdb4b13c6390d63ec984d66912e4b6a19e66ba3582f2ed104b" ]] || \
  fail "yt-dlp 固定许可证哈希不受支持"

testing="${WEB_VIDEO_PACKAGE_TESTING:-}"
fixture_dir="${WEB_VIDEO_PACKAGE_TEST_SOURCE_DIR:-}"
force_refresh="${WEB_VIDEO_PACKAGE_TEST_FORCE_REFRESH:-}"
test_binary_sha="${WEB_VIDEO_PACKAGE_TEST_BINARY_SHA256:-}"
test_license_sha="${WEB_VIDEO_PACKAGE_TEST_LICENSE_SHA256:-}"
if [[ "$testing" != "1" ]]; then
  [[ -z "$fixture_dir$force_refresh$test_binary_sha$test_license_sha" ]] || \
    fail "非测试模式不得覆盖 yt-dlp 来源或哈希"
else
  [[ -n "$fixture_dir" && ! -L "$fixture_dir" && -d "$fixture_dir" ]] || fail "测试 fixture 目录无效"
  fixture_real="${fixture_dir:A}"
  [[ "$fixture_real" == "$repo_real"/work/* ]] || fail "测试 fixture 必须位于项目 work/ 内"
  [[ "$test_binary_sha" =~ '^[0-9a-f]{64}$' ]] || fail "测试二进制哈希无效"
  [[ "$test_license_sha" =~ '^[0-9a-f]{64}$' ]] || fail "测试许可证哈希无效"
  [[ -z "$force_refresh" || "$force_refresh" == "1" ]] || fail "测试刷新开关无效"
fi

vendor_root="$repo_root/work/vendor"
cache_key="$YTDLP_VERSION"
if [[ "$testing" == "1" ]]; then
  cache_key="test-${test_binary_sha[1,16]}-${test_license_sha[1,16]}"
fi
cache_dir="$vendor_root/yt-dlp/$cache_key"
for directory in "$vendor_root" "${cache_dir:h}" "$cache_dir"; do
  [[ ! -L "$directory" ]] || fail "拒绝使用符号链接缓存目录：$directory"
  mkdir -p "$directory"
  [[ "${directory:A}" == "$repo_real"/work/* ]] || fail "缓存目录越出项目 work/"
done
cache_binary="$cache_dir/yt-dlp_macos"
cache_license="$cache_dir/THIRD_PARTY_LICENSES.txt"
expected_binary_sha="$YTDLP_MACOS_SHA256"
expected_license_sha="$YTDLP_LICENSE_SHA256"
if [[ "$testing" == "1" ]]; then
  expected_binary_sha="$test_binary_sha"
  expected_license_sha="$test_license_sha"
fi

verified_file() {
  local path="$1"
  local expected="$2"
  [[ -f "$path" && ! -L "$path" ]] || return 1
  [[ "$(/usr/bin/shasum -a 256 "$path" | /usr/bin/awk '{print $1}')" == "$expected" ]]
}

if [[ "$force_refresh" != "1" ]] && verified_file "$cache_binary" "$expected_binary_sha" \
  && verified_file "$cache_license" "$expected_license_sha"; then
  /bin/chmod 0755 "$cache_binary"
  /bin/chmod 0644 "$cache_license"
  print -- "yt-dlp $YTDLP_VERSION 校验通过"
  print -- "work/vendor/yt-dlp/$cache_key/yt-dlp_macos"
  print -- "work/vendor/yt-dlp/$cache_key/THIRD_PARTY_LICENSES.txt"
  exit 0
fi

temp_binary=""
temp_license=""
cleanup() {
  local exit_status=$?
  local temp_path
  trap - EXIT INT TERM
  for temp_path in "$temp_binary" "$temp_license"; do
    if [[ -n "$temp_path" && "${temp_path:h:A}" == "${vendor_root:A}" ]]; then
      /bin/rm -f -- "$temp_path"
    fi
  done
  return "$exit_status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
temp_binary="$(/usr/bin/mktemp "$vendor_root/.yt-dlp-macos.XXXXXX")"
temp_license="$(/usr/bin/mktemp "$vendor_root/.yt-dlp-license.XXXXXX")"

if [[ "$testing" == "1" ]]; then
  fixture_binary="$fixture_real/yt-dlp_macos"
  fixture_license="$fixture_real/THIRD_PARTY_LICENSES.txt"
  [[ -f "$fixture_binary" && ! -L "$fixture_binary" ]] || fail "测试 fixture 缺少普通解析器文件"
  [[ -f "$fixture_license" && ! -L "$fixture_license" ]] || fail "测试 fixture 缺少普通许可证文件"
  /bin/cp -p "$fixture_binary" "$temp_binary"
  /bin/cp -p "$fixture_license" "$temp_license"
else
  /usr/bin/curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error \
    "$binary_url" --output "$temp_binary"
  /usr/bin/curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error \
    "$license_url" --output "$temp_license"
fi

verified_file "$temp_binary" "$expected_binary_sha" || fail "yt-dlp_macos SHA256 校验失败"
verified_file "$temp_license" "$expected_license_sha" || fail "yt-dlp 许可证 SHA256 校验失败"
/bin/chmod 0755 "$temp_binary"
/bin/chmod 0644 "$temp_license"

publish_verified() {
  local source_path="$1"
  local destination_path="$2"
  local expected="$3"
  if /bin/ln "$source_path" "$destination_path" 2>/dev/null; then
    /bin/rm -f -- "$source_path"
  else
    verified_file "$destination_path" "$expected" || fail "已有缓存未通过校验，拒绝覆盖"
    /bin/rm -f -- "$source_path"
  fi
}

publish_verified "$temp_binary" "$cache_binary" "$expected_binary_sha"
publish_verified "$temp_license" "$cache_license" "$expected_license_sha"
verified_file "$cache_binary" "$expected_binary_sha" || fail "发布后的 yt-dlp_macos 校验失败"
verified_file "$cache_license" "$expected_license_sha" || fail "发布后的 yt-dlp 许可证校验失败"
[[ -x "$cache_binary" ]] || fail "发布后的 yt-dlp_macos 不可执行"
temp_binary=""
temp_license=""
print -- "yt-dlp $YTDLP_VERSION 校验通过"
print -- "work/vendor/yt-dlp/$cache_key/yt-dlp_macos"
print -- "work/vendor/yt-dlp/$cache_key/THIRD_PARTY_LICENSES.txt"
