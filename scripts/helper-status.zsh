#!/bin/zsh
set -euo pipefail

script_dir="${0:A:h}"
source "$script_dir/helper-common.zsh"
helper_initialize_paths "$0"

helper_prepare_state_dir
helper_validate_existing_state_dir

status_cleanup() {
  local original_status=$?
  trap - EXIT INT TERM
  helper_release_lifecycle_lock || original_status=1
  return "$original_status"
}
trap status_cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
if ! helper_acquire_lifecycle_lock; then
  exit 1
fi
if ! helper_test_barrier after-lock; then
  exit 1
fi

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
health_extract() {
  /usr/bin/plutil -extract "$1" raw -expect "$2" -o - - <<<"$health_json" 2>/dev/null
}
health_schema_valid="1"
health_ready="$(health_extract ready bool)" || health_schema_valid=""
health_pid="$(health_extract pid integer)" || health_schema_valid=""
health_version="$(health_extract version string)" || health_schema_valid=""
health_ffmpeg="$(health_extract ffmpeg bool)" || health_schema_valid=""
platform_available="$(health_extract platformDownloader.available bool)" || health_schema_valid=""
platform_version="$(health_extract platformDownloader.version string)" || health_schema_valid=""
if [[ "$platform_available" == "true" ]]; then
  parsed_platform_date="$(/bin/date -j -f '%Y.%m.%d' "$platform_version" '+%Y.%m.%d' 2>/dev/null)" || health_schema_valid=""
  [[ "$parsed_platform_date" == "$platform_version" ]] || health_schema_valid=""
elif [[ "$platform_available" == "false" ]]; then
  [[ -z "$platform_version" ]] || health_schema_valid=""
else
  health_schema_valid=""
fi
if [[ -z "$health_schema_valid" || "$health_ready" != "true" || "$health_pid" != <-> || -z "$health_version" ]]; then
  print -u2 -- "助手状态：健康响应格式无效"
  exit 1
fi
if [[ "$health_pid" != "$recorded_pid" ]]; then
  print -u2 -- "助手状态：PID 存在，但健康端点不属于该实例"
  exit 1
fi

print -- "助手状态：健康"
print -- "PID：$recorded_pid"
if [[ "$health_ffmpeg" == "true" ]]; then
  print -- "FFmpeg：可用"
else
  print -- "FFmpeg：未安装"
fi
if [[ "$platform_available" == "true" ]] && \
  [[ "$platform_version" =~ '^[0-9]{4}\.[0-9]{2}\.[0-9]{2}$' ]]; then
  print -- "平台解析器: 可用（$platform_version）"
else
  print -- "平台解析器: 不可用"
fi
print -- "版本：$health_version"
helper_config_download_dir
print -- "下载目录：$REPLY"
print -- "状态目录：$helper_state_dir"
