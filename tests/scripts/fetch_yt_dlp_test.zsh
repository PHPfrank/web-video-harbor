#!/bin/zsh
set -euo pipefail
unsetopt BG_NICE

script_dir="${0:A:h}"
repo_root="${script_dir:h:h}"
fetch_script="$repo_root/scripts/fetch-yt-dlp.zsh"
manifest="$repo_root/third_party/yt-dlp.env"
test_root="$repo_root/work/fetch-yt-dlp-test"
fixture_dir="$test_root/fixture"
outside_dir="${TMPDIR:-/tmp}/web-video-harbor-outside-fixture"

fail() {
  print -u2 -- "FAIL: $1"
  exit 1
}

[[ -f "$manifest" && ! -L "$manifest" ]] || fail "缺少 third_party/yt-dlp.env"
[[ -x "$fetch_script" && ! -L "$fetch_script" ]] || fail "缺少可执行 fetch-yt-dlp.zsh"
/bin/zsh -o NO_BG_NICE -n "$fetch_script" || fail "fetch-yt-dlp.zsh 语法错误"

source "$manifest"
[[ "$YTDLP_VERSION" == "2026.07.04" ]] || fail "固定 yt-dlp 版本错误"
[[ "$YTDLP_MACOS_SHA256" == "498bd0dae17855c599d371d68ec5bafc439a9d8640e838be25c765a9792f261b" ]] || \
  fail "固定 macOS 解析器 SHA256 错误"
[[ "$YTDLP_LICENSE_SHA256" == "b085c65586a953cdb4b13c6390d63ec984d66912e4b6a19e66ba3582f2ed104b" ]] || \
  fail "固定许可证 SHA256 错误"

fetch_text="$(<"$fetch_script")"
[[ "$fetch_text" == *'for required_command in curl shasum mktemp rg'* ]] || \
  fail "fetcher 没有在使用 rg 前检查命令可用性"
for fixed_url in \
  "https://github.com/yt-dlp/yt-dlp/releases/download/2026.07.04/yt-dlp_macos" \
  "https://raw.githubusercontent.com/yt-dlp/yt-dlp/2026.07.04/THIRD_PARTY_LICENSES.txt"; do
  [[ "$fetch_text" == *"$fixed_url"* ]] || fail "fetcher 缺少固定 HTTPS URL：$fixed_url"
done
[[ "$fetch_text" == *"--proto '=https'"*"--tlsv1.2"*"--fail"*"--location"* ]] || \
  fail "生产下载没有固定 HTTPS/TLS/curl 失败策略"
[[ "$fetch_text" != *'${YTDLP_MACOS_URL'* && "$fetch_text" != *'${YTDLP_LICENSE_URL'* ]] || \
  fail "生产下载 URL 可被环境变量覆盖"
[[ "$fetch_text" == *'/bin/ln "$source_path" "$destination_path"'* ]] || \
  fail "缓存发布没有使用原子 no-clobber 链接"

mkdir -p "$fixture_dir"
print -r -- '#!/bin/zsh' >"$fixture_dir/yt-dlp_macos"
print -r -- '[[ "${1:-}" == "--version" ]] || exit 2' >>"$fixture_dir/yt-dlp_macos"
print -r -- 'print -- 2026.07.04' >>"$fixture_dir/yt-dlp_macos"
chmod 0755 "$fixture_dir/yt-dlp_macos"
print -r -- 'fixture yt-dlp third-party licenses' >"$fixture_dir/THIRD_PARTY_LICENSES.txt"

fixture_binary_sha="$(/usr/bin/shasum -a 256 "$fixture_dir/yt-dlp_macos" | awk '{print $1}')"
fixture_license_sha="$(/usr/bin/shasum -a 256 "$fixture_dir/THIRD_PARTY_LICENSES.txt" | awk '{print $1}')"

if env WEB_VIDEO_PACKAGE_TEST_SOURCE_DIR="$fixture_dir" \
  WEB_VIDEO_PACKAGE_TEST_BINARY_SHA256="$fixture_binary_sha" \
  WEB_VIDEO_PACKAGE_TEST_LICENSE_SHA256="$fixture_license_sha" \
  /bin/zsh "$fetch_script" >"$test_root/non-test.txt" 2>&1; then
  fail "非测试模式接受了 fixture 注入"
fi

mkdir -p "$outside_dir"
if env WEB_VIDEO_PACKAGE_TESTING=1 WEB_VIDEO_PACKAGE_TEST_SOURCE_DIR="$outside_dir" \
  WEB_VIDEO_PACKAGE_TEST_BINARY_SHA256="$fixture_binary_sha" \
  WEB_VIDEO_PACKAGE_TEST_LICENSE_SHA256="$fixture_license_sha" \
  /bin/zsh "$fetch_script" >"$test_root/outside.txt" 2>&1; then
  fail "测试模式接受了项目 work/ 外 fixture"
fi

result="$(env YTDLP_VERSION=1900.01.01 YTDLP_MACOS_SHA256="$(printf '0%.0s' {1..64})" \
  YTDLP_LICENSE_SHA256="$(printf '1%.0s' {1..64})" \
  WEB_VIDEO_PACKAGE_TESTING=1 WEB_VIDEO_PACKAGE_TEST_SOURCE_DIR="$fixture_dir" \
  WEB_VIDEO_PACKAGE_TEST_BINARY_SHA256="$fixture_binary_sha" \
  WEB_VIDEO_PACKAGE_TEST_LICENSE_SHA256="$fixture_license_sha" \
  /bin/zsh "$fetch_script")"
[[ "$result" == *"yt-dlp 2026.07.04 校验通过"* ]] || fail "fetcher 没有报告固定版本"
cache_dir="$repo_root/work/vendor/yt-dlp/test-${fixture_binary_sha[1,16]}-${fixture_license_sha[1,16]}"
cache_binary="$cache_dir/yt-dlp_macos"
cache_license="$cache_dir/THIRD_PARTY_LICENSES.txt"
[[ -x "$cache_binary" && ! -L "$cache_binary" ]] || fail "缓存解析器无效"
[[ -f "$cache_license" && ! -L "$cache_license" ]] || fail "缓存许可证无效"
before_binary="$(/usr/bin/shasum -a 256 "$cache_binary" | awk '{print $1}')"
before_license="$(/usr/bin/shasum -a 256 "$cache_license" | awk '{print $1}')"

print -r -- 'bad response' >"$fixture_dir/yt-dlp_macos"
if env WEB_VIDEO_PACKAGE_TESTING=1 WEB_VIDEO_PACKAGE_TEST_FORCE_REFRESH=1 \
  WEB_VIDEO_PACKAGE_TEST_SOURCE_DIR="$fixture_dir" \
  WEB_VIDEO_PACKAGE_TEST_BINARY_SHA256="$fixture_binary_sha" \
  WEB_VIDEO_PACKAGE_TEST_LICENSE_SHA256="$fixture_license_sha" \
  /bin/zsh "$fetch_script" >"$test_root/bad-response.txt" 2>&1; then
  fail "错误校验和响应被接受"
fi
[[ "$(/usr/bin/shasum -a 256 "$cache_binary" | awk '{print $1}')" == "$before_binary" ]] || \
  fail "错误响应覆盖了已验证解析器缓存"
[[ "$(/usr/bin/shasum -a 256 "$cache_license" | awk '{print $1}')" == "$before_license" ]] || \
  fail "错误响应修改了已验证许可证缓存"

print -r -- '#!/bin/zsh' >"$fixture_dir/yt-dlp_macos"
print -r -- '[[ "${1:-}" == "--version" ]] || exit 2' >>"$fixture_dir/yt-dlp_macos"
print -r -- 'print -- 2026.07.04' >>"$fixture_dir/yt-dlp_macos"
chmod 0755 "$fixture_dir/yt-dlp_macos"
print -r -- 'bad license response' >"$fixture_dir/THIRD_PARTY_LICENSES.txt"
if env WEB_VIDEO_PACKAGE_TESTING=1 WEB_VIDEO_PACKAGE_TEST_FORCE_REFRESH=1 \
  WEB_VIDEO_PACKAGE_TEST_SOURCE_DIR="$fixture_dir" \
  WEB_VIDEO_PACKAGE_TEST_BINARY_SHA256="$fixture_binary_sha" \
  WEB_VIDEO_PACKAGE_TEST_LICENSE_SHA256="$fixture_license_sha" \
  /bin/zsh "$fetch_script" >"$test_root/bad-license-response.txt" 2>&1; then
  fail "错误许可证校验和响应被接受"
fi
[[ "$(/usr/bin/shasum -a 256 "$cache_binary" | awk '{print $1}')" == "$before_binary" ]] || \
  fail "错误许可证响应修改了已验证解析器缓存"
[[ "$(/usr/bin/shasum -a 256 "$cache_license" | awk '{print $1}')" == "$before_license" ]] || \
  fail "错误许可证响应覆盖了已验证许可证缓存"

concurrent_fixture="$test_root/concurrent-fixture"
mkdir -p "$concurrent_fixture"
print -r -- '#!/bin/zsh' >"$concurrent_fixture/yt-dlp_macos"
print -r -- '# concurrent publication fixture' >>"$concurrent_fixture/yt-dlp_macos"
print -r -- '[[ "${1:-}" == "--version" ]] || exit 2' >>"$concurrent_fixture/yt-dlp_macos"
print -r -- 'print -- 2026.07.04' >>"$concurrent_fixture/yt-dlp_macos"
chmod 0755 "$concurrent_fixture/yt-dlp_macos"
print -r -- 'concurrent fixture license' >"$concurrent_fixture/THIRD_PARTY_LICENSES.txt"
concurrent_binary_sha="$(/usr/bin/shasum -a 256 "$concurrent_fixture/yt-dlp_macos" | awk '{print $1}')"
concurrent_license_sha="$(/usr/bin/shasum -a 256 "$concurrent_fixture/THIRD_PARTY_LICENSES.txt" | awk '{print $1}')"
concurrent_cache="$repo_root/work/vendor/yt-dlp/test-${concurrent_binary_sha[1,16]}-${concurrent_license_sha[1,16]}"
if [[ -d "$concurrent_cache" && ! -L "$concurrent_cache" ]]; then
  /bin/rm -rf -- "$concurrent_cache"
fi
concurrent_pids=()
for worker in {1..6}; do
  env WEB_VIDEO_PACKAGE_TESTING=1 WEB_VIDEO_PACKAGE_TEST_SOURCE_DIR="$concurrent_fixture" \
    WEB_VIDEO_PACKAGE_TEST_BINARY_SHA256="$concurrent_binary_sha" \
    WEB_VIDEO_PACKAGE_TEST_LICENSE_SHA256="$concurrent_license_sha" \
    /bin/zsh "$fetch_script" >"$test_root/concurrent-$worker.txt" 2>&1 &
  concurrent_pids+=("$!")
done
for worker_pid in $concurrent_pids; do
  wait "$worker_pid" || fail "并发 fetcher 失败"
done
[[ "$(/usr/bin/shasum -a 256 "$concurrent_cache/yt-dlp_macos" | awk '{print $1}')" == \
  "$concurrent_binary_sha" ]] || fail "并发发布后的解析器缓存无效"
[[ "$(/usr/bin/shasum -a 256 "$concurrent_cache/THIRD_PARTY_LICENSES.txt" | awk '{print $1}')" == \
  "$concurrent_license_sha" ]] || fail "并发发布后的许可证缓存无效"

symlink_fixture="$test_root/symlink-fixture"
mkdir -p "$symlink_fixture"
print -r -- '#!/bin/zsh' >"$symlink_fixture/yt-dlp_macos"
print -r -- '# symlink publication fixture' >>"$symlink_fixture/yt-dlp_macos"
print -r -- '[[ "${1:-}" == "--version" ]] || exit 2' >>"$symlink_fixture/yt-dlp_macos"
print -r -- 'print -- 2026.07.04' >>"$symlink_fixture/yt-dlp_macos"
chmod 0755 "$symlink_fixture/yt-dlp_macos"
print -r -- 'symlink fixture license' >"$symlink_fixture/THIRD_PARTY_LICENSES.txt"
symlink_binary_sha="$(/usr/bin/shasum -a 256 "$symlink_fixture/yt-dlp_macos" | awk '{print $1}')"
symlink_license_sha="$(/usr/bin/shasum -a 256 "$symlink_fixture/THIRD_PARTY_LICENSES.txt" | awk '{print $1}')"
symlink_cache="$repo_root/work/vendor/yt-dlp/test-${symlink_binary_sha[1,16]}-${symlink_license_sha[1,16]}"
if [[ -d "$symlink_cache" && ! -L "$symlink_cache" ]]; then
  /bin/rm -rf -- "$symlink_cache"
fi
mkdir -p "$symlink_cache"
symlink_victim="$test_root/symlink-victim"
print -r -- 'do not overwrite' >"$symlink_victim"
/bin/ln -s "$symlink_victim" "$symlink_cache/yt-dlp_macos"
if env WEB_VIDEO_PACKAGE_TESTING=1 WEB_VIDEO_PACKAGE_TEST_SOURCE_DIR="$symlink_fixture" \
  WEB_VIDEO_PACKAGE_TEST_BINARY_SHA256="$symlink_binary_sha" \
  WEB_VIDEO_PACKAGE_TEST_LICENSE_SHA256="$symlink_license_sha" \
  /bin/zsh "$fetch_script" >"$test_root/symlink-cache.txt" 2>&1; then
  fail "fetcher 接受了符号链接缓存目标"
fi
[[ "$(<"$symlink_victim")" == 'do not overwrite' ]] || fail "fetcher 跟随缓存符号链接覆盖了目标"

if /usr/bin/find "$repo_root/work/vendor" -type f -name '*.part' -print -quit | rg -q .; then
  fail "fetcher 遗留 .part 文件"
fi
if /usr/bin/find "$repo_root/work/vendor" -type f -name '.yt-dlp-*' -print -quit | rg -q .; then
  fail "fetcher 遗留随机下载临时文件"
fi

print -- "yt-dlp 固定获取测试通过：版本、哈希、fixture 边界和缓存不覆盖。"
