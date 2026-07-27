#!/bin/zsh
set -euo pipefail
unsetopt BG_NICE

script_dir="${0:A:h}"
repo_root="${script_dir:h}"
repo_real="${repo_root:A}"
manifest="$repo_root/third_party/deno.env"
arm64_url="https://github.com/denoland/deno/releases/download/v2.8.1/deno-aarch64-apple-darwin.zip"
amd64_url="https://github.com/denoland/deno/releases/download/v2.8.1/deno-x86_64-apple-darwin.zip"
license_url="https://raw.githubusercontent.com/denoland/deno/v2.8.1/LICENSE.md"

fail() {
  print -u2 -- "$1"
  exit 1
}

for required_command in curl shasum mktemp rg unzip; do
  command -v "$required_command" >/dev/null 2>&1 || fail "缺少获取命令：$required_command"
done
[[ -f "$manifest" && ! -L "$manifest" ]] || fail "缺少安全的 third_party/deno.env"
if rg -v '^(DENO_VERSION=2\.8\.1|DENO_(ARM64|AMD64)_ZIP_SHA256=[0-9a-f]{64}|DENO_(ARM64|AMD64)_BINARY_SHA256=[0-9a-f]{64}|DENO_LICENSE_SHA256=[0-9a-f]{64})$' \
  "$manifest" | rg -q .; then
  fail "Deno 固定清单含无效字段"
fi
[[ "$(rg -c '^DENO_' "$manifest")" == "6" ]] || fail "Deno 固定清单字段数量错误"
source "$manifest"
[[ "$DENO_VERSION" == "2.8.1" ]] || fail "Deno 固定版本不受支持"
[[ "$DENO_ARM64_ZIP_SHA256" == "8154e2de0ee8c1cae31fa88e078724aaef0295fab9fd2ad6f8520389cee908f6" ]] || \
  fail "Deno arm64 ZIP 哈希不受支持"
[[ "$DENO_AMD64_ZIP_SHA256" == "47473845e0522ba11dd279e3dd318e2d84ee200c56b8280594e0ae0b0f827460" ]] || \
  fail "Deno x86_64 ZIP 哈希不受支持"
[[ "$DENO_ARM64_BINARY_SHA256" == "ce89814370dfcd0163ed03580bea2f01766b791d1531f0366da4f649663f77e4" ]] || \
  fail "Deno arm64 二进制哈希不受支持"
[[ "$DENO_AMD64_BINARY_SHA256" == "a38eb0bd7716493a046d4cb49af27efbc43232d9b3894406ffcd57f38e2d93cd" ]] || \
  fail "Deno x86_64 二进制哈希不受支持"
[[ "$DENO_LICENSE_SHA256" == "f62497fffecc0852960c8d3e6934b9db86d16396e9b604072e923892cae3a588" ]] || \
  fail "Deno 许可证哈希不受支持"

testing="${WEB_VIDEO_DENO_TESTING:-}"
fixture_dir="${WEB_VIDEO_DENO_TEST_SOURCE_DIR:-}"
force_refresh="${WEB_VIDEO_DENO_TEST_FORCE_REFRESH:-}"
test_arm64_zip_sha="${WEB_VIDEO_DENO_TEST_ARM64_ZIP_SHA256:-}"
test_amd64_zip_sha="${WEB_VIDEO_DENO_TEST_AMD64_ZIP_SHA256:-}"
test_arm64_binary_sha="${WEB_VIDEO_DENO_TEST_ARM64_BINARY_SHA256:-}"
test_amd64_binary_sha="${WEB_VIDEO_DENO_TEST_AMD64_BINARY_SHA256:-}"
test_license_sha="${WEB_VIDEO_DENO_TEST_LICENSE_SHA256:-}"
test_inputs="$fixture_dir$force_refresh$test_arm64_zip_sha$test_amd64_zip_sha$test_arm64_binary_sha$test_amd64_binary_sha$test_license_sha"
if [[ "$testing" != "1" ]]; then
  [[ -z "$test_inputs" ]] || fail "非测试模式不得覆盖 Deno 来源或哈希"
else
  [[ -n "$fixture_dir" && ! -L "$fixture_dir" && -d "$fixture_dir" ]] || fail "Deno 测试 fixture 目录无效"
  fixture_real="${fixture_dir:A}"
  [[ "$fixture_real" == "$repo_real"/work/* ]] || fail "Deno 测试 fixture 必须位于项目 work/ 内"
  for test_sha in "$test_arm64_zip_sha" "$test_amd64_zip_sha" "$test_arm64_binary_sha" \
    "$test_amd64_binary_sha" "$test_license_sha"; do
    [[ "$test_sha" =~ '^[0-9a-f]{64}$' ]] || fail "Deno 测试哈希无效"
  done
  [[ -z "$force_refresh" || "$force_refresh" == "1" ]] || fail "Deno 测试刷新开关无效"
fi

expected_arm64_zip_sha="$DENO_ARM64_ZIP_SHA256"
expected_amd64_zip_sha="$DENO_AMD64_ZIP_SHA256"
expected_arm64_binary_sha="$DENO_ARM64_BINARY_SHA256"
expected_amd64_binary_sha="$DENO_AMD64_BINARY_SHA256"
expected_license_sha="$DENO_LICENSE_SHA256"
cache_key="$DENO_VERSION"
if [[ "$testing" == "1" ]]; then
  expected_arm64_zip_sha="$test_arm64_zip_sha"
  expected_amd64_zip_sha="$test_amd64_zip_sha"
  expected_arm64_binary_sha="$test_arm64_binary_sha"
  expected_amd64_binary_sha="$test_amd64_binary_sha"
  expected_license_sha="$test_license_sha"
  cache_key="test-${test_arm64_zip_sha[1,12]}-${test_amd64_zip_sha[1,12]}-${test_license_sha[1,12]}"
fi

vendor_root="$repo_root/work/vendor"
cache_dir="$vendor_root/deno/$cache_key"
for directory in "$vendor_root" "${cache_dir:h}" "$cache_dir"; do
  [[ ! -L "$directory" ]] || fail "拒绝使用符号链接 Deno 缓存目录：$directory"
  mkdir -p "$directory"
  [[ "${directory:A}" == "$repo_real"/work/* ]] || fail "Deno 缓存目录越出项目 work/"
done
cache_arm64="$cache_dir/deno_macos_arm64"
cache_amd64="$cache_dir/deno_macos_x86_64"
cache_license="$cache_dir/LICENSE.md"

verified_file() {
  local path="$1"
  local expected="$2"
  [[ -f "$path" && ! -L "$path" ]] || return 1
  [[ "$(/usr/bin/shasum -a 256 "$path" | /usr/bin/awk '{print $1}')" == "$expected" ]]
}

if [[ "$force_refresh" != "1" ]] && verified_file "$cache_arm64" "$expected_arm64_binary_sha" \
  && verified_file "$cache_amd64" "$expected_amd64_binary_sha" \
  && verified_file "$cache_license" "$expected_license_sha"; then
  /bin/chmod 0755 "$cache_arm64" "$cache_amd64"
  /bin/chmod 0644 "$cache_license"
  print -- "Deno $DENO_VERSION 校验通过"
  print -- "work/vendor/deno/$cache_key/deno_macos_arm64"
  print -- "work/vendor/deno/$cache_key/deno_macos_x86_64"
  print -- "work/vendor/deno/$cache_key/LICENSE.md"
  exit 0
fi

temp_arm64_zip=""
temp_amd64_zip=""
temp_license=""
arm64_extract=""
amd64_extract=""
cleanup() {
  local exit_status=$?
  local temp_path
  trap - EXIT INT TERM
  for temp_path in "$temp_arm64_zip" "$temp_amd64_zip" "$temp_license"; do
    if [[ -n "$temp_path" && "${temp_path:h:A}" == "${vendor_root:A}" ]]; then
      /bin/rm -f -- "$temp_path"
    fi
  done
  for temp_path in "$arm64_extract" "$amd64_extract"; do
    if [[ -n "$temp_path" && "${temp_path:h:A}" == "${vendor_root:A}" && "${temp_path:t}" == .deno-extract.?????? ]]; then
      [[ ! -e "$temp_path" && ! -L "$temp_path" ]] || /bin/rm -rf -- "$temp_path"
    fi
  done
  return "$exit_status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
temp_arm64_zip="$(/usr/bin/mktemp "$vendor_root/.deno-arm64.XXXXXX")"
temp_amd64_zip="$(/usr/bin/mktemp "$vendor_root/.deno-amd64.XXXXXX")"
temp_license="$(/usr/bin/mktemp "$vendor_root/.deno-license.XXXXXX")"
arm64_extract="$(/usr/bin/mktemp -d "$vendor_root/.deno-extract.XXXXXX")"
amd64_extract="$(/usr/bin/mktemp -d "$vendor_root/.deno-extract.XXXXXX")"

if [[ "$testing" == "1" ]]; then
  fixture_arm64_zip="$fixture_real/deno-aarch64-apple-darwin.zip"
  fixture_amd64_zip="$fixture_real/deno-x86_64-apple-darwin.zip"
  fixture_license="$fixture_real/LICENSE.md"
  for fixture_file in "$fixture_arm64_zip" "$fixture_amd64_zip" "$fixture_license"; do
    [[ -f "$fixture_file" && ! -L "$fixture_file" ]] || fail "Deno 测试 fixture 缺少普通文件"
  done
  /bin/cp -p "$fixture_arm64_zip" "$temp_arm64_zip"
  /bin/cp -p "$fixture_amd64_zip" "$temp_amd64_zip"
  /bin/cp -p "$fixture_license" "$temp_license"
else
  /usr/bin/curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error \
    "$arm64_url" --output "$temp_arm64_zip"
  /usr/bin/curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error \
    "$amd64_url" --output "$temp_amd64_zip"
  /usr/bin/curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error \
    "$license_url" --output "$temp_license"
fi

verified_file "$temp_arm64_zip" "$expected_arm64_zip_sha" || fail "Deno arm64 ZIP SHA256 校验失败"
verified_file "$temp_amd64_zip" "$expected_amd64_zip_sha" || fail "Deno x86_64 ZIP SHA256 校验失败"
verified_file "$temp_license" "$expected_license_sha" || fail "Deno 许可证 SHA256 校验失败"

extract_single_deno() {
  local archive="$1"
  local destination="$2"
  local expected_binary_sha="$3"
  local entries
  entries="$(/usr/bin/unzip -Z -1 "$archive")" || fail "Deno ZIP 内容不可读"
  [[ "$entries" == "deno" ]] || fail "Deno ZIP 必须只包含单一 deno 文件"
  /usr/bin/unzip -qq "$archive" -d "$destination" || fail "Deno ZIP 解压失败"
  local binary="$destination/deno"
  [[ -f "$binary" && ! -L "$binary" ]] || fail "Deno ZIP 未解压出普通 deno 文件"
  verified_file "$binary" "$expected_binary_sha" || fail "Deno 二进制 SHA256 校验失败"
  /bin/chmod 0755 "$binary"
}

extract_single_deno "$temp_arm64_zip" "$arm64_extract" "$expected_arm64_binary_sha"
extract_single_deno "$temp_amd64_zip" "$amd64_extract" "$expected_amd64_binary_sha"
/bin/chmod 0644 "$temp_license"

publish_verified() {
  local source_path="$1"
  local destination_path="$2"
  local expected="$3"
  if /bin/ln "$source_path" "$destination_path" 2>/dev/null; then
    /bin/rm -f -- "$source_path"
  else
    verified_file "$destination_path" "$expected" || fail "已有 Deno 缓存未通过校验，拒绝覆盖"
    /bin/rm -f -- "$source_path"
  fi
}

publish_verified "$arm64_extract/deno" "$cache_arm64" "$expected_arm64_binary_sha"
publish_verified "$amd64_extract/deno" "$cache_amd64" "$expected_amd64_binary_sha"
publish_verified "$temp_license" "$cache_license" "$expected_license_sha"
verified_file "$cache_arm64" "$expected_arm64_binary_sha" || fail "发布后的 arm64 Deno 校验失败"
verified_file "$cache_amd64" "$expected_amd64_binary_sha" || fail "发布后的 x86_64 Deno 校验失败"
verified_file "$cache_license" "$expected_license_sha" || fail "发布后的 Deno 许可证校验失败"
[[ -x "$cache_arm64" && -x "$cache_amd64" ]] || fail "发布后的 Deno 不可执行"
temp_license=""
print -- "Deno $DENO_VERSION 校验通过"
print -- "work/vendor/deno/$cache_key/deno_macos_arm64"
print -- "work/vendor/deno/$cache_key/deno_macos_x86_64"
print -- "work/vendor/deno/$cache_key/LICENSE.md"
