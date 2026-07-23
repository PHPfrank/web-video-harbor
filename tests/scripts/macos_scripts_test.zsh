#!/bin/zsh
set -euo pipefail
unsetopt BG_NICE

script_dir="${0:A:h}"
repo_root="${script_dir:h:h}"

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

expected_state_dir="${HOME:?}/Library/Application Support/网页视频下载器"
expected_download_dir="${HOME:?}/Downloads/网页视频下载器"
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

outside_test_state="${TMPDIR:-/tmp}/web-video-helper-outside-state"
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
  if [[ "$readonly_text" != *helper_validate_existing_state_dir*helper_read_pid* ]]; then
    fail "$readonly_script 未在读取 PID 前只读验证状态目录与文件"
  fi
  if [[ "$readonly_text" == *helper_prepare_state_dir* ]]; then
    fail "$readonly_script 不得在检查状态时创建或修改状态目录"
  fi
done

dist_dir="$repo_root/work/dist"
arm_binary="$dist_dir/web-video-helper-arm64"
intel_binary="$dist_dir/web-video-helper-amd64"
universal_binary="$dist_dir/web-video-helper"
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
[[ "$version_output" == "web-video-helper dev" ]] || fail "助手版本输出异常：$version_output"
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
  './work/dist/web-video-helper --print-token' \
  './scripts/stop-helper.zsh'; do
  rg -Fq "$required_command" "$guide_path" || fail "安装说明缺少命令：$required_command"
done

for required_topic in \
  'MP4' 'M3U8' '微信视频号' '最佳努力' 'DRM' '加密' 'Blob-only' \
  'Cookie' '授权头' '请求体' '页面正文' '127.0.0.1:17432' \
  '未连接' 'FFmpeg' '无候选' '权限' '端口占用'; do
  rg -Fq "$required_topic" "$guide_path" || fail "安装说明缺少主题：$required_topic"
done
rg -Fq '~/Downloads/网页视频下载器/' "$readme_path" "$guide_path" || \
  fail "文档缺少默认下载目录"
if rg -n '保证.*(所有|任何).*下载' "$readme_path" "$guide_path" >/dev/null; then
  fail "文档包含不当的万能下载承诺"
fi

fixture_root="$(mktemp -d "$repo_root/work/script-tests/lifecycle.XXXXXX")"
fixture_state="$fixture_root/work/test-state"
fixture_scripts="$fixture_root/scripts"
fixture_binary="$fixture_root/work/dist/web-video-helper"
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
[[ ! -e "$missing_state" ]] || fail "status/stop 检查时隐式创建了状态目录"

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
while (( \$# > 0 )); do
  if [[ \"\$1\" == \"-p\" ]]; then
    shift
    candidate_pid=\"\$1\"
  fi
  shift
done
expected_pid=\"\"
[[ -f \"$fixture_state/helper.pid\" ]] && expected_pid=\"\$(<\"$fixture_state/helper.pid\")\"
if [[ ( -f \"$fixture_root/work/allow-helper-identity\" || -f \"$fixture_root/work/ps-claims-helper\" ) && -n \"\$expected_pid\" && \"\$candidate_pid\" == \"\$expected_pid\" ]]; then
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
expected_pid=\"\"
[[ -f \"$fixture_state/helper.pid\" ]] && expected_pid=\"\$(<\"$fixture_state/helper.pid\")\"
print -r -- \"p\$candidate_pid\"
print -r -- ftxt
if [[ -f \"$fixture_root/work/allow-helper-identity\" && -n \"\$expected_pid\" && \"\$candidate_pid\" == \"\$expected_pid\" ]]; then
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
