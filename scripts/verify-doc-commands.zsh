#!/bin/zsh
set -euo pipefail
unsetopt BG_NICE

script_dir="${0:A:h}"
repo_root="${script_dir:h}"
repo_real="${repo_root:A}"
verification_root="$repo_root/work/doc-verification"
helper_binary="$repo_root/work/dist/web-video-helper"

for required_command in curl ps lsof file lipo mktemp; do
  command -v "$required_command" >/dev/null 2>&1 || {
    print -u2 -- "文档验证缺少命令：$required_command"
    exit 1
  }
done

if [[ -L "$verification_root" ]]; then
  print -u2 -- "拒绝使用符号链接验证目录：$verification_root"
  exit 1
fi
mkdir -p "$verification_root"
verification_real="${verification_root:A}"
[[ "$verification_real" == "$repo_real"/work/* ]] || {
  print -u2 -- "文档验证目录必须位于项目 work/ 内"
  exit 1
}

run_root="$(/usr/bin/mktemp -d "$verification_root/run.XXXXXX")"
state_dir="$run_root/state"
download_dir="$run_root/downloads"
start_output="$run_root/start.txt"
status_output="$run_root/status.txt"
stop_output="$run_root/stop.txt"
duplicate_output="$run_root/duplicate.txt"
logger_pid_path="$run_root/logger.pid"
saved_pid_path="$run_root/helper.pid.saved"
mkdir -p "$state_dir" "$download_dir"
chmod 0700 "$state_dir" "$download_dir"

helper_pid=""
pid_was_moved=""
cleanup_verification() {
  if [[ -n "$pid_was_moved" && -f "$saved_pid_path" && ! -e "$state_dir/helper.pid" ]]; then
    mv -f -- "$saved_pid_path" "$state_dir/helper.pid"
    chmod 0600 "$state_dir/helper.pid"
  fi
  if [[ -f "$state_dir/helper.pid" ]]; then
    env WEB_VIDEO_HELPER_TESTING=1 \
      WEB_VIDEO_HELPER_TEST_STATE_DIR="$state_dir" \
      WEB_VIDEO_HELPER_TEST_ADDRESS="127.0.0.1:$test_port" \
      "$script_dir/stop-helper.zsh" >/dev/null 2>&1 || true
  fi
}
trap cleanup_verification EXIT INT TERM

test_port=""
for attempt in {1..50}; do
  candidate_port=$(( 30000 + RANDOM % 20000 ))
  if ! /usr/bin/curl -fsS --max-time 0.2 "http://127.0.0.1:$candidate_port/health" >/dev/null 2>&1; then
    test_port="$candidate_port"
    break
  fi
done
[[ -n "$test_port" ]] || {
  print -u2 -- "无法找到空闲的测试回环端口"
  exit 1
}

verification_token='AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'
config_path="$state_dir/config.json"
print -r -- "{\"address\":\"127.0.0.1:$test_port\",\"token\":\"$verification_token\",\"download_dir\":\"$download_dir\"}" >"$config_path"
chmod 0600 "$config_path"

"$script_dir/build-macos.zsh" >/dev/null
/usr/bin/lipo "$helper_binary" -verify_arch arm64 x86_64
version_output="$("$helper_binary" --version)"
[[ "$version_output" == 'web-video-helper dev' ]] || {
  print -u2 -- "助手版本命令输出异常"
  exit 1
}
printed_token="$("$helper_binary" --config "$config_path" --print-token)"
[[ "$printed_token" =~ '^[A-Za-z0-9_-]{43}$' && "$printed_token" == "$verification_token" ]] || {
  print -u2 -- "配对密钥命令输出格式异常"
  exit 1
}

export WEB_VIDEO_HELPER_TESTING=1
export WEB_VIDEO_HELPER_TEST_STATE_DIR="$state_dir"
export WEB_VIDEO_HELPER_TEST_ADDRESS="127.0.0.1:$test_port"
export WEB_VIDEO_HELPER_TEST_LOGGER_PID_PATH="$logger_pid_path"

"$script_dir/start-helper.zsh" >"$start_output" 2>&1
helper_pid="$(<"$state_dir/helper.pid")"
[[ "$helper_pid" == <-> ]] || {
  print -u2 -- "真实启动未生成有效 helper PID"
  exit 1
}

health_json="$(/usr/bin/curl -fsS --max-time 1 "http://127.0.0.1:$test_port/health")"
[[ "$health_json" == *'"ready":true'* && "$health_json" =~ '"pid":([0-9]+)' && "${match[1]}" == "$helper_pid" ]] || {
  print -u2 -- "真实健康响应没有绑定 helper PID"
  exit 1
}

expected_executable="${helper_binary:A}"
lsof_output="$(/usr/sbin/lsof -a -p "$helper_pid" -d txt -Fn)"
[[ "$lsof_output" == *$'\n'"n$expected_executable"* || "$lsof_output" == "n$expected_executable"* ]] || {
  print -u2 -- "lsof 未确认真实 universal helper"
  exit 1
}
expected_command="$helper_binary --config $config_path"
[[ "$(/bin/ps -ww -p "$helper_pid" -o command=)" == "$expected_command" ]] || {
  print -u2 -- "ps 未确认真实 helper 配置参数"
  exit 1
}

"$script_dir/helper-status.zsh" >"$status_output" 2>&1
[[ "$(<"$status_output")" == *'助手状态：健康'* ]] || {
  print -u2 -- "真实状态命令未报告健康"
  exit 1
}
for sensitive_output in "$start_output" "$status_output"; do
  if [[ "$(<"$sensitive_output")" == *"$printed_token"* ]]; then
    print -u2 -- "生命周期命令输出了配对密钥"
    exit 1
  fi
done

logger_pid="$(<"$logger_pid_path")"
[[ "$logger_pid" == <-> ]] && kill -0 "$logger_pid" 2>/dev/null || {
  print -u2 -- "真实 bounded logger 未运行"
  exit 1
}

mv "$state_dir/helper.pid" "$saved_pid_path"
pid_was_moved="1"
if "$script_dir/start-helper.zsh" >"$duplicate_output" 2>&1; then
  print -u2 -- "移走 PID 文件后，已有健康实例未阻止重复启动"
  exit 1
fi
[[ "$(<"$duplicate_output")" == *'健康端点已有实例'* ]] || {
  print -u2 -- "已有健康实例的拒绝提示异常"
  exit 1
}
mv "$saved_pid_path" "$state_dir/helper.pid"
chmod 0600 "$state_dir/helper.pid"
pid_was_moved=""

"$script_dir/stop-helper.zsh" >"$stop_output" 2>&1
helper_pid=""
for attempt in {1..50}; do
  if ! kill -0 "$logger_pid" 2>/dev/null && [[ ! -e "$logger_pid_path" ]]; then
    break
  fi
  /bin/sleep 0.05
done
if kill -0 "$logger_pid" 2>/dev/null || [[ -e "$logger_pid_path" ]]; then
  print -u2 -- "停止真实 helper 后 bounded logger 仍有残留"
  exit 1
fi
if /usr/bin/curl -fsS --max-time 0.5 "http://127.0.0.1:$test_port/health" >/dev/null 2>&1; then
  print -u2 -- "停止后测试端口仍有健康响应"
  exit 1
fi

trap - EXIT INT TERM
print -- "真实文档命令验证通过：universal、配对格式、启动、状态、停止和日志清理。"
