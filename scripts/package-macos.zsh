#!/bin/zsh
set -euo pipefail
unsetopt BG_NICE

script_dir="${0:A:h}"
repo_root="${script_dir:h}"
repo_real="${repo_root:A}"
package_name="WebVideoHarbor-macOS"
archive_name="$package_name.zip"
central_output_helper="/Users/frank/.codex/scripts/ensure-central-outputs.zsh"

fail() {
  print -u2 -- "$1"
  exit 1
}

for required_command in zip unzip bsdtar shasum find sort touch node file lipo mktemp rg; do
  command -v "$required_command" >/dev/null 2>&1 || fail "缺少打包命令：$required_command"
done

package_testing="${WEB_VIDEO_PACKAGE_TESTING:-}"
[[ -z "$package_testing" || "$package_testing" == "1" ]] || fail "测试模式开关无效"
if [[ "$package_testing" != "1" ]]; then
  package_test_inputs="${WEB_VIDEO_PACKAGE_TEST_SOURCE_DIR:-}${WEB_VIDEO_PACKAGE_TEST_BINARY_SHA256:-}${WEB_VIDEO_PACKAGE_TEST_LICENSE_SHA256:-}${WEB_VIDEO_PACKAGE_TEST_FORCE_REFRESH:-}${WEB_VIDEO_PACKAGE_TEST_OUTPUT_DIR:-}"
  [[ -z "$package_test_inputs" ]] || fail "生产模式不得注入测试数据"
fi

[[ -f "$repo_root/README.md" && ! -L "$repo_root/README.md" ]] || fail "缺少安全的 README.md"
[[ -f "$repo_root/docs/安装使用说明.md" && ! -L "$repo_root/docs/安装使用说明.md" ]] || \
  fail "缺少安全的 docs/安装使用说明.md"
[[ -f "$repo_root/THIRD_PARTY_NOTICES.md" && ! -L "$repo_root/THIRD_PARTY_NOTICES.md" ]] || \
  fail "缺少安全的 THIRD_PARTY_NOTICES.md"

package_work="$repo_root/work/package"
check_work="$repo_root/work/package-check"
for work_directory in "$package_work" "$check_work"; do
  [[ ! -L "$work_directory" ]] || fail "拒绝使用符号链接工作目录：$work_directory"
  mkdir -p "$work_directory"
  work_real="${work_directory:A}"
  [[ "$work_real" == "$repo_real"/work/* ]] || fail "工作目录越出项目 work/：$work_real"
done

if [[ "${WEB_VIDEO_PACKAGE_TESTING:-}" == "1" ]]; then
  [[ -n "${WEB_VIDEO_PACKAGE_TEST_OUTPUT_DIR:-}" ]] || fail "测试模式缺少输出目录"
  output_dir="$WEB_VIDEO_PACKAGE_TEST_OUTPUT_DIR"
  [[ ! -L "$output_dir" ]] || fail "测试输出目录不得是符号链接"
  mkdir -p "$output_dir"
  output_real="${output_dir:A}"
  [[ "$output_real" == "$repo_real"/work/* ]] || fail "测试输出目录必须位于项目 work/ 内"
else
  [[ -z "${WEB_VIDEO_PACKAGE_TEST_OUTPUT_DIR:-}" ]] || fail "非测试模式不得覆盖输出目录"
  if [[ "$repo_real" == */.worktrees/* ]]; then
    delivery_root="${repo_real%%/.worktrees/*}"
  else
    delivery_root="$repo_real"
  fi
  [[ -x "$central_output_helper" ]] || fail "缺少集中输出目录助手：$central_output_helper"
  "$central_output_helper" "$delivery_root"
  output_dir="$delivery_root/outputs"
  [[ -d "$output_dir" ]] || fail "集中输出目录不可用：$output_dir"
  output_real="${output_dir:A}"
fi

archive_path="$output_dir/$archive_name"
[[ ! -e "$archive_path" && ! -L "$archive_path" ]] || fail "输出 ZIP 已存在，拒绝覆盖：$archive_path"

"$repo_root/scripts/fetch-yt-dlp.zsh" >/dev/null || fail "无法获取并校验固定平台解析器"
source "$repo_root/third_party/yt-dlp.env"
yt_dlp_cache_key="$YTDLP_VERSION"
expected_parser_sha="$YTDLP_MACOS_SHA256"
expected_license_sha="$YTDLP_LICENSE_SHA256"
if [[ "${WEB_VIDEO_PACKAGE_TESTING:-}" == "1" ]]; then
  test_parser_sha="${WEB_VIDEO_PACKAGE_TEST_BINARY_SHA256:-}"
  test_license_sha="${WEB_VIDEO_PACKAGE_TEST_LICENSE_SHA256:-}"
  [[ "$test_parser_sha" =~ '^[0-9a-f]{64}$' ]] || fail "测试模式缺少解析器 SHA256"
  [[ "$test_license_sha" =~ '^[0-9a-f]{64}$' ]] || fail "测试模式缺少许可证 SHA256"
  yt_dlp_cache_key="test-${test_parser_sha[1,16]}-${test_license_sha[1,16]}"
  expected_parser_sha="$test_parser_sha"
  expected_license_sha="$test_license_sha"
fi
yt_dlp_cache="$repo_root/work/vendor/yt-dlp/$yt_dlp_cache_key"
[[ ! -e "$yt_dlp_cache/yt-dlp_macos.part" && ! -L "$yt_dlp_cache/yt-dlp_macos.part" ]] || \
  fail "缓存包含未完成的解析器 .part 文件"
source_parser="$yt_dlp_cache/yt-dlp_macos"
source_parser_license="$yt_dlp_cache/THIRD_PARTY_LICENSES.txt"
[[ -x "$source_parser" && ! -L "$source_parser" ]] || fail "固定平台解析器缓存无效"
[[ -f "$source_parser_license" && ! -L "$source_parser_license" ]] || fail "平台解析器许可证缓存无效"
parser_sha256="$(/usr/bin/shasum -a 256 "$source_parser" | /usr/bin/awk '{print $1}')"
license_sha256="$(/usr/bin/shasum -a 256 "$source_parser_license" | /usr/bin/awk '{print $1}')"
[[ "$parser_sha256" == "$expected_parser_sha" ]] || fail "固定平台解析器缓存 SHA256 异常"
[[ "$license_sha256" == "$expected_license_sha" ]] || fail "平台解析器许可证缓存 SHA256 异常"
[[ "$("$source_parser" --version)" == "$YTDLP_VERSION" ]] || fail "固定平台解析器版本异常"
/usr/bin/lipo "$source_parser" -verify_arch arm64 x86_64 || fail "固定平台解析器缺少 universal 架构"

"$repo_root/scripts/build-macos.zsh"
source_binary="$repo_root/work/dist/web-video-harbor-helper"
[[ -x "$source_binary" && ! -L "$source_binary" ]] || fail "构建没有生成安全的 universal 助手"
/usr/bin/lipo "$source_binary" -verify_arch arm64 x86_64 || fail "构建产物缺少 arm64 或 x86_64"

go_prefix="$(brew --prefix go 2>/dev/null)" || fail "无法定位实际构建所用的 Homebrew Go"
go_command="$go_prefix/bin/go"
[[ -x "$go_command" ]] || fail "实际构建所用的 Go 不可执行"
go_root="$($go_command env GOROOT)"
go_license="$go_root/LICENSE"
if [[ ! -f "$go_license" && -f "${go_root:h}/LICENSE" ]]; then
  go_license="${go_root:h}/LICENSE"
fi
[[ -f "$go_license" && ! -L "$go_license" ]] || fail "实际构建所用的 Go 缺少 LICENSE"

stage_run="$(/usr/bin/mktemp -d "$package_work/run.XXXXXX")"
stage_root="$stage_run/$package_name"
mkdir -p "$stage_root/docs" "$stage_root/extension" "$stage_root/helper" \
  "$stage_root/licenses" "$stage_root/scripts" "$stage_root/work/dist"

copy_regular_file() {
  local source_path="$1"
  local destination_path="$2"
  [[ -f "$source_path" && ! -L "$source_path" ]] || fail "拒绝复制非普通文件：$source_path"
  mkdir -p "${destination_path:h}"
  /bin/cp -p "$source_path" "$destination_path"
}

copy_regular_file "$repo_root/README.md" "$stage_root/README.md"
copy_regular_file "$repo_root/docs/安装使用说明.md" "$stage_root/docs/安装使用说明.md"
copy_regular_file "$repo_root/THIRD_PARTY_NOTICES.md" "$stage_root/THIRD_PARTY_NOTICES.md"
copy_regular_file "$go_license" "$stage_root/licenses/Go-LICENSE.txt"

if /usr/bin/find "$repo_root/extension" "$repo_root/helper/cmd" "$repo_root/helper/internal" -type l -print -quit | \
  rg -q .; then
  fail "源码目录包含符号链接"
fi

expected_extension_files=$'background.js\ncontent.js\nlib/helper-client.js\nlib/media.js\nlib/platform.js\nlib/popup-controller.js\nlib/popup-state.js\nmanifest.json\noptions.html\noptions.js\npopup.css\npopup.html\npopup.js'
actual_extension_files="$(
  cd "$repo_root/extension"
  /usr/bin/find . -type f ! -path './tests/*' -print | sed 's#^\./##' | /usr/bin/sort
)"
[[ "$actual_extension_files" == "$expected_extension_files" ]] || \
  fail "extension/ 存在非白名单文件或缺少生产文件：$actual_extension_files"
for relative_extension_path in ${(f)expected_extension_files}; do
  copy_regular_file "$repo_root/extension/$relative_extension_path" \
    "$stage_root/extension/$relative_extension_path"
done

copy_regular_file "$repo_root/helper/go.mod" "$stage_root/helper/go.mod"
copy_regular_file "$repo_root/helper/go.sum" "$stage_root/helper/go.sum"
while IFS= read -r -d '' source_path; do
  if rg -Pq '^//go:build[[:space:]].*(?<![![:alnum:]_])integration(?![[:alnum:]_])' "$source_path"; then
    continue
  fi
  relative_path="${source_path#$repo_root/}"
  copy_regular_file "$source_path" "$stage_root/$relative_path"
done < <(/usr/bin/find "$repo_root/helper/cmd" "$repo_root/helper/internal" -type f -name '*.go' \
  ! -name '*_test.go' ! -path '*/testdata/*' -print0)

for packaged_script in \
  bounded-log.zsh build-macos.zsh helper-common.zsh helper-status.zsh \
  start-helper.zsh stop-helper.zsh; do
  copy_regular_file "$repo_root/scripts/$packaged_script" "$stage_root/scripts/$packaged_script"
done
copy_regular_file "$source_binary" "$stage_root/work/dist/web-video-harbor-helper"
copy_regular_file "$source_parser" "$stage_root/work/dist/yt-dlp_macos"
copy_regular_file "$source_parser_license" "$stage_root/licenses/yt-dlp-THIRD_PARTY_LICENSES.txt"
[[ "$(/usr/bin/shasum -a 256 "$stage_root/work/dist/yt-dlp_macos" | /usr/bin/awk '{print $1}')" == \
  "$expected_parser_sha" ]] || fail "staging 平台解析器 SHA256 异常"
[[ "$(/usr/bin/shasum -a 256 "$stage_root/licenses/yt-dlp-THIRD_PARTY_LICENSES.txt" | /usr/bin/awk '{print $1}')" == \
  "$expected_license_sha" ]] || fail "staging 平台解析器许可证 SHA256 异常"

chmod 0755 "$stage_root/scripts/build-macos.zsh" "$stage_root/scripts/helper-status.zsh" \
  "$stage_root/scripts/start-helper.zsh" "$stage_root/scripts/stop-helper.zsh" \
  "$stage_root/work/dist/web-video-harbor-helper" "$stage_root/work/dist/yt-dlp_macos"
chmod 0644 "$stage_root/scripts/bounded-log.zsh" "$stage_root/scripts/helper-common.zsh"
chmod 0644 "$stage_root/licenses/yt-dlp-THIRD_PARTY_LICENSES.txt"
/usr/bin/find "$stage_root" -exec /usr/bin/touch -t 202001010000.00 {} +

validate_package_tree() {
  local tree_root="$1"
  local tree_label="$2"
  local top_entries
  local expected_entries
  local path_listing

  if /usr/bin/find "$tree_root" ! -type d ! -type f -print -quit | rg -q .; then
    fail "$tree_label 包含符号链接或特殊文件"
  fi

  top_entries="$(
    cd "$tree_root"
    /usr/bin/find . -mindepth 1 -maxdepth 1 -print | sed 's#^\./##' | /usr/bin/sort
  )"
  expected_entries=$'README.md\nTHIRD_PARTY_NOTICES.md\ndocs\nextension\nhelper\nlicenses\nscripts\nwork'
  [[ "$top_entries" == "$expected_entries" ]] || fail "$tree_label 顶层不符合白名单：$top_entries"

  path_listing="$(
    cd "$tree_root"
    /usr/bin/find . -mindepth 1 -print | sed 's#^\./##'
  )"
  if print -r -- "$path_listing" | rg -i \
    '(^|/)(tests?|testdata|outputs|\.git|__MACOSX|\.DS_Store|chrome-profile|downloads|build-cache|go-cache)(/|$)|(^|/)(config(\.local)?\.json|token)(/|$)|(^|/)[^/]*\.(log|pid|part|mp4|m3u8|ts)$' \
    >/dev/null; then
    fail "$tree_label 包含测试、缓存、凭证、日志或媒体文件"
  fi
  if rg -Pl '^//go:build[[:space:]].*(?<![![:alnum:]_])integration(?![[:alnum:]_])' \
    "$tree_root/helper" -g '*.go' | rg -q .; then
    fail "$tree_label 包含 integration build-tag 的测试专用 Go 源码"
  fi
}

validate_manifest_and_sources() {
  local package_root="$1"
  node -e '
    const fs = require("node:fs");
    const path = require("node:path");
    const manifestPath = process.argv[1];
    const extensionRoot = process.argv[2];
    const manifest = JSON.parse(fs.readFileSync(manifestPath, "utf8"));
    if (manifest.manifest_version !== 3) throw new Error("manifest_version 不是 3");
    const references = [
      manifest.background?.service_worker,
      manifest.action?.default_popup,
      manifest.options_page,
      manifest.options_ui?.page,
      ...(manifest.content_scripts ?? []).flatMap((entry) => [...(entry.js ?? []), ...(entry.css ?? [])]),
    ].filter(Boolean);
    for (const reference of references) {
      const resolved = path.resolve(extensionRoot, reference);
      if (!resolved.startsWith(extensionRoot + path.sep) || !fs.statSync(resolved).isFile()) {
        throw new Error(`manifest 本地引用无效：${reference}`);
      }
    }
  ' "$package_root/extension/manifest.json" "${package_root:A}/extension"

  while IFS= read -r -d '' js_path; do
    node --check "$js_path"
  done < <(/usr/bin/find "$package_root/extension" -type f -name '*.js' -print0)
  while IFS= read -r -d '' zsh_path; do
    /bin/zsh -o NO_BG_NICE -n "$zsh_path"
  done < <(/usr/bin/find "$package_root/scripts" -type f -name '*.zsh' -print0)

  rg -Fq '[安装使用说明](docs/安装使用说明.md)' "$package_root/README.md" || \
    fail "README 没有引用包内安装说明"
  [[ -f "$package_root/docs/安装使用说明.md" ]] || fail "README 引用的安装说明不存在"
  for documented_script in build-macos.zsh start-helper.zsh helper-status.zsh stop-helper.zsh; do
    rg -Fq "./scripts/$documented_script" "$package_root/docs/安装使用说明.md" || \
      fail "安装说明缺少脚本命令：$documented_script"
    [[ -f "$package_root/scripts/$documented_script" ]] || fail "安装说明引用的脚本不存在：$documented_script"
  done
}

create_checksum_manifest() {
  local tree_root="$1"
  local manifest_path="$2"
  (
    cd "$tree_root"
    /usr/bin/find . -type f -print0 | /usr/bin/sort -z | while IFS= read -r -d '' relative_path; do
      /usr/bin/shasum -a 256 "$relative_path"
    done
  ) >"$manifest_path"
}

validate_package_tree "$stage_root" "staging"
validate_manifest_and_sources "$stage_root"

temp_archive="$(/usr/bin/mktemp "$output_dir/.web-video-package.XXXXXX")"
cleanup_publish_temps() {
  local original_status=$?
  trap - EXIT INT TERM
  if [[ -n "${temp_archive:-}" && "${temp_archive:h:A}" == "$output_real" ]]; then
    rm -f -- "$temp_archive"
  fi
  return "$original_status"
}
trap cleanup_publish_temps EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
[[ "${temp_archive:h:A}" == "$output_real" ]] || fail "随机 ZIP 临时路径越界"
rm -f -- "$temp_archive"
(
  cd "$stage_run"
  /usr/bin/zip -X -q -r "$temp_archive" "$package_name"
)
[[ -f "$temp_archive" && ! -L "$temp_archive" ]] || fail "ZIP 临时文件生成失败"
/usr/bin/unzip -tq "$temp_archive" >/dev/null || fail "ZIP 完整性检查失败"

archive_listing="$(/usr/bin/bsdtar -tf "$temp_archive")"
[[ -n "$archive_listing" ]] || fail "ZIP 内容为空"
if print -r -- "$archive_listing" | rg '(^/|(^|/)\.\.(/|$))' >/dev/null; then
  fail "ZIP 含绝对路径或父目录穿越"
fi
top_levels="$(print -r -- "$archive_listing" | sed '/^$/d; s#/.*##' | /usr/bin/sort -u)"
[[ "$top_levels" == "$package_name" ]] || fail "ZIP 顶层目录不唯一：$top_levels"
if print -r -- "$archive_listing" | rg -i \
  '(^|/)(tests?|testdata|outputs|\.git|__MACOSX|\.DS_Store|chrome-profile|downloads|build-cache|go-cache)(/|$)|(^|/)(config(\.local)?\.json|token)(/|$)|(^|/)[^/]*\.(log|pid|part|mp4|m3u8|ts)$' \
  >/dev/null; then
  fail "ZIP 清单包含测试、缓存、凭证、日志或媒体文件"
fi

check_run="$(/usr/bin/mktemp -d "$check_work/run.XXXXXX")"
/usr/bin/bsdtar -xf "$temp_archive" -C "$check_run"
unpacked_root="$check_run/$package_name"
[[ -d "$unpacked_root" && ! -L "$unpacked_root" ]] || fail "解包顶层目录无效"
validate_package_tree "$unpacked_root" "解包内容"
validate_manifest_and_sources "$unpacked_root"

stage_checksums="$stage_run/staging.sha256"
unpacked_checksums="$check_run/unpacked.sha256"
create_checksum_manifest "$stage_root" "$stage_checksums"
create_checksum_manifest "$unpacked_root" "$unpacked_checksums"
/usr/bin/cmp -s "$stage_checksums" "$unpacked_checksums" || fail "staging 与解包文件校验清单不一致"

for executable_path in \
  scripts/build-macos.zsh scripts/helper-status.zsh scripts/start-helper.zsh \
  scripts/stop-helper.zsh work/dist/web-video-harbor-helper work/dist/yt-dlp_macos; do
  [[ -x "$unpacked_root/$executable_path" ]] || fail "ZIP 未保留可执行位：$executable_path"
done
[[ ! -x "$unpacked_root/scripts/helper-common.zsh" ]] || fail "共享脚本权限意外变为可执行"

unpacked_binary="$unpacked_root/work/dist/web-video-harbor-helper"
[[ "$($unpacked_binary --version)" == "web-video-harbor-helper 0.2.0" ]] || fail "解包助手版本输出异常"
/usr/bin/file "$unpacked_binary"
/usr/bin/lipo -info "$unpacked_binary"
/usr/bin/lipo "$unpacked_binary" -verify_arch arm64 x86_64 || fail "解包助手缺少 universal 架构"

unpacked_parser="$unpacked_root/work/dist/yt-dlp_macos"
[[ -x "$unpacked_parser" && ! -L "$unpacked_parser" ]] || fail "解包平台解析器无效"
[[ "$(/usr/bin/shasum -a 256 "$unpacked_parser" | /usr/bin/awk '{print $1}')" == \
  "$expected_parser_sha" ]] || fail "解包平台解析器 SHA256 异常"
[[ "$($unpacked_parser --version)" == "$YTDLP_VERSION" ]] || fail "解包平台解析器版本异常"
/usr/bin/lipo "$unpacked_parser" -verify_arch arm64 x86_64 || fail "解包平台解析器缺少 universal 架构"
[[ -f "$unpacked_root/licenses/yt-dlp-THIRD_PARTY_LICENSES.txt" ]] || fail "解包内容缺少 yt-dlp 许可证"
[[ "$(/usr/bin/shasum -a 256 "$unpacked_root/licenses/yt-dlp-THIRD_PARTY_LICENSES.txt" | /usr/bin/awk '{print $1}')" == \
  "$expected_license_sha" ]] || fail "解包平台解析器许可证 SHA256 异常"
rg -Fq 'licenses/yt-dlp-THIRD_PARTY_LICENSES.txt' "$unpacked_root/THIRD_PARTY_NOTICES.md" || \
  fail "第三方说明未引用包内 yt-dlp 许可证"

(
  cd "$unpacked_root"
  /bin/zsh ./scripts/build-macos.zsh
)
rebuilt_binary="$unpacked_root/work/dist/web-video-harbor-helper"
[[ "$($rebuilt_binary --version)" == "web-video-harbor-helper 0.2.0" ]] || fail "解包源码重建后的版本输出异常"
/usr/bin/file "$rebuilt_binary"
/usr/bin/lipo -info "$rebuilt_binary"
/usr/bin/lipo "$rebuilt_binary" -verify_arch arm64 x86_64 || fail "解包源码无法重建 universal 助手"

archive_digest="$(/usr/bin/shasum -a 256 "$temp_archive" | awk '{print $1}')"
[[ "$archive_digest" =~ '^[0-9a-f]{64}$' ]] || fail "无法计算 ZIP SHA256"
chmod 0644 "$temp_archive"

/bin/ln "$temp_archive" "$archive_path" || fail "发布 ZIP 失败，可能已有同名文件"
rm -f -- "$temp_archive"
temp_archive=""
trap - EXIT INT TERM

archive_size="$(/usr/bin/stat -f '%z' "$archive_path")"
print -- "打包与解包验证通过：$archive_path"
print -- "SHA256：$archive_digest"
print -- "大小：$archive_size 字节"
