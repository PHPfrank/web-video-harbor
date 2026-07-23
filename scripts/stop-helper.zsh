#!/bin/zsh
set -euo pipefail

script_dir="${0:A:h}"
source "$script_dir/helper-common.zsh"
helper_initialize_paths "$0"

if [[ ! -e "$helper_state_dir" && ! -L "$helper_state_dir" ]]; then
  print -- "本地助手未运行（没有状态目录）。"
  exit 0
fi
helper_validate_existing_state_dir

if [[ ! -e "$helper_pid_path" && ! -L "$helper_pid_path" ]]; then
  print -- "本地助手未运行（没有 PID 文件）。"
  exit 0
fi
if ! helper_read_pid; then
  print -u2 -- "PID 文件无效，未执行停止操作：$helper_pid_path"
  exit 1
fi
recorded_pid="$REPLY"

if ! kill -0 "$recorded_pid" 2>/dev/null; then
  helper_remove_pid_file
  print -- "本地助手已停止；已清理过期 PID 文件。"
  exit 0
fi
if ! helper_process_matches "$recorded_pid"; then
  print -u2 -- "PID $recorded_pid 的进程身份不匹配，已拒绝发送信号。"
  exit 1
fi

kill -TERM "$recorded_pid"
for attempt in {1..120}; do
  if ! kill -0 "$recorded_pid" 2>/dev/null; then
    helper_remove_pid_file
    print -- "本地助手已停止。"
    exit 0
  fi
  /bin/sleep 0.1
done

if ! helper_process_matches "$recorded_pid"; then
  print -u2 -- "等待期间 PID 身份发生变化，已拒绝强制停止。"
  exit 1
fi
kill -KILL "$recorded_pid"
for attempt in {1..20}; do
  if ! kill -0 "$recorded_pid" 2>/dev/null; then
    helper_remove_pid_file
    print -- "本地助手已强制停止。"
    exit 0
  fi
  /bin/sleep 0.1
done

print -u2 -- "无法确认本地助手已经停止；PID 文件已保留。"
exit 1
