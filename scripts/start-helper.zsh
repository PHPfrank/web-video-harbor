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
  helper_remove_pid_file
fi

if [[ -L "$helper_log_path" || ( -e "$helper_log_path" && ! -f "$helper_log_path" ) ]]; then
  print -u2 -- "日志路径不是安全的普通文件：$helper_log_path"
  exit 1
fi
: >"$helper_log_path"
chmod 0600 "$helper_log_path"

pid_temp="$helper_state_dir/.helper.pid.$$"
if [[ "${pid_temp:A:h}" != "${helper_state_dir:A}" ]]; then
  print -u2 -- "PID 临时路径无效"
  exit 1
fi
trap 'rm -f -- "$pid_temp"' EXIT

nohup "$helper_binary" --config "$helper_config_path" \
  >"$helper_log_path" 2>&1 </dev/null &
started_pid=$!
print -r -- "$started_pid" >"$pid_temp"
chmod 0600 "$pid_temp"
mv -f -- "$pid_temp" "$helper_pid_path"
chmod 0600 "$helper_pid_path"

for attempt in {1..50}; do
  /bin/sleep 0.1
  if ! kill -0 "$started_pid" 2>/dev/null; then
    break
  fi
  if helper_health_response && helper_process_matches "$started_pid"; then
    print -- "本地助手启动成功（PID $started_pid）。"
    print -- "状态目录：$helper_state_dir"
    exit 0
  fi
done

if kill -0 "$started_pid" 2>/dev/null && helper_process_matches "$started_pid"; then
  kill -TERM "$started_pid" 2>/dev/null || true
fi
for attempt in {1..20}; do
  kill -0 "$started_pid" 2>/dev/null || break
  /bin/sleep 0.1
done
if kill -0 "$started_pid" 2>/dev/null && helper_process_matches "$started_pid"; then
  kill -KILL "$started_pid" 2>/dev/null || true
fi
helper_remove_pid_file
print -u2 -- "本地助手未能在 5 秒内通过健康检查，请查看：$helper_log_path"
exit 1
