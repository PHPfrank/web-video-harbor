#!/bin/zsh
set -euo pipefail
unsetopt BG_NICE

script_dir="${0:A:h}"
repo_root="${script_dir:h:h}"
focused_case="${WEB_VIDEO_HELPER_FOCUSED_CASE:-all}"

case_enabled() {
  [[ "$focused_case" == "all" || "$focused_case" == "$1" ]]
}

finish_focused_case() {
  local completed_case="$1"
  if [[ "$focused_case" == "$completed_case" ]]; then
    print -- "聚焦用例通过：$completed_case"
    exit 0
  fi
}

fail() {
  print -u2 -- "FAIL: $1"
  exit 1
}

for script_name in build-macos.zsh start-helper.zsh stop-helper.zsh helper-status.zsh bounded-log.zsh verify-doc-commands.zsh; do
  script_path="$repo_root/scripts/$script_name"
  [[ -f "$script_path" ]] || fail "缺少脚本：$script_name"
  /bin/zsh -n "$script_path" || fail "脚本语法错误：$script_name"
done

common_path="$repo_root/scripts/helper-common.zsh"
[[ -f "$common_path" ]] || fail "缺少脚本共享路径实现"
/bin/zsh -n "$common_path" || fail "共享脚本语法错误"

if case_enabled bounded_random_temp; then
  rg -q 'mktemp.*helper\.log\.rotate\.XXXXXX' "$repo_root/scripts/bounded-log.zsh" || \
    fail "bounded logger 轮转临时文件没有使用同目录随机 O_EXCL 创建"
  finish_focused_case bounded_random_temp
fi
if case_enabled verify_bound_port; then
  rg -Fq 'testtools/loopback-port' "$repo_root/scripts/verify-doc-commands.zsh" || \
    fail "真实文档验证没有通过实际临时 bind 获取端口"
  finish_focused_case verify_bound_port
fi
if case_enabled verify_restore_order; then
  verify_text="$(<"$repo_root/scripts/verify-doc-commands.zsh")"
  if [[ "$verify_text" != *'pid_was_moved="1"'*'mv "$state_dir/helper.pid" "$saved_pid_path"'* ]]; then
    fail "PID 恢复标志没有在移动 PID 文件前设置"
  fi
  finish_focused_case verify_restore_order
fi
if case_enabled launch_signal_window; then
  start_text="$(<"$repo_root/scripts/start-helper.zsh")"
  if [[ "$start_text" != *'pending_start_signal=""'*"trap 'pending_start_signal=130' INT"*"trap 'pending_start_signal=143' TERM"*'nohup '*'started_pid=$!'*'rollback_started="1"'*"trap 'exit 130' INT"*"trap 'exit 143' TERM"*'[[ -n "$pending_start_signal" ]]'* ]]; then
    fail "start 没有在 helper launch/PID 捕获临界区延迟处理 INT/TERM"
  fi
fi

expected_state_dir="${HOME:?}/Library/Application Support/WebVideoHarbor"
expected_download_dir="${HOME:?}/Downloads/WebVideoHarbor"
path_output="$('/bin/zsh' -c '
  source "$1"
  helper_initialize_paths "$2"
  print -r -- "$helper_state_dir"
  print -r -- "$helper_download_dir"
' -- "$common_path" "$repo_root/scripts/start-helper.zsh")"
[[ "$path_output" == "$expected_state_dir"$'\n'"$expected_download_dir" ]] || \
  fail "默认状态或下载路径不符合约定"

test_state_dir="$repo_root/work/script-tests/path-check/state"
mkdir -p "$test_state_dir"
test_path_output="$(env WEB_VIDEO_HELPER_TESTING=1 \
  WEB_VIDEO_HELPER_TEST_STATE_DIR="$test_state_dir" /bin/zsh -c '
    source "$1"
    helper_initialize_paths "$2"
    print -r -- "$helper_state_dir"
  ' -- "$common_path" "$repo_root/scripts/start-helper.zsh")"
[[ "$test_path_output" == "$test_state_dir" ]] || fail "测试状态路径门闩未生效"

outside_test_state="${TMPDIR:-/tmp}/web-video-harbor-helper-outside-state"
if env WEB_VIDEO_HELPER_TESTING=1 WEB_VIDEO_HELPER_TEST_STATE_DIR="$outside_test_state" \
  /bin/zsh -c 'source "$1"; helper_initialize_paths "$2"' -- \
  "$common_path" "$repo_root/scripts/start-helper.zsh" >/dev/null 2>&1; then
  fail "测试门闩接受了仓库 work 目录以外的状态路径"
fi

if env WEB_VIDEO_HELPER_TEST_ADDRESS='127.0.0.1:23456' \
  /bin/zsh -c 'source "$1"; helper_initialize_paths "$2"' -- \
  "$common_path" "$repo_root/scripts/start-helper.zsh" >/dev/null 2>&1; then
  fail "非测试模式接受了 health 地址覆盖"
fi
test_health_url="$(env WEB_VIDEO_HELPER_TESTING=1 WEB_VIDEO_HELPER_TEST_STATE_DIR="$test_state_dir" \
  WEB_VIDEO_HELPER_TEST_ADDRESS='127.0.0.1:23456' /bin/zsh -c '
    source "$1"; helper_initialize_paths "$2"; print -r -- "$helper_health_url"
  ' -- "$common_path" "$repo_root/scripts/start-helper.zsh")"
[[ "$test_health_url" == 'http://127.0.0.1:23456/health' ]] || fail "测试 health 地址覆盖未生效"
if env WEB_VIDEO_HELPER_TESTING=1 WEB_VIDEO_HELPER_TEST_STATE_DIR="$test_state_dir" \
  WEB_VIDEO_HELPER_TEST_ADDRESS='0.0.0.0:23456' \
  /bin/zsh -c 'source "$1"; helper_initialize_paths "$2"' -- \
  "$common_path" "$repo_root/scripts/start-helper.zsh" >/dev/null 2>&1; then
  fail "测试 health 地址覆盖接受了非 loopback 地址"
fi

symlink_state_target="$repo_root/work/script-tests/path-check/symlink-target"
symlink_state_path="$repo_root/work/script-tests/path-check/symlink-state"
mkdir -p "$symlink_state_target"
rm -f -- "$symlink_state_path"
ln -s "$symlink_state_target" "$symlink_state_path"
if env WEB_VIDEO_HELPER_TESTING=1 WEB_VIDEO_HELPER_TEST_STATE_DIR="$symlink_state_path" \
  /bin/zsh -c 'source "$1"; helper_initialize_paths "$2"' -- \
  "$common_path" "$repo_root/scripts/start-helper.zsh" >/dev/null 2>&1; then
  fail "测试门闩接受了符号链接状态目录"
fi

if rg -n '^[[:space:]]*(export[[:space:]]+)?(HOME|home|CODEX_HOME)=' \
  "$repo_root/scripts"/*.zsh >/dev/null; then
  fail "脚本禁止重新赋值 HOME、home 或 CODEX_HOME"
fi

for readonly_script in stop-helper.zsh helper-status.zsh; do
  readonly_text="$(<"$repo_root/scripts/$readonly_script")"
  if [[ "$readonly_text" != *helper_prepare_state_dir*helper_validate_existing_state_dir*helper_acquire_lifecycle_lock*helper_read_pid* ]]; then
    fail "$readonly_script 未在读取 PID 前安全准备状态目录并获取生命周期锁"
  fi
done

dist_dir="$repo_root/work/dist"
arm_binary="$dist_dir/web-video-harbor-helper-arm64"
intel_binary="$dist_dir/web-video-harbor-helper-amd64"
universal_binary="$dist_dir/web-video-harbor-helper"
mkdir -p "$dist_dir"
rm -f -- "$arm_binary" "$intel_binary" "$universal_binary"

/bin/zsh "$repo_root/scripts/build-macos.zsh"
[[ -x "$arm_binary" ]] || fail "构建脚本未生成 arm64 助手"
[[ -x "$intel_binary" ]] || fail "构建脚本未生成 amd64 助手"
[[ -x "$universal_binary" ]] || fail "构建脚本未生成 universal 助手"

architecture_info="$(/usr/bin/lipo -info "$universal_binary")"
[[ "$architecture_info" == *"arm64"* && "$architecture_info" == *"x86_64"* ]] || \
  fail "universal 助手缺少 arm64 或 x86_64：$architecture_info"
version_output="$("$universal_binary" --version)"
[[ "$version_output" == "web-video-harbor-helper dev" ]] || fail "助手版本输出异常：$version_output"
rg -Fq '$("$go_command" version)' "$repo_root/scripts/build-macos.zsh" || \
  fail "构建脚本执行 Homebrew Go 时没有完整引用路径"

readme_path="$repo_root/README.md"
guide_path="$repo_root/docs/安装使用说明.md"
[[ -f "$readme_path" ]] || fail "缺少 README.md"
[[ -f "$guide_path" ]] || fail "缺少中文安装使用说明"

for required_command in \
  'brew install go ffmpeg' \
  './scripts/build-macos.zsh' \
  './scripts/start-helper.zsh' \
  './scripts/helper-status.zsh' \
  './work/dist/web-video-harbor-helper --print-token' \
  './scripts/stop-helper.zsh'; do
  rg -Fq "$required_command" "$guide_path" || fail "安装说明缺少命令：$required_command"
done

for required_topic in \
  'MP4' 'M3U8' '微信视频号' '最佳努力' 'DRM' '加密' 'Blob-only' \
  'Cookie' '授权头' '请求体' '页面正文' '127.0.0.1:17432' \
  '未连接' 'FFmpeg' '无候选' '权限' '端口占用'; do
  rg -Fq "$required_topic" "$guide_path" || fail "安装说明缺少主题：$required_topic"
done
rg -Fq '~/Downloads/WebVideoHarbor/' "$readme_path" "$guide_path" || \
  fail "文档缺少默认下载目录"
if rg -n '保证.*(所有|任何).*下载' "$readme_path" "$guide_path" >/dev/null; then
  fail "文档包含不当的万能下载承诺"
fi

fixture_root="$(mktemp -d "$repo_root/work/script-tests/lifecycle.XXXXXX")"
fixture_state="$fixture_root/work/test-state"
fixture_scripts="$fixture_root/scripts"
fixture_binary="$fixture_root/work/dist/web-video-harbor-helper"
fixture_fake_bin="$fixture_root/work/fake-bin"
fixture_sleep_pid=""

cleanup_fixture() {
  if [[ -f "$fixture_state/helper.pid" ]]; then
    cleanup_pid="$(<"$fixture_state/helper.pid")"
    if [[ "$cleanup_pid" == <-> ]] && kill -0 "$cleanup_pid" 2>/dev/null; then
      kill -TERM "$cleanup_pid" 2>/dev/null || true
    fi
  fi
  if [[ -n "$fixture_sleep_pid" ]] && kill -0 "$fixture_sleep_pid" 2>/dev/null; then
    kill -TERM "$fixture_sleep_pid" 2>/dev/null || true
  fi
}
trap cleanup_fixture EXIT

mkdir -p "$fixture_scripts" "$fixture_root/work/dist" "$fixture_fake_bin" "$fixture_state"
chmod 0700 "$fixture_state"
cp "$repo_root/scripts/helper-common.zsh" "$repo_root/scripts/start-helper.zsh" \
  "$repo_root/scripts/stop-helper.zsh" "$repo_root/scripts/helper-status.zsh" \
  "$repo_root/scripts/bounded-log.zsh" "$fixture_scripts/"

direct_log_marker="LATEST-DIRECT-LOG-MARKER"
direct_log_payload="${(l:1300000::X:)${:-}}$direct_log_marker"
if ! print -rn -- "$direct_log_payload" | env WEB_VIDEO_HELPER_TESTING=1 \
  WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
  /bin/zsh "$fixture_scripts/bounded-log.zsh" "$fixture_state/helper.log"; then
  fail "bounded logger 无法接收无换行长记录"
fi
[[ -f "$fixture_state/helper.log" ]] || fail "bounded logger 未创建日志"
direct_log_size="$(stat -f '%z' "$fixture_state/helper.log")"
(( direct_log_size <= 1048576 )) || fail "bounded logger 运行后超过 1MiB：$direct_log_size"
rg -Fq "$direct_log_marker" "$fixture_state/helper.log" || fail "bounded logger 未保留近期诊断标记"
[[ "$(stat -f '%Lp' "$fixture_state/helper.log")" == "600" ]] || fail "bounded logger 日志权限不是 0600"

exact_log_marker="EXACT-LIMIT-MARKER"
integer exact_padding=$(( 1048576 - ${#exact_log_marker} ))
exact_log_payload="${(l:exact_padding::E:)${:-}}$exact_log_marker"
print -rn -- "$exact_log_payload" | env WEB_VIDEO_HELPER_TESTING=1 \
  WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
  /bin/zsh "$fixture_scripts/bounded-log.zsh" "$fixture_state/helper.log"
[[ "$(stat -f '%z' "$fixture_state/helper.log")" == "1048576" ]] || fail "恰好 1MiB 的日志在 EOF 时被错误截断"
rg -Fq "$exact_log_marker" "$fixture_state/helper.log" || fail "恰好达到上限时丢失尾部标记"

if case_enabled bounded_signal_exit; then
  signal_logger_marker="$fixture_root/work/signal-logger.pid"
  (
    /bin/sleep 3
    print -r -- delayed
  ) | env WEB_VIDEO_HELPER_TESTING=1 \
    WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
    WEB_VIDEO_HELPER_TEST_LOGGER_PID_PATH="$signal_logger_marker" \
    /bin/zsh "$fixture_scripts/bounded-log.zsh" "$fixture_state/helper.log" &
  signal_pipeline_pid=$!
  for attempt in {1..100}; do
    [[ -f "$signal_logger_marker" ]] && break
    /bin/sleep 0.02
  done
  [[ -f "$signal_logger_marker" ]] || fail "未观察到 signal bounded logger"
  signal_logger_pid="$(<"$signal_logger_marker")"
  kill -TERM "$signal_logger_pid"
  for attempt in {1..50}; do
    ! kill -0 "$signal_logger_pid" 2>/dev/null && break
    /bin/sleep 0.02
  done
  if kill -0 "$signal_logger_pid" 2>/dev/null; then
    kill -KILL "$signal_logger_pid" 2>/dev/null || true
    fail "bounded logger 收到 TERM 后 cleanup 但未退出"
  fi
  [[ ! -e "$signal_logger_marker" ]] || fail "bounded logger TERM 后 marker 残留"
  wait "$signal_pipeline_pid" 2>/dev/null || true
  finish_focused_case bounded_signal_exit
fi

write_failure_state="$fixture_root/work/write-failure-state"
write_failure_ready="$fixture_root/work/write-failure-ready"
write_failure_result="$fixture_root/work/write-failure-result"
mkdir -p "$write_failure_state"
chmod 0700 "$write_failure_state"
(
  set +e
  setopt pipefail
  {
    print -rn -- "${(l:1048576::F:)${:-}}"
    print -r -- ready >"$write_failure_ready"
    /bin/sleep 1
    print -rn -- "${(l:1600000::D:)${:-}}"
  } | env WEB_VIDEO_HELPER_TESTING=1 \
    WEB_VIDEO_HELPER_TEST_STATE_DIR="$write_failure_state" \
    /bin/zsh "$fixture_scripts/bounded-log.zsh" "$write_failure_state/helper.log"
  print -r -- "$?" >"$write_failure_result"
) &
write_failure_pipeline_pid=$!
for attempt in {1..100}; do
  if [[ -f "$write_failure_ready" && -f "$write_failure_state/helper.log" ]] && \
    [[ "$(stat -f '%z' "$write_failure_state/helper.log")" == "1048576" ]]; then
    break
  fi
  /bin/sleep 0.02
done
[[ -f "$write_failure_ready" ]] || fail "日志写入失败测试的生产者未就绪"
[[ "$(stat -f '%z' "$write_failure_state/helper.log")" == "1048576" ]] || \
  fail "日志写入失败测试未先达到轮转上限"
chmod 0500 "$write_failure_state"
wait "$write_failure_pipeline_pid" || true
chmod 0700 "$write_failure_state"
[[ -f "$write_failure_result" ]] || fail "日志写入失败测试没有返回结果"
[[ "$(<"$write_failure_result")" == "0" ]] || \
  fail "日志存储失败后 logger 未继续读取到 EOF"

missing_state="$fixture_root/work/missing-state"
if env WEB_VIDEO_HELPER_TESTING=1 WEB_VIDEO_HELPER_TEST_STATE_DIR="$missing_state" \
  /bin/zsh "$fixture_scripts/helper-status.zsh" >/dev/null 2>&1; then
  fail "不存在状态目录时 status 应报告未运行"
fi
env WEB_VIDEO_HELPER_TESTING=1 WEB_VIDEO_HELPER_TEST_STATE_DIR="$missing_state" \
  /bin/zsh "$fixture_scripts/stop-helper.zsh" >/dev/null 2>&1
[[ -d "$missing_state" && ! -L "$missing_state" ]] || \
  fail "status/stop 没有为首次生命周期互斥创建安全状态目录"
[[ "$(stat -f '%Lp' "$missing_state")" == "700" ]] || \
  fail "status/stop 创建的首次状态目录权限不是 0700"
[[ ! -e "$missing_state/helper.lifecycle.lock" ]] || \
  fail "status/stop 首次状态检查后生命周期锁残留"

symlink_script_target="$fixture_root/work/state-link-target"
symlink_script_state="$fixture_root/work/state-link"
mkdir -p "$symlink_script_target"
print -r -- untouched >"$symlink_script_target/sentinel"
ln -s "$symlink_script_target" "$symlink_script_state"
for readonly_script in helper-status.zsh stop-helper.zsh; do
  if env WEB_VIDEO_HELPER_TESTING=1 WEB_VIDEO_HELPER_TEST_STATE_DIR="$symlink_script_state" \
    /bin/zsh "$fixture_scripts/$readonly_script" >/dev/null 2>&1; then
    fail "$readonly_script 接受了符号链接状态目录"
  fi
done
[[ "$(<"$symlink_script_target/sentinel")" == untouched ]] || fail "符号链接状态目标被改动"

missing_output="$fixture_root/work/missing-binary.txt"
if env WEB_VIDEO_HELPER_TESTING=1 WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
  /bin/zsh "$fixture_scripts/start-helper.zsh" >"$missing_output" 2>&1; then
  fail "缺少助手二进制时启动脚本仍然成功"
fi
rg -q '先.*build-macos\.zsh' "$missing_output" || fail "缺少二进制提示不清楚"

print -r -- '#!/bin/zsh
print -r -- '\''{"ready":true,"version":"test-version","ffmpeg":true}'\''' \
  >"$fixture_fake_bin/curl"
chmod 0755 "$fixture_fake_bin/curl"

print -r -- '#!/bin/zsh
print -r -- "/bin/sleep 30"' >"$fixture_fake_bin/ps"
chmod 0755 "$fixture_fake_bin/ps"
print -r -- '#!/bin/zsh
print -r -- "p0\nftxt\nn/bin/sleep\nftxt\nn/usr/lib/dyld"' >"$fixture_fake_bin/lsof"
chmod 0755 "$fixture_fake_bin/lsof"
export WEB_VIDEO_HELPER_TEST_LSOF_COMMAND="$fixture_fake_bin/lsof"

preexisting_launch_marker="$fixture_root/work/preexisting-helper-launched"
print -r -- "#!/bin/zsh
print -r -- launched >\"$preexisting_launch_marker\"
trap \"exit 0\" TERM INT
while true; do /bin/sleep 1; done" >"$fixture_binary"
chmod 0755 "$fixture_binary"
preexisting_output="$fixture_root/work/preexisting-health.txt"
if env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
  WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
  WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
  /bin/zsh "$fixture_scripts/start-helper.zsh" >"$preexisting_output" 2>&1; then
  fail "目标健康端点已有实例时启动脚本仍然成功"
fi
[[ ! -e "$preexisting_launch_marker" ]] || fail "检测到已有健康实例后仍启动了新 helper"
rg -q '健康端点已有实例' "$preexisting_output" || fail "已有健康实例提示不清楚"

print -r -- "#!/bin/zsh
[[ -f \"$fixture_root/work/health-fails\" ]] && exit 1
if [[ ! -f \"$fixture_state/helper.pid\" ]]; then exit 22; fi
health_pid=\"\$(<\"$fixture_state/helper.pid\")\"
if [[ -f \"$fixture_root/work/health-mismatch\" ]]; then (( health_pid += 1 )); fi
print -r -- \"{\\\"ready\\\":true,\\\"version\\\":\\\"test-version\\\",\\\"ffmpeg\\\":true,\\\"pid\\\":\$health_pid}\"" \
  >"$fixture_fake_bin/curl"
chmod 0755 "$fixture_fake_bin/curl"

print -r -- '#!/bin/zsh
/bin/sleep 0.05
exit 1' >"$fixture_binary"
chmod 0755 "$fixture_binary"
early_exit_output="$fixture_root/work/early-exit.txt"
if env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
  WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
  WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
  /bin/zsh "$fixture_scripts/start-helper.zsh" >"$early_exit_output" 2>&1; then
  fail "助手进程提前退出时误把其他健康响应当作启动成功"
fi
[[ ! -e "$fixture_state/helper.pid" ]] || fail "提前退出后仍保留 PID 文件"

print -r -- '#!/bin/zsh
print -r -- "$$" >>"'"$fixture_root/work/helper-launches"'"
print -r -- "$PPID" >"'"$fixture_root/work/helper-parent-"'$$"
trap "exit 0" TERM INT
/bin/sleep 0.3
bulk_output="${(l:1600000::L:)${:-}}"
print -rn -- "$bulk_output"
print -r -- "LATEST-RUNTIME-LOG-MARKER"
while true; do
  /bin/sleep 1
done' >"$fixture_binary"
chmod 0755 "$fixture_binary"

print -r -- "#!/bin/zsh
candidate_pid=\"\"
requested_field=\"\"
while (( \$# > 0 )); do
  if [[ \"\$1\" == \"-p\" ]]; then
    shift
    candidate_pid=\"\$1\"
  fi
  if [[ \"\$1\" == \"-o\" ]]; then
    shift
    requested_field=\"\$1\"
  fi
  shift
done
if [[ \"\$requested_field\" == \"ppid=\" ]]; then
  [[ -f \"$fixture_root/work/helper-parent-\$candidate_pid\" ]] || exit 1
  print -r -- \"\$(<\"$fixture_root/work/helper-parent-\$candidate_pid\")\"
elif [[ -f \"$fixture_root/work/allow-helper-identity\" || -f \"$fixture_root/work/ps-claims-helper\" ]]; then
  if [[ -f \"$fixture_root/work/wrong-config\" ]]; then
    print -r -- \"$fixture_binary --config $fixture_state/other.json\"
  else
    print -r -- \"$fixture_binary --config $fixture_state/config.json\"
  fi
else
  print -r -- \"/bin/sleep 30\"
fi" >"$fixture_fake_bin/ps"
chmod 0755 "$fixture_fake_bin/ps"

print -r -- "#!/bin/zsh
[[ -f \"$fixture_root/work/lsof-fails\" ]] && exit 1
candidate_pid=\"\"
while (( \$# > 0 )); do
  if [[ \"\$1\" == \"-p\" ]]; then shift; candidate_pid=\"\$1\"; fi
  shift
done
print -r -- \"p\$candidate_pid\"
print -r -- ftxt
if [[ -f \"$fixture_root/work/allow-helper-identity\" ]]; then
  print -r -- \"n$fixture_binary\"
else
  print -r -- n/bin/sleep
fi
print -r -- ftxt
print -r -- n/usr/lib/dyld" >"$fixture_fake_bin/lsof"
chmod 0755 "$fixture_fake_bin/lsof"

sentinel_token="DO-NOT-PRINT-THIS-TOKEN"
fixture_download="$fixture_root/work/downloads"
mkdir -p "$fixture_download"
print -r -- "{\"address\":\"127.0.0.1:17432\",\"token\":\"$sentinel_token\",\"download_dir\":\"$fixture_download\"}" \
  >"$fixture_state/config.json"
chmod 0600 "$fixture_state/config.json"
dd if=/dev/zero of="$fixture_state/helper.log" bs=1024 count=1100 2>/dev/null
print -r -- 'allowed' >"$fixture_root/work/allow-helper-identity"
export WEB_VIDEO_HELPER_TEST_LOGGER_PID_PATH="$fixture_root/work/logger.pid"

lifecycle_lock_path="$fixture_state/helper.lifecycle.lock"
if case_enabled first_lifecycle_interleave; then
for first_command in stop-helper.zsh helper-status.zsh; do
  first_barrier="$fixture_root/work/first-${first_command:r}-barrier"
  first_output="$fixture_root/work/first-${first_command:r}.txt"
  first_start_output="$fixture_root/work/first-${first_command:r}-start.txt"
  rm -f -- "$fixture_root/work/helper-launches" "$fixture_root/work/logger.pid" \
    "$first_barrier.ready" "$first_barrier.release"
  rm -f -- "$fixture_state/config.json" "$fixture_state/helper.pid" "$fixture_state/helper.log" \
    "$fixture_state/helper.lifecycle.lock"
  rmdir "$fixture_state"

  env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
    WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
    WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
    WEB_VIDEO_HELPER_TEST_BARRIER_STAGE=after-lock \
    WEB_VIDEO_HELPER_TEST_BARRIER_PATH="$first_barrier" \
    /bin/zsh "$fixture_scripts/$first_command" >"$first_output" 2>&1 &
  first_command_pid=$!
  for attempt in {1..100}; do
    [[ -f "$first_barrier.ready" ]] && break
    /bin/sleep 0.02
  done
  [[ -f "$first_barrier.ready" ]] || \
    fail "$first_command 在首次状态目录不存在时没有进入生命周期锁"

  env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
    WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
    WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
    /bin/zsh "$fixture_scripts/start-helper.zsh" >"$first_start_output" 2>&1 &
  first_start_pid=$!
  /bin/sleep 0.2
  kill -0 "$first_start_pid" 2>/dev/null || \
    fail "首次 start 没有等待 $first_command 持有的生命周期锁"

  print -r -- release >"$first_barrier.release"
  set +e
  wait "$first_command_pid"
  first_command_status=$?
  wait "$first_start_pid"
  first_start_status=$?
  set -e
  if [[ "$first_command" == "stop-helper.zsh" ]]; then
    [[ "$first_command_status" == "0" ]] || fail "首次 stop 在锁内检查时错误失败"
  else
    [[ "$first_command_status" != "0" ]] || fail "首次 status 在锁内检查时错误报告运行中"
  fi
  [[ "$first_start_status" == "0" ]] || fail "首次 start 未在 $first_command 后成功完成"
  [[ -f "$fixture_state/helper.pid" ]] || fail "首次交错后 helper PID 不可管理"
  env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
    WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
    WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
    /bin/zsh "$fixture_scripts/stop-helper.zsh" >/dev/null
done
print -r -- "{\"address\":\"127.0.0.1:17432\",\"token\":\"$sentinel_token\",\"download_dir\":\"$fixture_download\"}" \
  >"$fixture_state/config.json"
chmod 0600 "$fixture_state/config.json"
finish_focused_case first_lifecycle_interleave
fi

if case_enabled launch_signal_window; then
launch_signal_output="$fixture_root/work/launch-signal.txt"
rm -f -- "$fixture_root/work/helper-launches" "$fixture_root/work/logger.pid" \
  "$fixture_state/helper.pid" "$fixture_state/helper.lifecycle.lock"
if env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
  WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
  WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
  WEB_VIDEO_HELPER_TEST_FAIL_STAGE=launch-signal \
  /bin/zsh "$fixture_scripts/start-helper.zsh" >"$launch_signal_output" 2>&1; then
  fail "launch/PID 捕获临界区收到 TERM 后 start 仍报告成功"
fi
[[ -f "$fixture_root/work/helper-launches" ]] || fail "launch 信号用例没有真正创建 helper"
while IFS= read -r launch_signal_helper_pid; do
  for attempt in {1..100}; do
    ! kill -0 "$launch_signal_helper_pid" 2>/dev/null && break
    /bin/sleep 0.02
  done
  kill -0 "$launch_signal_helper_pid" 2>/dev/null && \
    fail "launch/PID 捕获临界区收到 TERM 后遗留无 PID helper"
done <"$fixture_root/work/helper-launches"
for attempt in {1..100}; do
  [[ ! -e "$fixture_root/work/logger.pid" ]] && break
  /bin/sleep 0.02
done
[[ ! -e "$fixture_state/helper.pid" ]] || fail "launch 信号回滚后 PID 文件残留"
[[ ! -e "$fixture_state/helper.lifecycle.lock" ]] || fail "launch 信号回滚后生命周期锁残留"
[[ ! -e "$fixture_root/work/logger.pid" ]] || fail "launch 信号回滚后 logger 残留"
finish_focused_case launch_signal_window
fi

if case_enabled stale_lock; then
dead_lock_pid=""
/bin/sleep 0.01 &
dead_lock_pid=$!
wait "$dead_lock_pid"
print -r -- "$dead_lock_pid" >"$lifecycle_lock_path"
chmod 0600 "$lifecycle_lock_path"
/bin/sleep 2
stale_lock_output="$fixture_root/work/stale-lock.txt"
if env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
  WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
  WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
  /bin/zsh "$fixture_scripts/helper-status.zsh" >"$stale_lock_output" 2>&1; then
  fail "没有 PID 时 status 应报告未运行"
fi
[[ ! -e "$lifecycle_lock_path" ]] || fail "status 未回收并释放 dead stale 生命周期锁"
finish_focused_case stale_lock
fi

if case_enabled unsafe_lock; then
lock_symlink_target="$fixture_root/work/lock-symlink-target"
print -r -- untouched >"$lock_symlink_target"
ln -s "$lock_symlink_target" "$lifecycle_lock_path"
unsafe_lock_output="$fixture_root/work/unsafe-lock.txt"
if env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
  WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
  WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
  /bin/zsh "$fixture_scripts/helper-status.zsh" >"$unsafe_lock_output" 2>&1; then
  fail "status 接受了符号链接生命周期锁"
fi
rg -q '生命周期锁.*不安全' "$unsafe_lock_output" || fail "生命周期锁符号链接提示不清楚"
[[ -L "$lifecycle_lock_path" ]] || fail "status 改动了符号链接生命周期锁"
[[ "$(<"$lock_symlink_target")" == untouched ]] || fail "status 改动了生命周期锁链接目标"
rm -f -- "$lifecycle_lock_path"
print -r -- "$$" >"$lifecycle_lock_path"
chmod 0644 "$lifecycle_lock_path"
if env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
  WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
  WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
  /bin/zsh "$fixture_scripts/helper-status.zsh" >"$unsafe_lock_output" 2>&1; then
  fail "status 接受了权限不安全的生命周期锁"
fi
[[ "$(stat -f '%Lp' "$lifecycle_lock_path")" == "644" ]] || fail "status 改动了不安全锁权限"
rm -f -- "$lifecycle_lock_path"
finish_focused_case unsafe_lock
fi

if case_enabled concurrent_start; then
concurrent_barrier="$fixture_root/work/concurrent-start-barrier"
concurrent_start_one="$fixture_root/work/concurrent-start-one.txt"
concurrent_start_two="$fixture_root/work/concurrent-start-two.txt"
rm -f -- "$fixture_root/work/helper-launches" "$fixture_root/work/logger.pid" \
  "$concurrent_barrier.ready" "$concurrent_barrier.release"
env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
  WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
  WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
  WEB_VIDEO_HELPER_TEST_BARRIER_STAGE=after-lock \
  WEB_VIDEO_HELPER_TEST_BARRIER_PATH="$concurrent_barrier" \
  /bin/zsh "$fixture_scripts/start-helper.zsh" >"$concurrent_start_one" 2>&1 &
concurrent_start_one_pid=$!
for attempt in {1..100}; do
  [[ -f "$concurrent_barrier.ready" ]] && break
  /bin/sleep 0.02
done
[[ -f "$concurrent_barrier.ready" ]] || fail "第一个 start 未到达持锁屏障"
env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
  WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
  WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
  /bin/zsh "$fixture_scripts/start-helper.zsh" >"$concurrent_start_two" 2>&1 &
concurrent_start_two_pid=$!
/bin/sleep 0.2
kill -0 "$concurrent_start_two_pid" 2>/dev/null || fail "第二个 start 未等待生命周期锁"
print -r -- release >"$concurrent_barrier.release"
set +e
wait "$concurrent_start_one_pid"
concurrent_start_one_status=$?
wait "$concurrent_start_two_pid"
concurrent_start_two_status=$?
set -e
[[ "$concurrent_start_one_status" == "0" && "$concurrent_start_two_status" != "0" ]] || \
  fail "两个并发 start 没有串行化为一成一败"
[[ "$(wc -l <"$fixture_root/work/helper-launches" | tr -d ' ')" == "1" ]] || \
  fail "两个并发 start 启动了多个 helper"
concurrent_helper_pid="$(<"$fixture_state/helper.pid")"
kill -0 "$concurrent_helper_pid" 2>/dev/null || fail "并发 start 后 PID 不可管理"
env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
  WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
  WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
  /bin/zsh "$fixture_scripts/stop-helper.zsh" >/dev/null
finish_focused_case concurrent_start
fi

if case_enabled lifecycle_signal_release; then
signal_barrier="$fixture_root/work/signal-start-barrier"
signal_start_output="$fixture_root/work/signal-start.txt"
rm -f -- "$fixture_root/work/helper-launches" "$signal_barrier.ready" "$signal_barrier.release"
env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
  WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
  WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
  WEB_VIDEO_HELPER_TEST_BARRIER_STAGE=after-lock \
  WEB_VIDEO_HELPER_TEST_BARRIER_PATH="$signal_barrier" \
  /bin/zsh "$fixture_scripts/start-helper.zsh" >"$signal_start_output" 2>&1 &
signal_start_pid=$!
for attempt in {1..100}; do
  [[ -f "$signal_barrier.ready" ]] && break
  /bin/sleep 0.02
done
[[ -f "$signal_barrier.ready" ]] || fail "signal start 未持有生命周期锁"
[[ "$(<"$signal_barrier.ready")" == "$signal_start_pid" ]] || fail "signal start 锁 owner PID 不匹配"
kill -TERM "$signal_start_pid"
set +e
wait "$signal_start_pid"
signal_start_status=$?
set -e
[[ "$signal_start_status" != "0" ]] || fail "start 收到 TERM 后错误报告成功"
[[ ! -e "$lifecycle_lock_path" ]] || fail "start 收到 TERM 后生命周期锁残留"
[[ ! -e "$fixture_state/helper.pid" ]] || fail "start 收到 TERM 后 PID 文件残留"
[[ ! -e "$fixture_root/work/helper-launches" ]] || fail "after-lock TERM 前不应启动 helper"
finish_focused_case lifecycle_signal_release
fi

if case_enabled start_stop_interleave; then
interleave_barrier="$fixture_root/work/start-stop-barrier"
interleave_start_output="$fixture_root/work/interleave-start.txt"
interleave_stop_output="$fixture_root/work/interleave-stop.txt"
rm -f -- "$fixture_root/work/helper-launches" "$fixture_root/work/logger.pid" \
  "$interleave_barrier.ready" "$interleave_barrier.release"
env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
  WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
  WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
  WEB_VIDEO_HELPER_TEST_BARRIER_STAGE=after-launch \
  WEB_VIDEO_HELPER_TEST_BARRIER_PATH="$interleave_barrier" \
  /bin/zsh "$fixture_scripts/start-helper.zsh" >"$interleave_start_output" 2>&1 &
interleave_start_pid=$!
for attempt in {1..100}; do
  [[ -f "$interleave_barrier.ready" ]] && break
  /bin/sleep 0.02
done
[[ -f "$interleave_barrier.ready" ]] || fail "start 未到达 launch 后持锁屏障"
env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
  WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
  WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
  /bin/zsh "$fixture_scripts/stop-helper.zsh" >"$interleave_stop_output" 2>&1 &
interleave_stop_pid=$!
/bin/sleep 0.2
kill -0 "$interleave_stop_pid" 2>/dev/null || fail "stop 未等待 start 持有的生命周期锁"
print -r -- release >"$interleave_barrier.release"
wait "$interleave_start_pid" || fail "start/stop 交错时 start 未先完成"
wait "$interleave_stop_pid" || fail "start/stop 交错时 stop 未随后完成"
interleaved_helper_pid="$(tail -n 1 "$fixture_root/work/helper-launches")"
kill -0 "$interleaved_helper_pid" 2>/dev/null && fail "start/stop 交错后 helper 残留"
[[ ! -e "$fixture_state/helper.pid" ]] || fail "start/stop 交错后 PID 文件残留"
[[ ! -e "$lifecycle_lock_path" ]] || fail "start/stop 交错后生命周期锁残留"
finish_focused_case start_stop_interleave
fi

for failure_stage in temp-create pid-write pid-chmod pid-publish; do
  case "$failure_stage" in
    temp-create) failure_case="pid_temp_create_failure" ;;
    pid-write) failure_case="pid_write_failure" ;;
    pid-chmod) failure_case="pid_chmod_failure" ;;
    pid-publish) failure_case="pid_publish_failure" ;;
  esac
  case_enabled "$failure_case" || continue
  rm -f -- "$fixture_root/work/helper-launches" "$fixture_root/work/logger.pid" \
    "$fixture_state/helper.pid"
  failure_output="$fixture_root/work/failure-$failure_stage.txt"
  if env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
    WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
    WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
    WEB_VIDEO_HELPER_TEST_FAIL_STAGE="$failure_stage" \
    /bin/zsh "$fixture_scripts/start-helper.zsh" >"$failure_output" 2>&1; then
    fail "$failure_stage 故障注入时 start 仍报告成功"
  fi
  [[ ! -e "$fixture_state/helper.pid" ]] || fail "$failure_stage 故障后 PID 文件残留"
  if [[ -f "$fixture_root/work/helper-launches" ]]; then
    while IFS= read -r failed_helper_pid; do
      kill -0 "$failed_helper_pid" 2>/dev/null && fail "$failure_stage 故障后 helper 残留"
    done <"$fixture_root/work/helper-launches"
  fi
  for attempt in {1..100}; do
    [[ ! -e "$fixture_root/work/logger.pid" ]] && break
    /bin/sleep 0.02
  done
  [[ ! -e "$fixture_root/work/logger.pid" ]] || fail "$failure_stage 故障后 logger 残留"
  [[ ! -e "$lifecycle_lock_path" ]] || fail "$failure_stage 故障后生命周期锁残留"
  finish_focused_case "$failure_case"
done

if [[ "$focused_case" != "all" ]]; then
  fail "未知或未执行的聚焦用例：$focused_case"
fi

start_output="$fixture_root/work/start.txt"
env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
  WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
  WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
  /bin/zsh "$fixture_scripts/start-helper.zsh" >"$start_output" 2>&1
rg -q '启动成功' "$start_output" || fail "启动脚本未确认健康状态"
if rg -q 'nice\(' "$start_output"; then
  fail "启动脚本触发了后台 nice 警告"
fi
[[ -f "$fixture_state/helper.pid" ]] || fail "启动脚本未写入 PID 文件"
helper_pid="$(<"$fixture_state/helper.pid")"
[[ "$helper_pid" == <-> ]] || fail "PID 文件内容无效"
kill -0 "$helper_pid" 2>/dev/null || fail "助手进程未运行"
[[ "$(stat -f '%Lp' "$fixture_state")" == "700" ]] || fail "状态目录权限不是 0700"
[[ "$(stat -f '%Lp' "$fixture_state/helper.pid")" == "600" ]] || fail "PID 文件权限不是 0600"
[[ "$(stat -f '%Lp' "$fixture_state/helper.log")" == "600" ]] || fail "日志权限不是 0600"
runtime_marker_seen=""
integer observed_log_max=0
for attempt in {1..100}; do
  log_size="$(stat -f '%z' "$fixture_state/helper.log")"
  (( log_size > observed_log_max )) && observed_log_max=$log_size
  (( log_size <= 1048576 )) || fail "助手运行期间日志超过 1MiB：$log_size"
  if rg -Fq 'LATEST-RUNTIME-LOG-MARKER' "$fixture_state/helper.log"; then
    runtime_marker_seen="1"
    break
  fi
  /bin/sleep 0.05
done
[[ -n "$runtime_marker_seen" ]] || fail "运行期 bounded logger 未保留近期标记"
[[ -f "$fixture_root/work/logger.pid" ]] || fail "未观察到 bounded logger 进程"
logger_pid="$(<"$fixture_root/work/logger.pid")"
[[ "$logger_pid" == <-> ]] || fail "bounded logger PID 无效"
kill -0 "$logger_pid" 2>/dev/null || fail "bounded logger 在 helper 运行时提前退出"

duplicate_output="$fixture_root/work/duplicate.txt"
if env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
  WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
  WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
  /bin/zsh "$fixture_scripts/start-helper.zsh" >"$duplicate_output" 2>&1; then
  fail "重复启动未被拒绝"
fi
rg -q '已经运行' "$duplicate_output" || fail "重复启动提示不清楚"

status_output="$fixture_root/work/status.txt"
env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
  WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
  WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
  /bin/zsh "$fixture_scripts/helper-status.zsh" >"$status_output" 2>&1
rg -q '助手状态：健康' "$status_output" || fail "状态脚本未报告健康"
rg -q 'FFmpeg：可用' "$status_output" || fail "状态脚本未报告 FFmpeg"
rg -Fq "下载目录：$fixture_download" "$status_output" || fail "状态脚本未报告下载目录"
if rg -Fq "$sentinel_token" "$status_output"; then
  fail "状态脚本泄露了配对密钥"
fi

for identity_failure in wrong-config lsof-fails; do
  print -r -- fail >"$fixture_root/work/$identity_failure"
  identity_output="$fixture_root/work/status-$identity_failure.txt"
  if env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
    WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
    WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
    /bin/zsh "$fixture_scripts/helper-status.zsh" >"$identity_output" 2>&1; then
    fail "状态脚本在 $identity_failure 时未 fail closed"
  fi
  rg -q '身份不匹配' "$identity_output" || fail "$identity_failure 的身份失败提示不清楚"
  rm -f -- "$fixture_root/work/$identity_failure"
done

print -r -- mismatch >"$fixture_root/work/health-mismatch"
mismatch_status_output="$fixture_root/work/status-health-mismatch.txt"
if env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
  WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
  WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
  /bin/zsh "$fixture_scripts/helper-status.zsh" >"$mismatch_status_output" 2>&1; then
  fail "状态脚本接受了属于其他 PID 的健康响应"
fi
rg -q '健康端点不属于该实例' "$mismatch_status_output" || fail "健康 PID 不匹配提示不清楚"
rm -f -- "$fixture_root/work/health-mismatch"

stop_output="$fixture_root/work/stop.txt"
env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
  WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
  WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
  /bin/zsh "$fixture_scripts/stop-helper.zsh" >"$stop_output" 2>&1
rg -q '已停止' "$stop_output" || fail "停止脚本未确认结束"
[[ ! -e "$fixture_state/helper.pid" ]] || fail "停止后仍保留 PID 文件"
if kill -0 "$helper_pid" 2>/dev/null; then
  fail "停止脚本未结束助手进程"
fi
for attempt in {1..50}; do
  if ! kill -0 "$logger_pid" 2>/dev/null && [[ ! -e "$fixture_root/work/logger.pid" ]]; then
    break
  fi
  /bin/sleep 0.05
done
if kill -0 "$logger_pid" 2>/dev/null || [[ -e "$fixture_root/work/logger.pid" ]]; then
  fail "停止 helper 后 bounded logger 仍有残留"
fi

print -r -- fail >"$fixture_root/work/health-fails"
print -r -- fail >"$fixture_root/work/lsof-fails"
unverified_start_output="$fixture_root/work/unverified-start.txt"
if env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
  WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
  WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
  /bin/zsh "$fixture_scripts/start-helper.zsh" >"$unverified_start_output" 2>&1; then
  fail "身份检查持续失败时启动脚本仍报告成功"
fi
[[ -f "$fixture_state/helper.pid" ]] || fail "无法确认新进程身份时删除了唯一 PID 记录"
unverified_helper_pid="$(<"$fixture_state/helper.pid")"
[[ "$unverified_helper_pid" == <-> ]] || fail "身份未确认路径没有保留有效 PID"
kill -0 "$unverified_helper_pid" 2>/dev/null || fail "身份未确认路径未覆盖存活进程"
rg -q '无法安全清理.*PID 文件已保留' "$unverified_start_output" || \
  fail "身份未确认的启动失败提示不清楚"
rm -f -- "$fixture_root/work/health-fails" "$fixture_root/work/lsof-fails"
env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
  WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
  WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
  /bin/zsh "$fixture_scripts/stop-helper.zsh" >/dev/null

/bin/sleep 30 &
fixture_sleep_pid=$!
rm -f -- "$fixture_root/work/allow-helper-identity"
print -r -- claims >"$fixture_root/work/ps-claims-helper"
print -r -- "$fixture_sleep_pid" >"$fixture_state/helper.pid"
chmod 0600 "$fixture_state/helper.pid"
wrong_process_output="$fixture_root/work/wrong-process.txt"
if env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
  WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
  WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
  /bin/zsh "$fixture_scripts/stop-helper.zsh" >"$wrong_process_output" 2>&1; then
  fail "停止脚本接受了无关 PID"
fi
rg -q '身份不匹配' "$wrong_process_output" || fail "无关 PID 提示不清楚"
kill -0 "$fixture_sleep_pid" 2>/dev/null || fail "停止脚本误杀了无关进程"
kill -TERM "$fixture_sleep_pid"
wait "$fixture_sleep_pid" 2>/dev/null || true
fixture_sleep_pid=""
rm -f -- "$fixture_root/work/ps-claims-helper"

pid_symlink_target="$fixture_root/work/pid-symlink-target"
pid_symlink_path="$fixture_state/helper.pid"
print -r -- 'unchanged' >"$pid_symlink_target"
rm -f -- "$pid_symlink_path"
ln -s "$pid_symlink_target" "$pid_symlink_path"
pid_symlink_output="$fixture_root/work/pid-symlink.txt"
if env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
  WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
  WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
  /bin/zsh "$fixture_scripts/stop-helper.zsh" >"$pid_symlink_output" 2>&1; then
  fail "停止脚本接受了符号链接 PID 文件"
fi
if env PATH="$fixture_fake_bin:/usr/bin:/bin" WEB_VIDEO_HELPER_TESTING=1 \
  WEB_VIDEO_HELPER_TEST_STATE_DIR="$fixture_state" \
  WEB_VIDEO_HELPER_TEST_PS_COMMAND="$fixture_fake_bin/ps" \
  /bin/zsh "$fixture_scripts/helper-status.zsh" >/dev/null 2>&1; then
  fail "状态脚本接受了符号链接 PID 文件"
fi
[[ -L "$pid_symlink_path" ]] || fail "停止脚本改动了符号链接 PID 文件"
[[ "$(<"$pid_symlink_target")" == 'unchanged' ]] || fail "停止脚本改动了 PID 链接目标"

print -- "macOS 脚本基础检查通过"
