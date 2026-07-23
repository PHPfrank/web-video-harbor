#!/bin/zsh
set -u
setopt NO_BG_NICE
trap '' HUP

script_dir="${0:A:h}"
source "$script_dir/helper-common.zsh"
helper_initialize_paths "$0"

if (( $# != 1 )) || [[ "$1" != "$helper_log_path" ]]; then
  exit 2
fi
target_log="$1"
helper_validate_existing_state_dir || exit 1

zmodload zsh/system || exit 1
zmodload zsh/files || exit 1
export LC_ALL=C

integer max_bytes=1048576
integer keep_bytes=524288
integer chunk_bytes=8192
integer log_fd=-1
integer marker_fd=-1
integer copied=0
integer read_result=0
integer used_bytes=0
integer recent_bytes=0
integer logging_enabled=1
local chunk=""
local recent_data=""
rotate_temp="$helper_state_dir/.helper.log.rotate.$$"
logger_pid_path=""

disable_log_storage() {
  if (( log_fd >= 0 )); then
    exec {log_fd}>&-
    log_fd=-1
  fi
  if [[ "$rotate_temp" == "$helper_state_dir"/.helper.log.rotate.<-> ]]; then
    rm -f -- "$rotate_temp" 2>/dev/null || true
  fi
  logging_enabled=0
  recent_data=""
  recent_bytes=0
}

cleanup_logger() {
  if (( log_fd >= 0 )); then
    exec {log_fd}>&-
    log_fd=-1
  fi
  if [[ -n "$logger_pid_path" && "$logger_pid_path" == "${helper_work_dir:A}"/* ]]; then
    rm -f -- "$logger_pid_path"
  fi
  if [[ "$rotate_temp" == "$helper_state_dir"/.helper.log.rotate.<-> ]]; then
    rm -f -- "$rotate_temp"
  fi
}
trap cleanup_logger EXIT INT TERM

if [[ "${WEB_VIDEO_HELPER_TESTING:-}" == "1" && -n "${WEB_VIDEO_HELPER_TEST_LOGGER_PID_PATH:-}" ]]; then
  logger_pid_path="${WEB_VIDEO_HELPER_TEST_LOGGER_PID_PATH:A}"
  if [[ "$logger_pid_path" != "${helper_work_dir:A}"/* || -e "$logger_pid_path" || -L "$logger_pid_path" ]]; then
    exit 1
  fi
  sysopen -w -o creat,excl,nofollow -m 0600 -u marker_fd "$logger_pid_path" || exit 1
  syswrite -o "$marker_fd" "$$"$'\n' || exit 1
  exec {marker_fd}>&-
  marker_fd=-1
fi

if ! sysopen -w -o creat,nofollow,trunc -m 0600 -u log_fd "$target_log"; then
  disable_log_storage
fi

while true; do
  chunk=""
  copied=0
  sysread -i 0 -s "$chunk_bytes" -c copied chunk
  read_result=$?
  if (( read_result == 5 )); then
    break
  fi
  if (( read_result != 0 )); then
    exit "$read_result"
  fi

  if (( logging_enabled && used_bytes + copied > max_bytes )); then
    exec {log_fd}>&-
    log_fd=-1
    if ! sysopen -w -o creat,excl,nofollow -m 0600 -u log_fd "$rotate_temp"; then
      disable_log_storage
    elif (( recent_bytes > 0 )) && ! syswrite -o "$log_fd" "$recent_data"; then
      disable_log_storage
    else
      exec {log_fd}>&-
      log_fd=-1
      if ! mv -f -- "$rotate_temp" "$target_log"; then
        disable_log_storage
      elif ! sysopen -a -o nofollow -u log_fd "$target_log"; then
        disable_log_storage
      else
        used_bytes=$recent_bytes
      fi
    fi
  fi

  if (( logging_enabled )); then
    if ! syswrite -o "$log_fd" "$chunk"; then
      disable_log_storage
    else
      (( used_bytes += copied ))

      recent_data+="$chunk"
      (( recent_bytes += copied ))
      if (( recent_bytes > keep_bytes )); then
        integer drop_bytes=$(( recent_bytes - keep_bytes ))
        recent_data="${recent_data[$(( drop_bytes + 1 )),-1]}"
        recent_bytes=$keep_bytes
      fi
    fi
  fi
done

exit 0
