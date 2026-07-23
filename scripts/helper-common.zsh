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
  helper_lock_path="$helper_state_dir/helper.lifecycle.lock"
  helper_lock_owned=""
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

helper_validate_lifecycle_lock() {
  local expected_lock="$helper_state_dir/helper.lifecycle.lock"
  if [[ ! -e "$helper_lock_path" && ! -L "$helper_lock_path" ]]; then
    return 2
  fi
  if [[ "$helper_lock_path" != "$expected_lock" || ! -f "$helper_lock_path" || \
    -L "$helper_lock_path" || ! -O "$helper_lock_path" ]]; then
    print -u2 -- "生命周期锁不安全：不是当前用户拥有的普通文件：$helper_lock_path"
    return 1
  fi
  local lock_mode
  lock_mode="$(/usr/bin/stat -f '%Lp' "$helper_lock_path" 2>/dev/null)" || {
    [[ ! -e "$helper_lock_path" && ! -L "$helper_lock_path" ]] && return 2
    print -u2 -- "无法安全读取生命周期锁权限：$helper_lock_path"
    return 1
  }
  if [[ "$lock_mode" != "600" ]]; then
    print -u2 -- "生命周期锁权限不安全，应为 0600：$helper_lock_path"
    return 1
  fi
  local lock_owner
  lock_owner="$(<"$helper_lock_path")" || {
    [[ ! -e "$helper_lock_path" && ! -L "$helper_lock_path" ]] && return 2
    print -u2 -- "无法安全读取生命周期锁 owner：$helper_lock_path"
    return 1
  }
  if [[ "$lock_owner" != <-> || "$lock_owner" -le 1 ]]; then
    print -u2 -- "生命周期锁 owner PID 无效：$helper_lock_path"
    return 1
  fi
  REPLY="$lock_owner"
}

helper_acquire_lifecycle_lock() {
  helper_validate_existing_state_dir || return 1
  [[ -x /usr/bin/shlock ]] || {
    print -u2 -- "缺少 macOS 生命周期锁工具：/usr/bin/shlock"
    return 1
  }

  integer lock_attempts=100
  if [[ "${WEB_VIDEO_HELPER_TESTING:-}" == "1" && \
    "${WEB_VIDEO_HELPER_TEST_LOCK_ATTEMPTS:-}" == <-> && \
    "${WEB_VIDEO_HELPER_TEST_LOCK_ATTEMPTS:-0}" -ge 1 && \
    "${WEB_VIDEO_HELPER_TEST_LOCK_ATTEMPTS:-0}" -le 100 ]]; then
    lock_attempts="$WEB_VIDEO_HELPER_TEST_LOCK_ATTEMPTS"
  fi

  local attempt lock_validation_status
  for attempt in {1..$lock_attempts}; do
    if [[ -e "$helper_lock_path" || -L "$helper_lock_path" ]]; then
      helper_validate_lifecycle_lock || {
        lock_validation_status=$?
        (( lock_validation_status == 2 )) && continue
        return 1
      }
    fi

    if (umask 077; /usr/bin/shlock -p "$$" -f "$helper_lock_path" 2>/dev/null); then
      chmod 0600 "$helper_lock_path" || return 1
      helper_validate_lifecycle_lock || return 1
      if [[ "$REPLY" != "$$" ]]; then
        print -u2 -- "生命周期锁 owner 与当前脚本不匹配"
        return 1
      fi
      helper_lock_owned="1"
      return 0
    fi

    if [[ -e "$helper_lock_path" || -L "$helper_lock_path" ]]; then
      helper_validate_lifecycle_lock || {
        lock_validation_status=$?
        (( lock_validation_status == 2 )) && continue
        return 1
      }
    fi
    /bin/sleep 0.05
  done

  print -u2 -- "等待本地助手生命周期锁超时，请稍后重试。"
  return 1
}

helper_release_lifecycle_lock() {
  [[ "$helper_lock_owned" == "1" ]] || return 0
  if ! helper_validate_lifecycle_lock || [[ "$REPLY" != "$$" ]]; then
    print -u2 -- "生命周期锁 owner 已变化，拒绝删除他人锁。"
    return 1
  fi
  rm -f -- "$helper_lock_path" || return 1
  helper_lock_owned=""
}

helper_test_stage_fails() {
  local requested_stage="$1"
  [[ "${WEB_VIDEO_HELPER_TESTING:-}" == "1" && \
    "${WEB_VIDEO_HELPER_TEST_FAIL_STAGE:-}" == "$requested_stage" ]]
}

helper_test_barrier() {
  local requested_stage="$1"
  if [[ "${WEB_VIDEO_HELPER_TESTING:-}" != "1" || \
    "${WEB_VIDEO_HELPER_TEST_BARRIER_STAGE:-}" != "$requested_stage" ]]; then
    return 0
  fi
  local barrier_path="${WEB_VIDEO_HELPER_TEST_BARRIER_PATH:-}"
  local barrier_real="${barrier_path:A}"
  if [[ -z "$barrier_path" || "$barrier_real" != "${helper_work_dir:A}"/* || \
    -e "$barrier_real.ready" || -L "$barrier_real.ready" ]]; then
    print -u2 -- "测试生命周期屏障路径不安全"
    return 1
  fi
  print -r -- "$$" >"$barrier_real.ready" || return 1
  chmod 0600 "$barrier_real.ready" || return 1
  local attempt
  for attempt in {1..250}; do
    [[ -f "$barrier_real.release" && ! -L "$barrier_real.release" ]] && return 0
    /bin/sleep 0.02
  done
  print -u2 -- "等待测试生命周期屏障释放超时"
  return 1
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
  local expected_pid="${1:-}"
  local expected_path="$helper_state_dir/helper.pid"
  if [[ "$helper_pid_path" != "$expected_path" || "${helper_pid_path:A:h}" != "${helper_state_dir:A}" ]]; then
    print -u2 -- "拒绝删除未确认的 PID 路径"
    return 1
  fi
  if [[ -n "$expected_pid" ]] && { ! helper_read_pid || [[ "$REPLY" != "$expected_pid" ]]; }; then
    print -u2 -- "PID 文件内容已变化，拒绝删除：$helper_pid_path"
    return 1
  fi
  rm -f -- "$helper_pid_path"
}

helper_create_pid_temp() {
  if helper_test_stage_fails temp-create; then
    print -u2 -- "测试注入：PID 临时文件创建失败"
    return 1
  fi
  local created_path
  created_path="$(umask 077; /usr/bin/mktemp "$helper_state_dir/.helper.pid.XXXXXX")" || return 1
  if [[ "${created_path:A:h}" != "${helper_state_dir:A}" || ! -f "$created_path" || \
    -L "$created_path" || ! -O "$created_path" || \
    "$(/usr/bin/stat -f '%Lp' "$created_path" 2>/dev/null)" != "600" ]]; then
    print -u2 -- "PID 临时文件不安全：$created_path"
    return 1
  fi
  REPLY="$created_path"
}

helper_write_pid_temp() {
  local pid_temp="$1"
  local expected_pid="$2"
  if [[ "${pid_temp:A:h}" != "${helper_state_dir:A}" || ! -f "$pid_temp" || \
    -L "$pid_temp" || ! -O "$pid_temp" ]]; then
    print -u2 -- "拒绝写入不安全的 PID 临时文件"
    return 1
  fi
  if helper_test_stage_fails pid-write; then
    print -u2 -- "测试注入：PID 写入失败"
    return 1
  fi
  zmodload zsh/system || return 1
  integer pid_fd=-1
  sysopen -w -o nofollow,trunc -u pid_fd "$pid_temp" || return 1
  if ! syswrite -o "$pid_fd" "$expected_pid"$'\n'; then
    exec {pid_fd}>&-
    return 1
  fi
  exec {pid_fd}>&-
  if helper_test_stage_fails pid-chmod; then
    print -u2 -- "测试注入：PID chmod 失败"
    return 1
  fi
  chmod 0600 "$pid_temp" || return 1
  [[ "$(<"$pid_temp")" == "$expected_pid" ]]
}

helper_publish_pid_temp() {
  local pid_temp="$1"
  local expected_pid="$2"
  if helper_test_stage_fails pid-publish; then
    print -u2 -- "测试注入：PID 原子发布失败"
    return 1
  fi
  [[ ! -e "$helper_pid_path" && ! -L "$helper_pid_path" ]] || return 1
  /bin/ln "$pid_temp" "$helper_pid_path" || return 1
  if ! helper_read_pid || [[ "$REPLY" != "$expected_pid" ]] || \
    [[ "$(/usr/bin/stat -f '%Lp' "$helper_pid_path" 2>/dev/null)" != "600" ]]; then
    return 1
  fi
  rm -f -- "$pid_temp"
}

helper_ps_command() {
  REPLY="/bin/ps"
  if [[ "${WEB_VIDEO_HELPER_TESTING:-}" == "1" && -n "${WEB_VIDEO_HELPER_TEST_PS_COMMAND:-}" ]]; then
    local requested_ps_real="${WEB_VIDEO_HELPER_TEST_PS_COMMAND:A}"
    if [[ "$requested_ps_real" != "${helper_work_dir:A}"/* || ! -x "$requested_ps_real" ]]; then
      return 1
    fi
    REPLY="$requested_ps_real"
  fi
}

helper_started_child_matches() {
  local candidate_pid="$1"
  local expected_parent_pid="$2"
  helper_process_matches "$candidate_pid" || return 1
  helper_ps_command || return 1
  local child_parent
  child_parent="$("$REPLY" -p "$candidate_pid" -o ppid= 2>/dev/null)" || return 1
  child_parent="${child_parent//[[:space:]]/}"
  [[ "$child_parent" == "$expected_parent_pid" ]]
}

helper_wait_started_child_match() {
  local candidate_pid="$1"
  local expected_parent_pid="$2"
  local attempt
  for attempt in {1..20}; do
    helper_started_child_matches "$candidate_pid" "$expected_parent_pid" && return 0
    kill -0 "$candidate_pid" 2>/dev/null || return 1
    /bin/sleep 0.02
  done
  return 1
}

helper_ensure_pid_record() {
  local expected_pid="$1"
  local existing_temp="${2:-}"
  if helper_read_pid 2>/dev/null && [[ "$REPLY" == "$expected_pid" ]]; then
    return 0
  fi
  [[ ! -e "$helper_pid_path" && ! -L "$helper_pid_path" ]] || return 1

  local recovery_temp="$existing_temp"
  if [[ -z "$recovery_temp" || ! -f "$recovery_temp" || -L "$recovery_temp" || \
    ! -O "$recovery_temp" || "${recovery_temp:A:h}" != "${helper_state_dir:A}" || \
    "$(<"$recovery_temp")" != "$expected_pid" ]]; then
    recovery_temp="$(umask 077; /usr/bin/mktemp "$helper_state_dir/.helper.pid.recovery.XXXXXX")" || return 1
    print -r -- "$expected_pid" >"$recovery_temp" || return 1
  fi
  chmod 0600 "$recovery_temp" || return 1
  /bin/ln "$recovery_temp" "$helper_pid_path" || return 1
  rm -f -- "$recovery_temp"
  helper_read_pid && [[ "$REPLY" == "$expected_pid" ]]
}

helper_rollback_started_process() {
  local started_pid="$1"
  local parent_pid="$2"
  local pid_temp="${3:-}"
  if ! kill -0 "$started_pid" 2>/dev/null; then
    if helper_read_pid 2>/dev/null && [[ "$REPLY" == "$started_pid" ]]; then
      helper_remove_pid_file "$started_pid" || return 1
    fi
    return 0
  fi

  if ! helper_wait_started_child_match "$started_pid" "$parent_pid"; then
    helper_ensure_pid_record "$started_pid" "$pid_temp" || true
    print -u2 -- "无法确认刚启动进程仍属于当前脚本，无法安全清理；未发送信号，PID 文件已保留。"
    return 1
  fi
  kill -TERM "$started_pid" 2>/dev/null || true
  local attempt
  for attempt in {1..40}; do
    kill -0 "$started_pid" 2>/dev/null || break
    /bin/sleep 0.05
  done
  if kill -0 "$started_pid" 2>/dev/null; then
    if ! helper_started_child_matches "$started_pid" "$parent_pid"; then
      helper_ensure_pid_record "$started_pid" "$pid_temp" || true
      print -u2 -- "回滚期间进程身份发生变化；PID 文件已保留。"
      return 1
    fi
    kill -KILL "$started_pid" 2>/dev/null || true
  fi
  for attempt in {1..40}; do
    kill -0 "$started_pid" 2>/dev/null || break
    /bin/sleep 0.05
  done
  if kill -0 "$started_pid" 2>/dev/null; then
    helper_ensure_pid_record "$started_pid" "$pid_temp" || true
    print -u2 -- "无法确认刚启动进程已经结束；PID 文件已保留。"
    return 1
  fi
  if helper_read_pid 2>/dev/null && [[ "$REPLY" == "$started_pid" ]]; then
    helper_remove_pid_file "$started_pid" || return 1
  fi
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
