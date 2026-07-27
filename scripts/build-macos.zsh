#!/bin/zsh
set -euo pipefail

script_dir="${0:A:h}"
repo_root="${script_dir:h}"
repo_real="${repo_root:A}"
helper_source="$repo_root/helper/cmd/web-video-harbor-helper"
dist_dir="$repo_root/work/dist"
build_cache="$repo_root/work/build-cache/go"
build_tmp="$repo_root/work/build-tmp"

for required_command in brew lipo file; do
  if ! command -v "$required_command" >/dev/null 2>&1; then
    print -u2 -- "缺少构建命令：$required_command"
    exit 1
  fi
done

go_prefix="$(brew --prefix go 2>/dev/null)" || {
  print -u2 -- "未找到 Homebrew Go，请先运行：brew install go"
  exit 1
}
go_command="$go_prefix/bin/go"
if [[ ! -x "$go_command" ]]; then
  print -u2 -- "Homebrew Go 不可执行：$go_command"
  exit 1
fi

for directory in "$dist_dir" "$build_cache" "$build_tmp"; do
  directory_real="${directory:A}"
  if [[ "$directory_real" != "$repo_real"/work/* ]]; then
    print -u2 -- "拒绝使用项目 work/ 以外的构建目录：$directory_real"
    exit 1
  fi
  if [[ -L "$directory" ]]; then
    print -u2 -- "拒绝使用符号链接构建目录：$directory"
    exit 1
  fi
  mkdir -p "$directory"
done

arm_binary="$dist_dir/web-video-harbor-helper-arm64"
intel_binary="$dist_dir/web-video-harbor-helper-amd64"
universal_binary="$dist_dir/web-video-harbor-helper"
universal_temp="$dist_dir/.web-video-harbor-helper-universal-$$"
if [[ "${universal_temp:A:h}" != "${dist_dir:A}" ]]; then
  print -u2 -- "临时构建路径无效"
  exit 1
fi
trap 'rm -f -- "$universal_temp"' EXIT

print -- "使用 Homebrew Go：$("$go_command" version)"
release_ldflags='-X main.version=0.2.0'
(
  cd "$repo_root/helper"
  env GOCACHE="$build_cache" GOTMPDIR="$build_tmp" CGO_ENABLED=0 \
    GOOS=darwin GOARCH=arm64 "$go_command" build -trimpath \
    -ldflags "$release_ldflags" \
    -o "$arm_binary" "$helper_source"
  env GOCACHE="$build_cache" GOTMPDIR="$build_tmp" CGO_ENABLED=0 \
    GOOS=darwin GOARCH=amd64 "$go_command" build -trimpath \
    -ldflags "$release_ldflags" \
    -o "$intel_binary" "$helper_source"
)

chmod 0755 "$arm_binary" "$intel_binary"
/usr/bin/file "$arm_binary" "$intel_binary"
/usr/bin/lipo -create "$arm_binary" "$intel_binary" -output "$universal_temp"
/usr/bin/lipo "$universal_temp" -verify_arch arm64 x86_64
chmod 0755 "$universal_temp"
mv -f -- "$universal_temp" "$universal_binary"

/usr/bin/file "$universal_binary"
/usr/bin/lipo -info "$universal_binary"
print -- "构建完成：$universal_binary"
