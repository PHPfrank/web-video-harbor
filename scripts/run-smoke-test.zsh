#!/bin/zsh
set -euo pipefail

script_dir="${0:A:h}"
repo_root="${script_dir:h}"
work_root="$repo_root/work"
fixture_root="$work_root/fixtures-generated"
download_dir="$work_root/smoke-downloads"
browser_root="$work_root/smoke-browser"
go_cache="$work_root/go-cache"
go_tmp="$work_root/go-tmp"
results_path="$work_root/smoke-results.json"
browser_results_path="$work_root/smoke-browser-results.json"
chrome_path='/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'

reject_symlink_ancestry() {
  local candidate="$1"
  while [[ "$candidate" != "/" ]]; do
    if [[ -L "$candidate" ]]; then
      print -u2 -- "拒绝使用包含符号链接的工作路径：$candidate"
      exit 1
    fi
    candidate="${candidate:h}"
  done
}

repo_real="${repo_root:A}"
reject_symlink_ancestry "$work_root"

for command_name in go ffmpeg ffprobe node cmp; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    print -u2 -- "缺少必需命令：$command_name"
    exit 1
  fi
done
if [[ ! -x "$chrome_path" ]]; then
  print -u2 -- "未找到可执行的 Google Chrome：$chrome_path"
  exit 1
fi
print -- "浏览器自动化：当前 @playwright/mcp 未暴露 playwright-cli，使用隔离 Chrome CDP。"

for directory in "$fixture_root" "$fixture_root/hls/720" "$fixture_root/hls/1080" \
  "$download_dir" "$browser_root" "$go_cache" "$go_tmp"; do
  reject_symlink_ancestry "$directory"
  mkdir -p "$directory"
  if [[ -L "$directory" || ! -d "$directory" ]]; then
    print -u2 -- "拒绝使用符号链接或非目录路径：$directory"
    exit 1
  fi
  directory_real="${directory:A}"
  if [[ "$directory_real" != "$repo_real"/* ]]; then
    print -u2 -- "拒绝使用仓库以外的生成目录：$directory_real"
    exit 1
  fi
done

rm -f -- "$fixture_root/direct.mp4" \
  "$fixture_root/hls/720/generated.m3u8" \
  "$fixture_root/hls/1080/generated.m3u8" \
  "$results_path" "$browser_results_path"
find "$fixture_root/hls/720" -mindepth 1 -maxdepth 1 -type f -name 'segment*.ts' -delete
find "$fixture_root/hls/1080" -mindepth 1 -maxdepth 1 -type f -name 'segment*.ts' -delete
find "$download_dir" -mindepth 1 -maxdepth 1 -type f -name '集成测试-*.mp4' -delete
find "$download_dir" -mindepth 1 -maxdepth 1 -type f -name '网页视频下载器集成测试*.mp4' -delete

ffmpeg -hide_banner -loglevel error -nostdin -y \
  -f lavfi -i 'color=c=0x2563eb:s=640x360:r=24' \
  -f lavfi -i 'sine=frequency=440:sample_rate=48000' \
  -t 2 -shortest -c:v libx264 -preset ultrafast -pix_fmt yuv420p \
  -g 24 -keyint_min 24 -sc_threshold 0 -c:a aac -b:a 64k \
  "$fixture_root/direct.mp4"

ffmpeg -hide_banner -loglevel error -nostdin -y \
  -f lavfi -i 'color=c=0x16a34a:s=1280x720:r=24' \
  -f lavfi -i 'sine=frequency=660:sample_rate=48000' \
  -t 2 -shortest -c:v libx264 -preset ultrafast -pix_fmt yuv420p \
  -g 24 -keyint_min 24 -sc_threshold 0 -c:a aac -b:a 64k \
  -f hls -hls_time 1 -hls_list_size 0 -hls_playlist_type vod \
  -hls_segment_filename "$fixture_root/hls/720/segment%03d.ts" \
  "$fixture_root/hls/720/generated.m3u8"

ffmpeg -hide_banner -loglevel error -nostdin -y \
  -f lavfi -i 'color=c=0x9333ea:s=1920x1080:r=24' \
  -f lavfi -i 'sine=frequency=880:sample_rate=48000' \
  -t 2 -shortest -c:v libx264 -preset ultrafast -pix_fmt yuv420p \
  -g 24 -keyint_min 24 -sc_threshold 0 -c:a aac -b:a 64k \
  -f hls -hls_time 1 -hls_list_size 0 -hls_playlist_type vod \
  -hls_segment_filename "$fixture_root/hls/1080/segment%03d.ts" \
  "$fixture_root/hls/1080/generated.m3u8"

for quality in 720 1080; do
  for segment in segment000.ts segment001.ts; do
    if [[ ! -s "$fixture_root/hls/$quality/$segment" ]]; then
      print -u2 -- "HLS fixture 生成失败：$quality/$segment"
      exit 1
    fi
  done
  if ! cmp -s "$fixture_root/hls/$quality/generated.m3u8" "$repo_root/tests/fixtures/site/$quality/index.m3u8"; then
    print -u2 -- "HLS fixture 清单与受控测试清单不一致：$quality"
    exit 1
  fi
done

(
  cd "$repo_root/helper"
  env GOCACHE="$go_cache" GOTMPDIR="$go_tmp" go test -race ./...
)

(
  cd "$repo_root/tests/integration"
  env GOCACHE="$go_cache" GOTMPDIR="$go_tmp" \
    SMOKE_REPO_ROOT="$repo_root" \
    SMOKE_FIXTURE_ROOT="$fixture_root" \
    SMOKE_DOWNLOAD_DIR="$download_dir" \
    SMOKE_RESULTS_PATH="$results_path" \
    SMOKE_FFMPEG_PATH="$(command -v ffmpeg)" \
    SMOKE_CHROME_PATH="$chrome_path" \
    SMOKE_BROWSER_ROOT="$browser_root" \
    SMOKE_BROWSER_RESULTS_PATH="$browser_results_path" \
    go test -race -tags=integration -count=1 -v ./...
)

(
  cd "$repo_root"
  node --test extension/tests/*.test.js
  for javascript_file in extension/background.js extension/content.js extension/popup.js \
    extension/options.js extension/lib/*.js; do
    node --check "$javascript_file"
  done
  zsh -n scripts/run-smoke-test.zsh
  git diff --check
)

for result_key in direct single_hls master_1080; do
  media_path="$(node -p "JSON.parse(require('fs').readFileSync(process.argv[1], 'utf8'))[process.argv[2]]" "$results_path" "$result_key")"
  probe_json="$(ffprobe -v error -show_entries format=duration -show_entries stream=codec_type,codec_name \
    -of json "$media_path")"
  node -e '
    const probe = JSON.parse(process.argv[1]);
    const duration = Number(probe.format && probe.format.duration);
    const codecs = new Map((probe.streams || []).map((stream) => [stream.codec_type, stream.codec_name]));
    if (!(duration >= 1.5 && duration <= 3.0)) throw new Error(`unexpected duration ${duration}`);
    if (codecs.get("video") !== "h264") throw new Error(`unexpected video codec ${codecs.get("video")}`);
    if (codecs.get("audio") !== "aac") throw new Error(`unexpected audio codec ${codecs.get("audio")}`);
  ' "$probe_json"
  print -- "已验证 $result_key：$media_path"
done

for result_key in direct hls; do
  media_path="$(node -p "JSON.parse(require('fs').readFileSync(process.argv[1], 'utf8')).outputs[process.argv[2]]" "$browser_results_path" "$result_key")"
  probe_json="$(ffprobe -v error -show_entries format=duration -show_entries stream=codec_type,codec_name \
    -of json "$media_path")"
  node -e '
    const probe = JSON.parse(process.argv[1]);
    const duration = Number(probe.format && probe.format.duration);
    const codecs = new Map((probe.streams || []).map((stream) => [stream.codec_type, stream.codec_name]));
    if (!(duration >= 1.5 && duration <= 3.0)) throw new Error(`unexpected duration ${duration}`);
    if (codecs.get("video") !== "h264") throw new Error(`unexpected video codec ${codecs.get("video")}`);
    if (codecs.get("audio") !== "aac") throw new Error(`unexpected audio codec ${codecs.get("audio")}`);
  ' "$probe_json"
  print -- "已验证 Chrome popup $result_key：$media_path"
done

print -- "Smoke test 全部通过。"
