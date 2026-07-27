#!/bin/zsh
set -euo pipefail
unsetopt BG_NICE

script_dir="${0:A:h}"
repo_root="${script_dir:h:h}"
fetch_script="$repo_root/scripts/fetch-deno.zsh"
manifest="$repo_root/third_party/deno.env"
test_root="$repo_root/work/fetch-deno-test"
fixture_dir="$test_root/fixture"
outside_dir="${TMPDIR:-/tmp}/web-video-harbor-deno-outside-fixture"

fail() {
  print -u2 -- "FAIL: $1"
  exit 1
}

[[ -f "$manifest" && ! -L "$manifest" ]] || fail "缺少 third_party/deno.env"
[[ -x "$fetch_script" && ! -L "$fetch_script" ]] || fail "缺少可执行 fetch-deno.zsh"
/bin/zsh -o NO_BG_NICE -n "$fetch_script" || fail "fetch-deno.zsh 语法错误"

source "$manifest"
[[ "$DENO_VERSION" == "2.8.1" ]] || fail "固定 Deno 版本错误"
[[ "$DENO_ARM64_ZIP_SHA256" == "8154e2de0ee8c1cae31fa88e078724aaef0295fab9fd2ad6f8520389cee908f6" ]] || \
  fail "固定 arm64 ZIP SHA256 错误"
[[ "$DENO_AMD64_ZIP_SHA256" == "47473845e0522ba11dd279e3dd318e2d84ee200c56b8280594e0ae0b0f827460" ]] || \
  fail "固定 x86_64 ZIP SHA256 错误"
[[ "$DENO_ARM64_BINARY_SHA256" == "ce89814370dfcd0163ed03580bea2f01766b791d1531f0366da4f649663f77e4" ]] || \
  fail "固定 arm64 二进制 SHA256 错误"
[[ "$DENO_AMD64_BINARY_SHA256" == "a38eb0bd7716493a046d4cb49af27efbc43232d9b3894406ffcd57f38e2d93cd" ]] || \
  fail "固定 x86_64 二进制 SHA256 错误"
[[ "$DENO_LICENSE_SHA256" == "f62497fffecc0852960c8d3e6934b9db86d16396e9b604072e923892cae3a588" ]] || \
  fail "固定许可证 SHA256 错误"

fetch_text="$(<"$fetch_script")"
for fixed_url in \
  "https://github.com/denoland/deno/releases/download/v2.8.1/deno-aarch64-apple-darwin.zip" \
  "https://github.com/denoland/deno/releases/download/v2.8.1/deno-x86_64-apple-darwin.zip" \
  "https://raw.githubusercontent.com/denoland/deno/v2.8.1/LICENSE.md"; do
  [[ "$fetch_text" == *"$fixed_url"* ]] || fail "fetcher 缺少固定 HTTPS URL：$fixed_url"
done
[[ "$fetch_text" == *"--proto '=https'"*"--tlsv1.2"*"--fail"*"--location"* ]] || \
  fail "生产下载没有固定 HTTPS/TLS/curl 失败策略"
[[ "$fetch_text" == *'/bin/ln "$source_path" "$destination_path"'* ]] || \
  fail "缓存发布没有使用原子 no-clobber 链接"
[[ "$fetch_text" != *'${DENO_ARM64_URL'* && "$fetch_text" != *'${DENO_AMD64_URL'* ]] || \
  fail "生产下载 URL 可被环境变量覆盖"

mkdir -p "$fixture_dir/arm64" "$fixture_dir/amd64"
print -r -- '#!/bin/zsh' >"$fixture_dir/arm64/deno"
print -r -- 'print -- "deno 2.8.1 arm64 fixture"' >>"$fixture_dir/arm64/deno"
print -r -- '#!/bin/zsh' >"$fixture_dir/amd64/deno"
print -r -- 'print -- "deno 2.8.1 amd64 fixture"' >>"$fixture_dir/amd64/deno"
chmod 0755 "$fixture_dir/arm64/deno" "$fixture_dir/amd64/deno"
print -r -- 'fixture Deno license' >"$fixture_dir/LICENSE.md"
/bin/rm -f -- "$fixture_dir/deno-aarch64-apple-darwin.zip" \
  "$fixture_dir/deno-x86_64-apple-darwin.zip"
/usr/bin/zip -q -j "$fixture_dir/deno-aarch64-apple-darwin.zip" "$fixture_dir/arm64/deno"
/usr/bin/zip -q -j "$fixture_dir/deno-x86_64-apple-darwin.zip" "$fixture_dir/amd64/deno"

arm64_zip_sha="$(/usr/bin/shasum -a 256 "$fixture_dir/deno-aarch64-apple-darwin.zip" | awk '{print $1}')"
amd64_zip_sha="$(/usr/bin/shasum -a 256 "$fixture_dir/deno-x86_64-apple-darwin.zip" | awk '{print $1}')"
arm64_binary_sha="$(/usr/bin/shasum -a 256 "$fixture_dir/arm64/deno" | awk '{print $1}')"
amd64_binary_sha="$(/usr/bin/shasum -a 256 "$fixture_dir/amd64/deno" | awk '{print $1}')"
license_sha="$(/usr/bin/shasum -a 256 "$fixture_dir/LICENSE.md" | awk '{print $1}')"

deno_test_env=(
  WEB_VIDEO_DENO_TESTING=1
  WEB_VIDEO_DENO_TEST_SOURCE_DIR="$fixture_dir"
  WEB_VIDEO_DENO_TEST_ARM64_ZIP_SHA256="$arm64_zip_sha"
  WEB_VIDEO_DENO_TEST_AMD64_ZIP_SHA256="$amd64_zip_sha"
  WEB_VIDEO_DENO_TEST_ARM64_BINARY_SHA256="$arm64_binary_sha"
  WEB_VIDEO_DENO_TEST_AMD64_BINARY_SHA256="$amd64_binary_sha"
  WEB_VIDEO_DENO_TEST_LICENSE_SHA256="$license_sha"
)

if env "${deno_test_env[@]:1}" /bin/zsh "$fetch_script" >"$test_root/non-test.txt" 2>&1; then
  fail "非测试模式接受了 Deno fixture 注入"
fi

mkdir -p "$outside_dir"
if env WEB_VIDEO_DENO_TESTING=1 WEB_VIDEO_DENO_TEST_SOURCE_DIR="$outside_dir" \
  WEB_VIDEO_DENO_TEST_ARM64_ZIP_SHA256="$arm64_zip_sha" \
  WEB_VIDEO_DENO_TEST_AMD64_ZIP_SHA256="$amd64_zip_sha" \
  WEB_VIDEO_DENO_TEST_ARM64_BINARY_SHA256="$arm64_binary_sha" \
  WEB_VIDEO_DENO_TEST_AMD64_BINARY_SHA256="$amd64_binary_sha" \
  WEB_VIDEO_DENO_TEST_LICENSE_SHA256="$license_sha" \
  /bin/zsh "$fetch_script" >"$test_root/outside.txt" 2>&1; then
  fail "测试模式接受了项目 work/ 外 fixture"
fi

result="$(env "${deno_test_env[@]}" /bin/zsh "$fetch_script")"
[[ "$result" == *"Deno 2.8.1 校验通过"* ]] || fail "fetcher 没有报告固定版本"
cache_key="test-${arm64_zip_sha[1,12]}-${amd64_zip_sha[1,12]}-${license_sha[1,12]}"
cache_dir="$repo_root/work/vendor/deno/$cache_key"
arm64_binary="$cache_dir/deno_macos_arm64"
amd64_binary="$cache_dir/deno_macos_x86_64"
cache_license="$cache_dir/LICENSE.md"
[[ -x "$arm64_binary" && ! -L "$arm64_binary" ]] || fail "缓存 arm64 Deno 无效"
[[ -x "$amd64_binary" && ! -L "$amd64_binary" ]] || fail "缓存 x86_64 Deno 无效"
[[ -f "$cache_license" && ! -L "$cache_license" ]] || fail "缓存 Deno 许可证无效"
[[ "$(/usr/bin/shasum -a 256 "$arm64_binary" | awk '{print $1}')" == "$arm64_binary_sha" ]] || \
  fail "缓存 arm64 Deno 哈希错误"
[[ "$(/usr/bin/shasum -a 256 "$amd64_binary" | awk '{print $1}')" == "$amd64_binary_sha" ]] || \
  fail "缓存 x86_64 Deno 哈希错误"

before_arm64="$(/usr/bin/shasum -a 256 "$arm64_binary" | awk '{print $1}')"
print -r -- 'bad archive response' >"$fixture_dir/deno-aarch64-apple-darwin.zip"
if env "${deno_test_env[@]}" WEB_VIDEO_DENO_TEST_FORCE_REFRESH=1 \
  /bin/zsh "$fetch_script" >"$test_root/bad-response.txt" 2>&1; then
  fail "错误校验和 ZIP 被接受"
fi
[[ "$(/usr/bin/shasum -a 256 "$arm64_binary" | awk '{print $1}')" == "$before_arm64" ]] || \
  fail "错误响应覆盖了已验证 Deno 缓存"

/bin/rm -f -- "$fixture_dir/deno-aarch64-apple-darwin.zip"
/usr/bin/zip -q -j "$fixture_dir/deno-aarch64-apple-darwin.zip" "$fixture_dir/arm64/deno"
concurrent_fixture="$test_root/concurrent-fixture"
mkdir -p "$concurrent_fixture/arm64" "$concurrent_fixture/amd64"
/bin/cp -p "$fixture_dir/arm64/deno" "$concurrent_fixture/arm64/deno"
/bin/cp -p "$fixture_dir/amd64/deno" "$concurrent_fixture/amd64/deno"
/bin/cp -p "$fixture_dir/LICENSE.md" "$concurrent_fixture/LICENSE.md"
/bin/rm -f -- "$concurrent_fixture/deno-aarch64-apple-darwin.zip" \
  "$concurrent_fixture/deno-x86_64-apple-darwin.zip"
/usr/bin/zip -q -j "$concurrent_fixture/deno-aarch64-apple-darwin.zip" "$concurrent_fixture/arm64/deno"
/usr/bin/zip -q -j "$concurrent_fixture/deno-x86_64-apple-darwin.zip" "$concurrent_fixture/amd64/deno"
concurrent_arm64_zip_sha="$(/usr/bin/shasum -a 256 "$concurrent_fixture/deno-aarch64-apple-darwin.zip" | awk '{print $1}')"
concurrent_amd64_zip_sha="$(/usr/bin/shasum -a 256 "$concurrent_fixture/deno-x86_64-apple-darwin.zip" | awk '{print $1}')"
concurrent_cache="$repo_root/work/vendor/deno/test-${concurrent_arm64_zip_sha[1,12]}-${concurrent_amd64_zip_sha[1,12]}-${license_sha[1,12]}"
if [[ -d "$concurrent_cache" && ! -L "$concurrent_cache" ]]; then
  /bin/rm -rf -- "$concurrent_cache"
fi
concurrent_pids=()
for worker in {1..6}; do
  env WEB_VIDEO_DENO_TESTING=1 WEB_VIDEO_DENO_TEST_SOURCE_DIR="$concurrent_fixture" \
    WEB_VIDEO_DENO_TEST_ARM64_ZIP_SHA256="$concurrent_arm64_zip_sha" \
    WEB_VIDEO_DENO_TEST_AMD64_ZIP_SHA256="$concurrent_amd64_zip_sha" \
    WEB_VIDEO_DENO_TEST_ARM64_BINARY_SHA256="$arm64_binary_sha" \
    WEB_VIDEO_DENO_TEST_AMD64_BINARY_SHA256="$amd64_binary_sha" \
    WEB_VIDEO_DENO_TEST_LICENSE_SHA256="$license_sha" \
    /bin/zsh "$fetch_script" >"$test_root/concurrent-$worker.txt" 2>&1 &
  concurrent_pids+=("$!")
done
for worker_pid in $concurrent_pids; do
  wait "$worker_pid" || fail "并发 Deno fetcher 失败"
done
[[ "$(/usr/bin/shasum -a 256 "$concurrent_cache/deno_macos_arm64" | awk '{print $1}')" == \
  "$arm64_binary_sha" ]] || fail "并发发布后的 arm64 Deno 无效"
[[ "$(/usr/bin/shasum -a 256 "$concurrent_cache/deno_macos_x86_64" | awk '{print $1}')" == \
  "$amd64_binary_sha" ]] || fail "并发发布后的 x86_64 Deno 无效"

symlink_fixture="$test_root/symlink-fixture"
mkdir -p "$symlink_fixture/arm64" "$symlink_fixture/amd64"
/bin/cp -p "$fixture_dir/arm64/deno" "$symlink_fixture/arm64/deno"
/bin/cp -p "$fixture_dir/amd64/deno" "$symlink_fixture/amd64/deno"
/bin/cp -p "$fixture_dir/LICENSE.md" "$symlink_fixture/LICENSE.md"
print -r -- '# symlink-specific archive' >>"$symlink_fixture/arm64/deno"
/bin/rm -f -- "$symlink_fixture/deno-aarch64-apple-darwin.zip" \
  "$symlink_fixture/deno-x86_64-apple-darwin.zip"
/usr/bin/zip -q -j "$symlink_fixture/deno-aarch64-apple-darwin.zip" "$symlink_fixture/arm64/deno"
/usr/bin/zip -q -j "$symlink_fixture/deno-x86_64-apple-darwin.zip" "$symlink_fixture/amd64/deno"
symlink_arm64_zip_sha="$(/usr/bin/shasum -a 256 "$symlink_fixture/deno-aarch64-apple-darwin.zip" | awk '{print $1}')"
symlink_amd64_zip_sha="$(/usr/bin/shasum -a 256 "$symlink_fixture/deno-x86_64-apple-darwin.zip" | awk '{print $1}')"
symlink_arm64_binary_sha="$(/usr/bin/shasum -a 256 "$symlink_fixture/arm64/deno" | awk '{print $1}')"
symlink_cache="$repo_root/work/vendor/deno/test-${symlink_arm64_zip_sha[1,12]}-${symlink_amd64_zip_sha[1,12]}-${license_sha[1,12]}"
mkdir -p "$symlink_cache"
symlink_victim="$test_root/symlink-victim"
print -r -- 'do not overwrite' >"$symlink_victim"
/bin/ln -s "$symlink_victim" "$symlink_cache/deno_macos_arm64"
if env WEB_VIDEO_DENO_TESTING=1 WEB_VIDEO_DENO_TEST_SOURCE_DIR="$symlink_fixture" \
  WEB_VIDEO_DENO_TEST_ARM64_ZIP_SHA256="$symlink_arm64_zip_sha" \
  WEB_VIDEO_DENO_TEST_AMD64_ZIP_SHA256="$symlink_amd64_zip_sha" \
  WEB_VIDEO_DENO_TEST_ARM64_BINARY_SHA256="$symlink_arm64_binary_sha" \
  WEB_VIDEO_DENO_TEST_AMD64_BINARY_SHA256="$amd64_binary_sha" \
  WEB_VIDEO_DENO_TEST_LICENSE_SHA256="$license_sha" \
  /bin/zsh "$fetch_script" >"$test_root/symlink-cache.txt" 2>&1; then
  fail "fetcher 接受了符号链接缓存目标"
fi
[[ "$(<"$symlink_victim")" == 'do not overwrite' ]] || fail "fetcher 跟随缓存符号链接覆盖了目标"

if /usr/bin/find "$repo_root/work/vendor" -type f -name '*.part' -print -quit | rg -q .; then
  fail "Deno fetcher 遗留 .part 文件"
fi
if /usr/bin/find "$repo_root/work/vendor" -name '.deno-*' -print -quit | rg -q .; then
  fail "Deno fetcher 遗留随机临时文件"
fi

print -- "Deno 固定获取测试通过：版本、哈希、解压边界、并发和缓存不覆盖。"
