#!/bin/zsh
set -euo pipefail

script_dir="${0:A:h}"
source "$script_dir/helper-common.zsh"
helper_initialize_paths "$0"

if [[ ! -e "$helper_pid_path" && ! -L "$helper_pid_path" ]]; then
  print -- "助手状态：未运行"
  print -- "状态目录：$helper_state_dir"
  helper_config_download_dir
  print -- "下载目录：$REPLY"
  exit 1
fi
if ! helper_read_pid; then
  print -u2 -- "助手状态：PID 文件无效"
  exit 1
fi
recorded_pid="$REPLY"
if ! kill -0 "$recorded_pid" 2>/dev/null; then
  print -- "助手状态：未运行（PID 文件已过期）"
  exit 1
fi
if ! helper_process_matches "$recorded_pid"; then
  print -u2 -- "助手状态：PID $recorded_pid 身份不匹配"
  exit 1
fi
if ! helper_health_response; then
  print -- "助手状态：进程存在，但健康检查失败"
  print -- "PID：$recorded_pid"
  exit 1
fi
health_json="$REPLY"

print -- "助手状态：健康"
print -- "PID：$recorded_pid"
if [[ "$health_json" == *'"ffmpeg":true'* ]]; then
  print -- "FFmpeg：可用"
else
  print -- "FFmpeg：未安装"
fi
if [[ "$health_json" =~ '"version":"([^"\\]*)"' ]]; then
  print -- "版本：${match[1]}"
fi
helper_config_download_dir
print -- "下载目录：$REPLY"
print -- "状态目录：$helper_state_dir"
