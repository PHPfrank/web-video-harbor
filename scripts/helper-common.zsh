#!/bin/zsh

helper_initialize_paths() {
  local calling_script="$1"
  local user_directory="${HOME:?无法确定用户目录}"

  helper_script_dir="${calling_script:A:h}"
  helper_repo_root="${helper_script_dir:h}"
  helper_repo_real="${helper_repo_root:A}"
  helper_work_dir="$helper_repo_root/work"
  helper_binary="$helper_work_dir/dist/web-video-helper"
  helper_state_dir="$user_directory/Library/Application Support/网页视频下载器"
  helper_download_dir="$user_directory/Downloads/网页视频下载器"

  if [[ "${WEB_VIDEO_HELPER_TESTING:-}" == "1" ]]; then
    local requested_state="${WEB_VIDEO_HELPER_TEST_STATE_DIR:?测试状态目录不能为空}"
    local allowed_work_real="${helper_work_dir:A}"
    local ancestry_path="$requested_state"
    while [[ "${ancestry_path:A}" != "$allowed_work_real" && "$ancestry_path" != "/" ]]; do
      if [[ -L "$ancestry_path" ]]; then
        print -u2 -- "测试状态目录不能包含符号链接"
        return 1
      fi
      ancestry_path="${ancestry_path:h}"
    done
    local requested_real="${requested_state:A}"
    if [[ "$requested_real" != "$allowed_work_real"/* ]]; then
      print -u2 -- "测试状态目录必须位于当前项目 work/ 内"
      return 1
    fi
    helper_state_dir="$requested_real"
  fi

  helper_config_path="$helper_state_dir/config.json"
  helper_pid_path="$helper_state_dir/helper.pid"
  helper_log_path="$helper_state_dir/helper.log"
  helper_health_url="http://127.0.0.1:17432/health"

  if [[ -n "${WEB_VIDEO_HELPER_TEST_ADDRESS:-}" ]]; then
    if [[ "${WEB_VIDEO_HELPER_TESTING:-}" != "1" ]]; then
      print -u2 -- "health 地址覆盖只允许用于受控测试"
      return 1
    fi
    local requested_address="$WEB_VIDEO_HELPER_TEST_ADDRESS"
    if [[ ! "$requested_address" =~ '^127\.0\.0\.1:([1-9][0-9]{0,4})$' ]]; then
      print -u2 -- "测试 health 地址必须是 127.0.0.1 和有效端口"
      return 1
    fi
    local requested_port="${match[1]}"
    if (( requested_port > 65535 )); then
      print -u2 -- "测试 health 端口超出范围"
      return 1
    fi
    helper_health_url="http://$requested_address/health"
  fi
}

helper_prepare_state_dir() {
  if [[ -L "$helper_state_dir" ]]; then
    print -u2 -- "拒绝使用符号链接状态目录：$helper_state_dir"
    return 1
  fi
  mkdir -p "$helper_state_dir"
  if [[ ! -d "$helper_state_dir" || -L "$helper_state_dir" ]]; then
    print -u2 -- "状态路径不是安全目录：$helper_state_dir"
    return 1
  fi
  chmod 0700 "$helper_state_dir"
}

helper_validate_existing_state_dir() {
  if [[ ! -d "$helper_state_dir" || -L "$helper_state_dir" || ! -O "$helper_state_dir" ]]; then
    print -u2 -- "状态路径不是当前用户拥有的真实目录：$helper_state_dir"
    return 1
  fi
  if [[ "$(/usr/bin/stat -f '%Lp' "$helper_state_dir" 2>/dev/null)" != "700" ]]; then
    print -u2 -- "状态目录权限不安全，应为 0700：$helper_state_dir"
    return 1
  fi

  local state_file state_mode
  for state_file in "$helper_config_path" "$helper_pid_path" "$helper_log_path"; do
    if [[ -e "$state_file" || -L "$state_file" ]]; then
      if [[ ! -f "$state_file" || -L "$state_file" || ! -O "$state_file" ]]; then
        print -u2 -- "状态文件不是当前用户拥有的普通文件：$state_file"
        return 1
      fi
      state_mode="$(/usr/bin/stat -f '%Lp' "$state_file" 2>/dev/null)" || return 1
      if [[ "$state_mode" != "600" ]]; then
        print -u2 -- "状态文件权限不安全，应为 0600：$state_file"
        return 1
      fi
    fi
  done
}

helper_read_pid() {
  if [[ ! -f "$helper_pid_path" || -L "$helper_pid_path" ]]; then
    return 1
  fi
  local recorded_pid="$(<"$helper_pid_path")"
  if [[ "$recorded_pid" != <-> || "$recorded_pid" -le 1 ]]; then
    return 1
  fi
  REPLY="$recorded_pid"
}

helper_process_matches() {
  local candidate_pid="$1"
  local ps_command="/bin/ps"
  local lsof_command="/usr/sbin/lsof"
  if [[ "${WEB_VIDEO_HELPER_TESTING:-}" == "1" && -n "${WEB_VIDEO_HELPER_TEST_PS_COMMAND:-}" ]]; then
    local requested_ps_real="${WEB_VIDEO_HELPER_TEST_PS_COMMAND:A}"
    local allowed_work_real="${helper_work_dir:A}"
    if [[ "$requested_ps_real" != "$allowed_work_real"/* || ! -x "$requested_ps_real" ]]; then
      print -u2 -- "测试进程检查器必须是当前项目 work/ 内的可执行文件"
      return 1
    fi
    ps_command="$requested_ps_real"
  fi
  if [[ "${WEB_VIDEO_HELPER_TESTING:-}" == "1" && -n "${WEB_VIDEO_HELPER_TEST_LSOF_COMMAND:-}" ]]; then
    local requested_lsof_real="${WEB_VIDEO_HELPER_TEST_LSOF_COMMAND:A}"
    local allowed_lsof_work_real="${helper_work_dir:A}"
    if [[ "$requested_lsof_real" != "$allowed_lsof_work_real"/* || ! -x "$requested_lsof_real" ]]; then
      print -u2 -- "测试可执行文件检查器必须是当前项目 work/ 内的可执行文件"
      return 1
    fi
    lsof_command="$requested_lsof_real"
  fi
  [[ -x "$lsof_command" ]] || return 1

  local lsof_output expected_executable executable_matched=""
  lsof_output="$("$lsof_command" -a -p "$candidate_pid" -d txt -Fn 2>/dev/null)" || return 1
  expected_executable="${helper_binary:A}"
  local lsof_line
  while IFS= read -r lsof_line; do
    if [[ "$lsof_line" == "n$expected_executable" ]]; then
      executable_matched="1"
      break
    fi
  done <<<"$lsof_output"
  [[ -n "$executable_matched" ]] || return 1

  local process_command
  process_command="$("$ps_command" -ww -p "$candidate_pid" -o command= 2>/dev/null)" || return 1
  [[ -n "$process_command" ]] || return 1
  [[ "$process_command" == "$helper_binary --config $helper_config_path" ]]
}

helper_remove_pid_file() {
  local expected_path="$helper_state_dir/helper.pid"
  if [[ "$helper_pid_path" != "$expected_path" || "${helper_pid_path:A:h}" != "${helper_state_dir:A}" ]]; then
    print -u2 -- "拒绝删除未确认的 PID 路径"
    return 1
  fi
  rm -f -- "$helper_pid_path"
}

helper_health_response() {
  local curl_command
  curl_command="$(command -v curl)" || return 1
  REPLY="$("$curl_command" -fsS --max-time 1 "$helper_health_url" 2>/dev/null)" || return 1
  [[ "$REPLY" == *'"ready":true'* ]] || return 1
  helper_health_pid=""
  if [[ "$REPLY" =~ '"pid":[[:space:]]*([0-9]+)' ]]; then
    helper_health_pid="${match[1]}"
  fi
}

helper_config_download_dir() {
  REPLY="$helper_download_dir"
  if [[ -f "$helper_config_path" && ! -L "$helper_config_path" && -x /usr/bin/plutil ]]; then
    local configured_dir
    configured_dir="$(/usr/bin/plutil -extract download_dir raw -o - "$helper_config_path" 2>/dev/null)" || return 0
    if [[ "$configured_dir" == /* ]]; then
      REPLY="$configured_dir"
    fi
  fi
}
