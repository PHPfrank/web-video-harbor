//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"web-video-harbor/helper/internal/ytdlp"
)

func prepareFakePlatformDownloader(t *testing.T, repoRoot, downloadDir, ffmpegPath string) ytdlp.ProbeResult {
	t.Helper()
	if info, err := os.Stat(ffmpegPath); err != nil || info.IsDir() {
		t.Fatalf("fake platform FFmpeg is unavailable: path=%q info=%v err=%v", ffmpegPath, info, err)
	}
	fakeRoot := filepath.Join(repoRoot, "work", "smoke-platform")
	if err := os.MkdirAll(fakeRoot, 0o700); err != nil {
		t.Fatalf("create fake platform root: %v", err)
	}
	fakePath := filepath.Join(fakeRoot, "yt-dlp_macos")
	retryMarker := filepath.Join(downloadDir, ".web-video-platform-retry-marker")
	if err := os.Remove(retryMarker); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove stale fake retry marker: %v", err)
	}
	script := `#!/bin/zsh
set -euo pipefail
unsetopt BG_NICE
if [[ "${1:-}" == "--version" && "$#" == "1" ]]; then
  print -- '2026.07.04'
  exit 0
fi

page_url="${@[-1]}"
staging_dir=''
ffmpeg_path=''
selector=''
while (( $# > 0 )); do
  case "$1" in
    --paths)
      shift
      staging_dir="${1#home:}"
      ;;
    --ffmpeg-location)
      shift
      ffmpeg_path="$1"
      ;;
    --format)
      shift
      selector="$1"
      ;;
  esac
  shift
done

[[ -n "$staging_dir" && -d "$staging_dir" && -x "$ffmpeg_path" ]] || exit 20
[[ "$selector" == 'bv*[height<=720]+ba/b[height<=720]' ]] || exit 21

if [[ "$page_url" == *'cancel12345'* ]]; then
  trap 'exit 143' INT TERM
  print -- $'WVH_PROGRESS:"video-137"\t10%'
  while true; do /bin/sleep 0.1; done
fi

retry_marker="${staging_dir:h}/.web-video-platform-retry-marker"
if [[ "$page_url" == *'retry123456'* && ! -e "$retry_marker" ]]; then
  print -r -- 'retry once' >"$retry_marker"
  print -u2 -- 'temporary network failure'
  exit 22
fi
if [[ "$page_url" == *'retry123456'* ]]; then
  /bin/rm -f -- "$retry_marker"
fi

print -- $'WVH_PROGRESS:"video-137"\t10%'
/bin/sleep 0.1
print -- $'WVH_PROGRESS:"video-137"\t100%'
print -- $'WVH_PROGRESS:"audio-140"\t10%'
"$ffmpeg_path" -hide_banner -loglevel error -nostdin -y \
  -f lavfi -i 'color=c=0x0ea5e9:s=1280x720:r=24' \
  -f lavfi -i 'sine=frequency=523:sample_rate=48000' \
  -t 2 -shortest -c:v libx264 -preset ultrafast -pix_fmt yuv420p \
  -c:a aac -b:a 64k "$staging_dir/media.mp4"
print -- $'WVH_PROGRESS:"audio-140"\t100%'
`
	if err := os.WriteFile(fakePath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake platform downloader: %v", err)
	}
	if err := os.Chmod(fakePath, 0o700); err != nil {
		t.Fatalf("chmod fake platform downloader: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Remove(retryMarker)
	})
	result, err := ytdlp.ProbeAdjacentForIntegration(context.Background(), filepath.Join(fakeRoot, "helper-placeholder"))
	if err != nil {
		t.Fatalf("probe fake platform downloader: %v", err)
	}
	if result.Version != "2026.07.04" || result.Path == "" || result.Snapshot == nil {
		_ = result.Close()
		t.Fatalf("fake platform identity is incomplete: %s", fmt.Sprintf("%+v", result))
	}
	return result
}
