#!/bin/zsh
set -euo pipefail
unsetopt BG_NICE

script_dir="${0:A:h}"
repo_root="${script_dir:h:h}"
package_script="$repo_root/scripts/package-macos.zsh"
test_output_root="$repo_root/work/package-test-output"
mkdir -p "$test_output_root"
run_output_dir="$(/usr/bin/mktemp -d "$test_output_root/run.XXXXXX")"
archive_path="$run_output_dir/网页视频下载器-macOS.zip"
unpack_root="$run_output_dir/unpacked"

fail() {
  print -u2 -- "FAIL: $1"
  exit 1
}

[[ -x "$package_script" ]] || fail "缺少可执行打包脚本 scripts/package-macos.zsh"
/bin/zsh -o NO_BG_NICE -n "$package_script" || fail "打包脚本语法错误"
package_script_text="$(<"$package_script")"
if [[ "$package_script_text" != *"trap cleanup_publish_temps EXIT"*"trap 'exit 130' INT"*"trap 'exit 143' TERM"* ]]; then
  fail "打包发布临时文件没有使用 EXIT 清理并把 INT/TERM 转换为退出"
fi

env WEB_VIDEO_PACKAGE_TESTING=1 WEB_VIDEO_PACKAGE_TEST_OUTPUT_DIR="$run_output_dir" \
  /bin/zsh "$package_script"

[[ -f "$archive_path" ]] || fail "打包脚本没有生成测试 ZIP"
[[ ! -L "$archive_path" ]] || fail "测试 ZIP 不得是符号链接"

mkdir -p "$unpack_root"
/usr/bin/bsdtar -xf "$archive_path" -C "$unpack_root"
package_root="$unpack_root/网页视频下载器-macOS"

for required_path in \
  README.md \
  docs/安装使用说明.md \
  THIRD_PARTY_NOTICES.md \
  licenses/Go-LICENSE.txt \
  extension/manifest.json \
  scripts/build-macos.zsh \
  scripts/start-helper.zsh \
  helper/go.mod \
  helper/cmd/web-video-helper/main.go \
  helper/internal/safety/exact_host_default.go \
  work/dist/web-video-helper; do
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

[[ -x "$package_root/work/dist/web-video-helper" ]] || fail "预构建助手不可执行"
/usr/bin/lipo "$package_root/work/dist/web-video-helper" -verify_arch arm64 x86_64 || \
  fail "预构建助手不是 arm64+x86_64 universal"
[[ "$($package_root/work/dist/web-video-helper --version)" == "web-video-helper dev" ]] || \
  fail "预构建助手版本输出异常"

archive_listing="$(/usr/bin/bsdtar -tf "$archive_path")"
top_levels="$(print -r -- "$archive_listing" | sed '/^$/d; s#/.*##' | sort -u)"
[[ "$top_levels" == "网页视频下载器-macOS" ]] || fail "ZIP 顶层目录不唯一：$top_levels"
if print -r -- "$archive_listing" | rg -i \
  '(^|/)(tests?|testdata|outputs|\.git|__MACOSX|\.DS_Store|[^/]*\.(log|pid|part))(/|$)|(^|/)(config\.json|config\.local\.json|token)(/|$)|(^|/)(chrome-profile|downloads|build-cache|go-cache)(/|$)' \
  >/dev/null; then
  fail "ZIP 包含测试、缓存、凭证或运行时文件"
fi

unexpected_file="$repo_root/extension/.package-test-private.pem"
unexpected_output_dir="$(/usr/bin/mktemp -d "$test_output_root/unexpected.XXXXXX")"
trap 'rm -f -- "$unexpected_file"' EXIT INT TERM
: >"$unexpected_file"
if env WEB_VIDEO_PACKAGE_TESTING=1 WEB_VIDEO_PACKAGE_TEST_OUTPUT_DIR="$unexpected_output_dir" \
  /bin/zsh "$package_script" >"$unexpected_output_dir/rejected.txt" 2>&1; then
  fail "扩展目录中的非白名单敏感文件被静默打包"
fi
rm -f -- "$unexpected_file"
trap - EXIT INT TERM
rg -Fq '非白名单' "$unexpected_output_dir/rejected.txt" || fail "非白名单扩展文件的拒绝信息不明确"
[[ ! -e "$unexpected_output_dir/网页视频下载器-macOS.zip" ]] || fail "拒绝非白名单文件后仍发布了 ZIP"

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
repeat_digest="$(/usr/bin/shasum -a 256 "$repeat_output_dir/网页视频下载器-macOS.zip" | awk '{print $1}')"
[[ "$first_digest" == "$repeat_digest" ]] || \
  fail "相同源码重复打包的 SHA256 不一致：$first_digest != $repeat_digest"

print -- "macOS 打包行为测试通过：布局、universal 助手、禁项、不覆盖和可重复归档。"
