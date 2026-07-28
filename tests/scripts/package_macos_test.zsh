#!/bin/zsh
set -euo pipefail
unsetopt BG_NICE

script_dir="${0:A:h}"
repo_root="${script_dir:h:h}"
package_script="$repo_root/scripts/package-macos.zsh"
test_output_root="$repo_root/work/package-test-output"
mkdir -p "$test_output_root"
run_output_dir="$(/usr/bin/mktemp -d "$test_output_root/run.XXXXXX")"
archive_path="$run_output_dir/WebVideoHarbor-macOS-v1.0.0.zip"
unpack_root="$run_output_dir/unpacked"
fixture_root="$repo_root/work/package-yt-dlp-fixture"
deno_fixture_root="$repo_root/work/package-deno-fixture"

fail() {
  print -u2 -- "FAIL: $1"
  exit 1
}

[[ -x "$package_script" ]] || fail "缺少可执行打包脚本 scripts/package-macos.zsh"
/bin/zsh -o NO_BG_NICE -n "$package_script" || fail "打包脚本语法错误"
package_script_text="$(<"$package_script")"
[[ "$package_script_text" == *'archive_name="$package_name-v$release_version.zip"'* ]] || \
  fail "打包脚本没有从 VERSION 生成独立归档文件名"
[[ "$package_script_text" == *'version_file="$repo_root/VERSION"'* ]] || \
  fail "打包脚本没有使用根目录 VERSION"
version_fixture_root="$(/usr/bin/mktemp -d "$test_output_root/version.XXXXXX")"
mkdir -p "$version_fixture_root/scripts"
/bin/cp -p "$package_script" "$version_fixture_root/scripts/package-macos.zsh"
for invalid_version in '1.0.0 ' $'1.0.0\n2.0.0' 'v1.0.0'; do
  print -r -- "$invalid_version" >"$version_fixture_root/VERSION"
  if /bin/zsh "$version_fixture_root/scripts/package-macos.zsh" \
    >"$version_fixture_root/rejected.txt" 2>&1; then
    fail "打包脚本接受了无效 VERSION：$invalid_version"
  fi
  rg -Fq 'VERSION 无效' "$version_fixture_root/rejected.txt" || \
    fail "打包脚本没有明确拒绝无效 VERSION：$invalid_version"
done
if [[ "$package_script_text" != *"trap cleanup_publish_temps EXIT"*"trap 'exit 130' INT"*"trap 'exit 143' TERM"* ]]; then
  fail "打包发布临时文件没有使用 EXIT 清理并把 INT/TERM 转换为退出"
fi
[[ "$package_script_text" != *'fetch_output='* ]] || fail "打包脚本保留了未使用的 fetcher 输出"
[[ "$package_script_text" == *'parser_sha256='*'license_sha256='* ]] || \
  fail "打包脚本没有独立重验解析器与许可证 SHA256"
[[ "$package_script_text" == *'"$source_parser" --version'* ]] || \
  fail "打包脚本没有在复制前验证解析器版本"
[[ "$package_script_text" == *'lipo "$source_parser" -verify_arch arm64 x86_64'* ]] || \
  fail "打包脚本没有在复制前验证解析器 universal 架构"
[[ "$package_script_text" == *"缓存包含未完成的解析器 .part 文件"* ]] || \
  fail "打包脚本没有拒绝解析器 .part 文件"
[[ "$package_script_text" == *'scripts/fetch-deno.zsh'* ]] || \
  fail "打包脚本没有获取固定 Deno 运行环境"
[[ "$package_script_text" == *'deno_arm64_sha256='*'deno_amd64_sha256='*'deno_license_sha256='* ]] || \
  fail "打包脚本没有独立重验两种架构 Deno 与许可证 SHA256"
[[ "$package_script_text" == *"缓存包含未完成的 Deno .part 文件"* ]] || \
  fail "打包脚本没有拒绝 Deno .part 文件"

invalid_mode_output="$(/usr/bin/mktemp -d "$test_output_root/invalid-mode.XXXXXX")"
if env WEB_VIDEO_PACKAGE_TESTING=0 WEB_VIDEO_PACKAGE_TEST_OUTPUT_DIR="$invalid_mode_output" \
  /bin/zsh "$package_script" >"$invalid_mode_output/rejected.txt" 2>&1; then
  fail "打包脚本接受了非 1 的测试模式开关"
fi
rg -Fq '测试模式开关无效' "$invalid_mode_output/rejected.txt" || \
  fail "无效测试模式开关的拒绝信息不明确"

production_injection_output="$(/usr/bin/mktemp -d "$test_output_root/production-injection.XXXXXX")"
if env WEB_VIDEO_PACKAGE_TEST_SOURCE_DIR="$repo_root/work" \
  WEB_VIDEO_PACKAGE_TEST_BINARY_SHA256="$(printf '0%.0s' {1..64})" \
  WEB_VIDEO_PACKAGE_TEST_LICENSE_SHA256="$(printf '1%.0s' {1..64})" \
  WEB_VIDEO_PACKAGE_TEST_OUTPUT_DIR="$production_injection_output" \
  /bin/zsh "$package_script" >"$production_injection_output/rejected.txt" 2>&1; then
  fail "生产模式接受了测试 fixture 或哈希注入"
fi
rg -Fq '生产模式不得注入测试数据' "$production_injection_output/rejected.txt" || \
  fail "生产模式测试数据注入的拒绝信息不明确"

go_prefix="$(brew --prefix go 2>/dev/null)" || fail "无法定位 Homebrew Go"
go_command="$go_prefix/bin/go"
[[ -x "$go_command" ]] || fail "Go 编译器不可执行"
mkdir -p "$fixture_root/src" "$fixture_root/build"
print -r -- 'package main' >"$fixture_root/src/main.go"
print -r -- 'import ("fmt"; "os")' >>"$fixture_root/src/main.go"
print -r -- 'func main(){ if len(os.Args)==2 && os.Args[1]=="--version" { fmt.Println("2026.07.04"); return }; os.Exit(2) }' \
  >>"$fixture_root/src/main.go"
env CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 "$go_command" build -trimpath \
  -o "$fixture_root/build/yt-dlp-arm64" "$fixture_root/src/main.go"
env CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 "$go_command" build -trimpath \
  -o "$fixture_root/build/yt-dlp-amd64" "$fixture_root/src/main.go"
/usr/bin/lipo -create "$fixture_root/build/yt-dlp-arm64" "$fixture_root/build/yt-dlp-amd64" \
  -output "$fixture_root/yt-dlp_macos"
chmod 0755 "$fixture_root/yt-dlp_macos"
print -r -- 'fixture yt-dlp third-party license bundle' >"$fixture_root/THIRD_PARTY_LICENSES.txt"
export WEB_VIDEO_PACKAGE_TEST_SOURCE_DIR="$fixture_root"
export WEB_VIDEO_PACKAGE_TEST_BINARY_SHA256="$(/usr/bin/shasum -a 256 "$fixture_root/yt-dlp_macos" | awk '{print $1}')"
export WEB_VIDEO_PACKAGE_TEST_LICENSE_SHA256="$(/usr/bin/shasum -a 256 "$fixture_root/THIRD_PARTY_LICENSES.txt" | awk '{print $1}')"

mkdir -p "$deno_fixture_root/src" "$deno_fixture_root/build" \
  "$deno_fixture_root/arm64" "$deno_fixture_root/amd64"
print -r -- 'package main' >"$deno_fixture_root/src/main.go"
print -r -- 'import ("fmt"; "os")' >>"$deno_fixture_root/src/main.go"
print -r -- 'func main(){ if len(os.Args)==2 && os.Args[1]=="--version" { fmt.Println("deno 2.8.1"); return }; os.Exit(2) }' \
  >>"$deno_fixture_root/src/main.go"
env CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 "$go_command" build -trimpath \
  -o "$deno_fixture_root/arm64/deno" "$deno_fixture_root/src/main.go"
env CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 "$go_command" build -trimpath \
  -o "$deno_fixture_root/amd64/deno" "$deno_fixture_root/src/main.go"
/bin/rm -f -- "$deno_fixture_root/deno-aarch64-apple-darwin.zip" \
  "$deno_fixture_root/deno-x86_64-apple-darwin.zip"
/usr/bin/zip -q -j "$deno_fixture_root/deno-aarch64-apple-darwin.zip" "$deno_fixture_root/arm64/deno"
/usr/bin/zip -q -j "$deno_fixture_root/deno-x86_64-apple-darwin.zip" "$deno_fixture_root/amd64/deno"
print -r -- 'fixture Deno license' >"$deno_fixture_root/LICENSE.md"
export WEB_VIDEO_DENO_TESTING=1
export WEB_VIDEO_DENO_TEST_SOURCE_DIR="$deno_fixture_root"
export WEB_VIDEO_DENO_TEST_ARM64_ZIP_SHA256="$(/usr/bin/shasum -a 256 "$deno_fixture_root/deno-aarch64-apple-darwin.zip" | awk '{print $1}')"
export WEB_VIDEO_DENO_TEST_AMD64_ZIP_SHA256="$(/usr/bin/shasum -a 256 "$deno_fixture_root/deno-x86_64-apple-darwin.zip" | awk '{print $1}')"
export WEB_VIDEO_DENO_TEST_ARM64_BINARY_SHA256="$(/usr/bin/shasum -a 256 "$deno_fixture_root/arm64/deno" | awk '{print $1}')"
export WEB_VIDEO_DENO_TEST_AMD64_BINARY_SHA256="$(/usr/bin/shasum -a 256 "$deno_fixture_root/amd64/deno" | awk '{print $1}')"
export WEB_VIDEO_DENO_TEST_LICENSE_SHA256="$(/usr/bin/shasum -a 256 "$deno_fixture_root/LICENSE.md" | awk '{print $1}')"

env WEB_VIDEO_PACKAGE_TESTING=1 /bin/zsh "$repo_root/scripts/fetch-yt-dlp.zsh" >/dev/null
env WEB_VIDEO_DENO_TESTING=1 /bin/zsh "$repo_root/scripts/fetch-deno.zsh" >/dev/null
fixture_cache="$repo_root/work/vendor/yt-dlp/test-${WEB_VIDEO_PACKAGE_TEST_BINARY_SHA256[1,16]}-${WEB_VIDEO_PACKAGE_TEST_LICENSE_SHA256[1,16]}"
parser_part="$fixture_cache/yt-dlp_macos.part"
trap '/bin/rm -f -- "$parser_part"' EXIT INT TERM
: >"$parser_part"
part_output="$(/usr/bin/mktemp -d "$test_output_root/parser-part.XXXXXX")"
if env WEB_VIDEO_PACKAGE_TESTING=1 WEB_VIDEO_PACKAGE_TEST_OUTPUT_DIR="$part_output" \
  /bin/zsh "$package_script" >"$part_output/rejected.txt" 2>&1; then
  fail "未完成的解析器 .part 文件未被拒绝"
fi
/bin/rm -f -- "$parser_part"
trap - EXIT INT TERM
[[ ! -e "$part_output/WebVideoHarbor-macOS-v1.0.0.zip" ]] || fail "存在解析器 .part 文件时仍发布了 ZIP"
rg -Fq '缓存包含未完成的解析器 .part 文件' "$part_output/rejected.txt" || \
  fail "解析器 .part 文件拒绝信息不明确"

deno_cache_key="test-${WEB_VIDEO_DENO_TEST_ARM64_ZIP_SHA256[1,12]}-${WEB_VIDEO_DENO_TEST_AMD64_ZIP_SHA256[1,12]}-${WEB_VIDEO_DENO_TEST_LICENSE_SHA256[1,12]}"
deno_fixture_cache="$repo_root/work/vendor/deno/$deno_cache_key"
deno_part="$deno_fixture_cache/deno_macos_arm64.part"
trap '/bin/rm -f -- "$deno_part"' EXIT INT TERM
: >"$deno_part"
deno_part_output="$(/usr/bin/mktemp -d "$test_output_root/deno-part.XXXXXX")"
if env WEB_VIDEO_PACKAGE_TESTING=1 WEB_VIDEO_PACKAGE_TEST_OUTPUT_DIR="$deno_part_output" \
  /bin/zsh "$package_script" >"$deno_part_output/rejected.txt" 2>&1; then
  fail "未完成的 Deno .part 文件未被拒绝"
fi
/bin/rm -f -- "$deno_part"
trap - EXIT INT TERM
[[ ! -e "$deno_part_output/WebVideoHarbor-macOS-v1.0.0.zip" ]] || fail "存在 Deno .part 文件时仍发布了 ZIP"
rg -Fq '缓存包含未完成的 Deno .part 文件' "$deno_part_output/rejected.txt" || \
  fail "Deno .part 文件拒绝信息不明确"

bad_parser_output="$(/usr/bin/mktemp -d "$test_output_root/bad-parser.XXXXXX")"
if env WEB_VIDEO_PACKAGE_TESTING=1 WEB_VIDEO_PACKAGE_TEST_OUTPUT_DIR="$bad_parser_output" \
  WEB_VIDEO_PACKAGE_TEST_BINARY_SHA256="$(printf 'f%.0s' {1..64})" \
  /bin/zsh "$package_script" >"$bad_parser_output/rejected.txt" 2>&1; then
  fail "校验和错误的解析器被打包"
fi
[[ ! -e "$bad_parser_output/WebVideoHarbor-macOS-v1.0.0.zip" ]] || fail "错误解析器仍发布了 ZIP"

bad_license_output="$(/usr/bin/mktemp -d "$test_output_root/bad-license.XXXXXX")"
if env WEB_VIDEO_PACKAGE_TESTING=1 WEB_VIDEO_PACKAGE_TEST_OUTPUT_DIR="$bad_license_output" \
  WEB_VIDEO_PACKAGE_TEST_LICENSE_SHA256="$(printf 'e%.0s' {1..64})" \
  /bin/zsh "$package_script" >"$bad_license_output/rejected.txt" 2>&1; then
  fail "校验和错误的许可证被打包"
fi
[[ ! -e "$bad_license_output/WebVideoHarbor-macOS-v1.0.0.zip" ]] || fail "错误许可证仍发布了 ZIP"

invalid_parser_fixture="$repo_root/work/package-invalid-parser-fixture"
mkdir -p "$invalid_parser_fixture"
print -r -- '#!/bin/zsh' >"$invalid_parser_fixture/yt-dlp_macos"
print -r -- '[[ "${1:-}" == "--version" ]] || exit 2' >>"$invalid_parser_fixture/yt-dlp_macos"
print -r -- 'print -- 1900.01.01' >>"$invalid_parser_fixture/yt-dlp_macos"
chmod 0755 "$invalid_parser_fixture/yt-dlp_macos"
/bin/cp -p "$fixture_root/THIRD_PARTY_LICENSES.txt" "$invalid_parser_fixture/THIRD_PARTY_LICENSES.txt"
invalid_parser_sha="$(/usr/bin/shasum -a 256 "$invalid_parser_fixture/yt-dlp_macos" | awk '{print $1}')"
invalid_parser_license_sha="$(/usr/bin/shasum -a 256 "$invalid_parser_fixture/THIRD_PARTY_LICENSES.txt" | awk '{print $1}')"
invalid_parser_output="$(/usr/bin/mktemp -d "$test_output_root/invalid-parser.XXXXXX")"
if env WEB_VIDEO_PACKAGE_TESTING=1 WEB_VIDEO_PACKAGE_TEST_SOURCE_DIR="$invalid_parser_fixture" \
  WEB_VIDEO_PACKAGE_TEST_BINARY_SHA256="$invalid_parser_sha" \
  WEB_VIDEO_PACKAGE_TEST_LICENSE_SHA256="$invalid_parser_license_sha" \
  WEB_VIDEO_PACKAGE_TEST_OUTPUT_DIR="$invalid_parser_output" \
  /bin/zsh "$package_script" >"$invalid_parser_output/rejected.txt" 2>&1; then
  fail "版本错误的解析器被打包"
fi
[[ ! -e "$invalid_parser_output/WebVideoHarbor-macOS-v1.0.0.zip" ]] || fail "版本错误的解析器仍发布了 ZIP"
rg -Fq '解析器版本异常' "$invalid_parser_output/rejected.txt" || fail "解析器版本错误信息不明确"

thin_parser_fixture="$repo_root/work/package-thin-parser-fixture"
mkdir -p "$thin_parser_fixture"
print -r -- '#!/bin/zsh' >"$thin_parser_fixture/yt-dlp_macos"
print -r -- '[[ "${1:-}" == "--version" ]] || exit 2' >>"$thin_parser_fixture/yt-dlp_macos"
print -r -- 'print -- 2026.07.04' >>"$thin_parser_fixture/yt-dlp_macos"
chmod 0755 "$thin_parser_fixture/yt-dlp_macos"
/bin/cp -p "$fixture_root/THIRD_PARTY_LICENSES.txt" "$thin_parser_fixture/THIRD_PARTY_LICENSES.txt"
thin_parser_sha="$(/usr/bin/shasum -a 256 "$thin_parser_fixture/yt-dlp_macos" | awk '{print $1}')"
thin_parser_license_sha="$(/usr/bin/shasum -a 256 "$thin_parser_fixture/THIRD_PARTY_LICENSES.txt" | awk '{print $1}')"
thin_parser_output="$(/usr/bin/mktemp -d "$test_output_root/thin-parser.XXXXXX")"
if env WEB_VIDEO_PACKAGE_TESTING=1 WEB_VIDEO_PACKAGE_TEST_SOURCE_DIR="$thin_parser_fixture" \
  WEB_VIDEO_PACKAGE_TEST_BINARY_SHA256="$thin_parser_sha" \
  WEB_VIDEO_PACKAGE_TEST_LICENSE_SHA256="$thin_parser_license_sha" \
  WEB_VIDEO_PACKAGE_TEST_OUTPUT_DIR="$thin_parser_output" \
  /bin/zsh "$package_script" >"$thin_parser_output/rejected.txt" 2>&1; then
  fail "非 universal 解析器被打包"
fi
[[ ! -e "$thin_parser_output/WebVideoHarbor-macOS-v1.0.0.zip" ]] || fail "非 universal 解析器仍发布了 ZIP"
rg -Fq '解析器缺少 universal 架构' "$thin_parser_output/rejected.txt" || \
  fail "非 universal 解析器错误信息不明确"

package_runs_before="$(/usr/bin/find "$repo_root/work/package" -mindepth 1 -maxdepth 1 -type d -name 'run.*' -print | /usr/bin/sort)"
check_runs_before="$(/usr/bin/find "$repo_root/work/package-check" -mindepth 1 -maxdepth 1 -type d -name 'run.*' -print | /usr/bin/sort)"
env WEB_VIDEO_PACKAGE_TESTING=1 WEB_VIDEO_PACKAGE_TEST_OUTPUT_DIR="$run_output_dir" \
  /bin/zsh "$package_script"
package_runs_after="$(/usr/bin/find "$repo_root/work/package" -mindepth 1 -maxdepth 1 -type d -name 'run.*' -print | /usr/bin/sort)"
check_runs_after="$(/usr/bin/find "$repo_root/work/package-check" -mindepth 1 -maxdepth 1 -type d -name 'run.*' -print | /usr/bin/sort)"
[[ "$package_runs_after" == "$package_runs_before" ]] || fail "打包脚本遗留了本次 staging 目录"
[[ "$check_runs_after" == "$check_runs_before" ]] || fail "打包脚本遗留了本次解包验证目录"

[[ -f "$archive_path" ]] || fail "打包脚本没有生成测试 ZIP"
[[ ! -L "$archive_path" ]] || fail "测试 ZIP 不得是符号链接"

mkdir -p "$unpack_root"
/usr/bin/bsdtar -xf "$archive_path" -C "$unpack_root"
package_root="$unpack_root/WebVideoHarbor-macOS"

for required_path in \
  VERSION \
  LICENSE \
  PRIVACY.md \
  README.md \
  docs/安装使用说明.md \
  docs/使用边界.md \
  THIRD_PARTY_NOTICES.md \
  licenses/Go-LICENSE.txt \
  extension/manifest.json \
  extension/lib/platform-settings.js \
  scripts/build-macos.zsh \
  scripts/start-helper.zsh \
  helper/go.mod \
  helper/go.sum \
  helper/cmd/web-video-harbor-helper/main.go \
  helper/internal/safety/exact_host_default.go \
  work/dist/yt-dlp_macos \
  work/dist/deno_macos_arm64 \
  work/dist/deno_macos_x86_64 \
  licenses/Deno-LICENSE.md \
  licenses/yt-dlp-THIRD_PARTY_LICENSES.txt \
  work/dist/web-video-harbor-helper; do
  [[ -e "$package_root/$required_path" ]] || fail "ZIP 缺少：$required_path"
done

go_license_path="$package_root/licenses/Go-LICENSE.txt"
for license_phrase in \
  'Copyright 2009 The Go Authors.' \
  'Redistribution and use in source and binary forms' \
  'THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS'; do
  rg -Fq "$license_phrase" "$go_license_path" || fail "包内 Go LICENSE 不完整：$license_phrase"
done
rg -Fq 'licenses/Go-LICENSE.txt' "$package_root/THIRD_PARTY_NOTICES.md" || \
  fail "第三方说明没有引用包内 Go LICENSE"
if rg -l '^//go:build integration$' "$package_root/helper" -g '*.go' | rg -q .; then
  fail "ZIP 包含 integration build-tag 的测试专用 Go 源码"
fi

[[ -x "$package_root/work/dist/web-video-harbor-helper" ]] || fail "预构建助手不可执行"
/usr/bin/lipo "$package_root/work/dist/web-video-harbor-helper" -verify_arch arm64 x86_64 || \
  fail "预构建助手不是 arm64+x86_64 universal"
[[ "$($package_root/work/dist/web-video-harbor-helper --version)" == "web-video-harbor-helper 1.0.0" ]] || \
  fail "预构建助手版本输出异常"
parser_path="$package_root/work/dist/yt-dlp_macos"
[[ -x "$parser_path" && ! -L "$parser_path" ]] || fail "包内平台解析器无效"
[[ "$($parser_path --version)" == "2026.07.04" ]] || fail "包内平台解析器版本错误"
/usr/bin/lipo "$parser_path" -verify_arch arm64 x86_64 || fail "包内平台解析器不是 universal"
[[ "$(/usr/bin/shasum -a 256 "$parser_path" | awk '{print $1}')" == \
  "$WEB_VIDEO_PACKAGE_TEST_BINARY_SHA256" ]] || fail "包内平台解析器与已验证 fixture 不一致"
packaged_license="$package_root/licenses/yt-dlp-THIRD_PARTY_LICENSES.txt"
[[ "$(/usr/bin/shasum -a 256 "$packaged_license" | awk '{print $1}')" == \
  "$WEB_VIDEO_PACKAGE_TEST_LICENSE_SHA256" ]] || fail "包内 yt-dlp 许可证内容被篡改"
rg -Fq 'yt-dlp' "$package_root/THIRD_PARTY_NOTICES.md" || fail "第三方说明缺少 yt-dlp"
rg -Fq '2026.07.04' "$package_root/THIRD_PARTY_NOTICES.md" || fail "第三方说明缺少 yt-dlp 版本"
rg -Fq 'https://github.com/yt-dlp/yt-dlp/tree/2026.07.04' "$package_root/THIRD_PARTY_NOTICES.md" || \
  fail "第三方说明缺少 yt-dlp 固定版本上游 URL"
rg -Fq 'licenses/yt-dlp-THIRD_PARTY_LICENSES.txt' "$package_root/THIRD_PARTY_NOTICES.md" || \
  fail "第三方说明没有引用包内 yt-dlp 许可证"

deno_arm64_path="$package_root/work/dist/deno_macos_arm64"
deno_amd64_path="$package_root/work/dist/deno_macos_x86_64"
[[ -x "$deno_arm64_path" && ! -L "$deno_arm64_path" ]] || fail "包内 arm64 Deno 无效"
[[ -x "$deno_amd64_path" && ! -L "$deno_amd64_path" ]] || fail "包内 x86_64 Deno 无效"
/usr/bin/lipo "$deno_arm64_path" -verify_arch arm64 || fail "包内 arm64 Deno 架构错误"
if /usr/bin/lipo "$deno_arm64_path" -verify_arch x86_64 >/dev/null 2>&1; then
  fail "包内 arm64 Deno 意外包含 x86_64"
fi
/usr/bin/lipo "$deno_amd64_path" -verify_arch x86_64 || fail "包内 x86_64 Deno 架构错误"
if /usr/bin/lipo "$deno_amd64_path" -verify_arch arm64 >/dev/null 2>&1; then
  fail "包内 x86_64 Deno 意外包含 arm64"
fi
[[ "$(/usr/bin/shasum -a 256 "$deno_arm64_path" | awk '{print $1}')" == \
  "$WEB_VIDEO_DENO_TEST_ARM64_BINARY_SHA256" ]] || fail "包内 arm64 Deno 与已验证 fixture 不一致"
[[ "$(/usr/bin/shasum -a 256 "$deno_amd64_path" | awk '{print $1}')" == \
  "$WEB_VIDEO_DENO_TEST_AMD64_BINARY_SHA256" ]] || fail "包内 x86_64 Deno 与已验证 fixture 不一致"
[[ "$(/usr/bin/shasum -a 256 "$package_root/licenses/Deno-LICENSE.md" | awk '{print $1}')" == \
  "$WEB_VIDEO_DENO_TEST_LICENSE_SHA256" ]] || fail "包内 Deno 许可证内容被篡改"
rg -Fq 'Deno' "$package_root/THIRD_PARTY_NOTICES.md" || fail "第三方说明缺少 Deno"
rg -Fq '2.8.1' "$package_root/THIRD_PARTY_NOTICES.md" || fail "第三方说明缺少 Deno 版本"
rg -Fq 'licenses/Deno-LICENSE.md' "$package_root/THIRD_PARTY_NOTICES.md" || \
  fail "第三方说明没有引用包内 Deno 许可证"

archive_listing="$(/usr/bin/bsdtar -tf "$archive_path")"
top_levels="$(print -r -- "$archive_listing" | sed '/^$/d; s#/.*##' | sort -u)"
[[ "$top_levels" == "WebVideoHarbor-macOS" ]] || fail "ZIP 顶层目录不唯一：$top_levels"
if print -r -- "$archive_listing" | rg -i \
  '(^|/)(tests?|testdata|outputs|\.git|__MACOSX|\.DS_Store|[^/]*\.(log|pid|part))(/|$)|(^|/)(config\.json|config\.local\.json|token)(/|$)|(^|/)(chrome-profile|downloads|build-cache|go-cache)(/|$)' \
  >/dev/null; then
  fail "ZIP 包含测试、缓存、凭证或运行时文件"
fi

unexpected_files=(
  "$repo_root/extension/.package-parser.part"
  "$repo_root/extension/.package-debug.log"
  "$repo_root/extension/token"
  "$repo_root/extension/.package-unexpected.bin"
)
unexpected_output_dir="$(/usr/bin/mktemp -d "$test_output_root/unexpected.XXXXXX")"
cleanup_unexpected_files() {
  rm -f -- $unexpected_files
}
trap cleanup_unexpected_files EXIT INT TERM
for unexpected_file in $unexpected_files; do
  : >"$unexpected_file"
done
if env WEB_VIDEO_PACKAGE_TESTING=1 WEB_VIDEO_PACKAGE_TEST_OUTPUT_DIR="$unexpected_output_dir" \
  /bin/zsh "$package_script" >"$unexpected_output_dir/rejected.txt" 2>&1; then
  fail "扩展目录中的 .part、日志、凭证或意外二进制被静默打包"
fi
cleanup_unexpected_files
trap - EXIT INT TERM
rg -Fq '非白名单' "$unexpected_output_dir/rejected.txt" || fail "非白名单扩展文件的拒绝信息不明确"
[[ ! -e "$unexpected_output_dir/WebVideoHarbor-macOS-v1.0.0.zip" ]] || fail "拒绝非白名单文件后仍发布了 ZIP"

[[ ! -e "$archive_path.sha256" ]] || fail "打包脚本不应发布可选摘要旁车文件"
archive_digest_before_no_clobber="$(/usr/bin/shasum -a 256 "$archive_path" | awk '{print $1}')"

if env WEB_VIDEO_PACKAGE_TESTING=1 WEB_VIDEO_PACKAGE_TEST_OUTPUT_DIR="$run_output_dir" \
  /bin/zsh "$package_script" >"$run_output_dir/no-clobber.txt" 2>&1; then
  fail "打包脚本覆盖或接受了已有 ZIP"
fi
rg -Fq '已存在' "$run_output_dir/no-clobber.txt" || fail "已有 ZIP 的拒绝信息不明确"
archive_digest_after_no_clobber="$(/usr/bin/shasum -a 256 "$archive_path" | awk '{print $1}')"
[[ "$archive_digest_after_no_clobber" == "$archive_digest_before_no_clobber" ]] || \
  fail "no-clobber 失败路径修改了已经发布的 ZIP"

repeat_output_dir="$(/usr/bin/mktemp -d "$test_output_root/repeat.XXXXXX")"
env WEB_VIDEO_PACKAGE_TESTING=1 WEB_VIDEO_PACKAGE_TEST_OUTPUT_DIR="$repeat_output_dir" \
  /bin/zsh "$package_script" >/dev/null
first_digest="$(/usr/bin/shasum -a 256 "$archive_path" | awk '{print $1}')"
repeat_digest="$(/usr/bin/shasum -a 256 "$repeat_output_dir/WebVideoHarbor-macOS-v1.0.0.zip" | awk '{print $1}')"
[[ "$first_digest" == "$repeat_digest" ]] || \
  fail "相同源码重复打包的 SHA256 不一致：$first_digest != $repeat_digest"

print -- "macOS 打包行为测试通过：布局、universal 助手、禁项、不覆盖和可重复归档。"
