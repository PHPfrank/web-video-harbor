#!/bin/zsh
set -euo pipefail
unsetopt BG_NICE

script_dir="${0:A:h}"
source "$script_dir/helper-common.zsh"
helper_initialize_paths "$0"

if [[ ! -x "$helper_binary" ]]; then
  print -u2 -- "尚未找到本地助手，请先运行 scripts/build-macos.zsh。"
  exit 1
fi

helper_prepare_state_dir
helper_validate_existing_state_dir

started_pid=""
pid_temp=""
rollback_started=""

start_cleanup() {
  local original_status=$?
  trap - EXIT INT TERM
  if [[ "$rollback_started" == "1" && "$started_pid" == <-> ]]; then
    helper_rollback_started_process "$started_pid" "$$" "$pid_temp" || original_status=1
  fi
  if [[ -n "$pid_temp" && "${pid_temp:A:h}" == "${helper_state_dir:A}" && \
    "$pid_temp" == "$helper_state_dir"/.helper.pid.* ]]; then
    rm -f -- "$pid_temp"
  fi
  helper_release_lifecycle_lock || original_status=1
  return "$original_status"
}
trap start_cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if ! helper_acquire_lifecycle_lock; then
  exit 1
fi
if ! helper_test_barrier after-lock; then
  exit 1
fi

if [[ -e "$helper_pid_path" || -L "$helper_pid_path" ]]; then
  if ! helper_read_pid; then
    print -u2 -- "PID 文件无效，请先确认没有助手进程，再手动移走：$helper_pid_path"
    exit 1
  fi
  recorded_pid="$REPLY"
  if kill -0 "$recorded_pid" 2>/dev/null; then
    if helper_process_matches "$recorded_pid"; then
      print -u2 -- "本地助手已经运行（PID $recorded_pid），已拒绝重复启动。"
    else
      print -u2 -- "PID $recorded_pid 的进程身份不匹配，为避免误操作已拒绝启动。"
    fi
    exit 1
  fi
  helper_remove_pid_file "$recorded_pid"
fi

if helper_health_response; then
  print -u2 -- "目标健康端点已有实例响应，已拒绝启动新的本地助手。"
  exit 1
fi

if [[ -L "$helper_log_path" || ( -e "$helper_log_path" && ! -f "$helper_log_path" ) ]]; then
  print -u2 -- "日志路径不是安全的普通文件：$helper_log_path"
  exit 1
fi
: >"$helper_log_path"
chmod 0600 "$helper_log_path"

if ! helper_create_pid_temp; then
  exit 1
fi
pid_temp="$REPLY"

pending_start_signal=""
trap 'pending_start_signal=130' INT
trap 'pending_start_signal=143' TERM
nohup "$helper_binary" --config "$helper_config_path" \
  > >(/bin/zsh "$script_dir/bounded-log.zsh" "$helper_log_path") 2>&1 </dev/null &
if helper_test_stage_fails launch-signal; then
  kill -TERM "$$"
fi
started_pid=$!
rollback_started="1"
trap 'exit 130' INT
trap 'exit 143' TERM
if [[ -n "$pending_start_signal" ]]; then
  exit "$pending_start_signal"
fi
if ! helper_test_barrier after-launch; then
  exit 1
fi
if ! helper_write_pid_temp "$pid_temp" "$started_pid"; then
  exit 1
fi
if ! helper_publish_pid_temp "$pid_temp" "$started_pid"; then
  exit 1
fi
pid_temp=""

for attempt in {1..50}; do
  /bin/sleep 0.1
  if ! kill -0 "$started_pid" 2>/dev/null; then
    break
  fi
  if helper_health_response && [[ "$helper_health_pid" == "$started_pid" ]] && helper_process_matches "$started_pid"; then
    rollback_started=""
    print -- "本地助手启动成功（PID $started_pid）。"
    print -- "状态目录：$helper_state_dir"
    exit 0
  fi
done

print -u2 -- "本地助手未能在 5 秒内通过健康检查，请查看：$helper_log_path"
exit 1
