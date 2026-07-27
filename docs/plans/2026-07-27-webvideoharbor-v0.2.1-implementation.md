# WebVideoHarbor v0.2.1 Network Compatibility Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 修复 Bilibili 单视频完成后误报失败，并为 YouTube 增加不读取 Cookie 的公司网络兼容回退与本地 JavaScript 运行环境。

**Architecture:** 保留现有受控 yt-dlp 运行器与私有 staging，在 Bilibili 路径移除会制造退出码 101 的冗余下载上限。YouTube 仅在明确 TLS reset 后进行一次固定 Chrome/macOS 网络特征回退；两个路径都使用相邻打包、经过快照验证的 Deno 运行 EJS，且不开放任意参数。

**Tech Stack:** Go 1.25、yt-dlp 2026.07.04、Deno、FFmpeg、zsh、Node.js `node:test`、Chrome Manifest V3。

---

### Task 1: 修复 Bilibili 完成后退出码 101

**Files:**
- Modify: `helper/internal/ytdlp/runner_test.go`
- Modify: `helper/internal/ytdlp/runner.go`
- Modify: `tests/integration/fake_ytdlp_test.go`
- Modify: `tests/integration/helper_test.go`

**Step 1: Write the failing unit test**

在参数安全测试中明确拒绝 `--max-downloads`，并继续断言 `--no-playlist`、固定 URL 与格式选择器存在：

```go
if slices.Contains(args, "--max-downloads") {
    t.Fatalf("args contain max-downloads, which makes Bilibili anthology exit 101: %q", args)
}
```

**Step 2: Run test to verify it fails**

Run: `cd helper && go test ./internal/ytdlp -run 'TestRunner.*Fixed|TestRunner.*Arguments' -count=1`

Expected: FAIL，参数中仍包含 `--max-downloads 1`。

**Step 3: Write minimal implementation**

从 `buildArgs` 删除：

```go
"--max-downloads", "1",
```

保留 `--no-playlist` 和平台 URL 规范化作为单视频边界。

**Step 4: Add integration regression**

让 fake yt-dlp 在收到 `--max-downloads` 时模拟“文件已生成但退出 101”，没有该参数时生成一个有效 staging 文件并退出 0。集成测试提交 Bilibili anthology URL，断言任务完成并发布文件。

**Step 5: Run focused tests**

Run: `cd helper && go test ./internal/ytdlp ./internal/api -count=1`

Run: `cd tests/integration && go test ./... -count=1`

Expected: PASS。

**Step 6: Commit**

```bash
git add helper/internal/ytdlp/runner.go helper/internal/ytdlp/runner_test.go tests/integration/fake_ytdlp_test.go tests/integration/helper_test.go
git commit -m "fix: complete Bilibili anthology downloads"
```

### Task 2: 让安全可执行文件快照支持解析器与运行时

**Files:**
- Modify: `helper/internal/ytdlp/snapshot.go`
- Modify: `helper/internal/ytdlp/probe_test.go`
- Modify: `helper/internal/ytdlp/probe.go`

**Step 1: Write failing tests**

新增测试分别为 `yt-dlp_macos` 与 `deno_macos_arm64`/`deno_macos_x86_64` 创建快照，断言：

- 私有快照文件名与来源用途固定；
- 替换路径、符号链接、摘要变化、关闭和 active lease 仍被拒绝；
- 两个快照使用不同私有目录，关闭互不影响。

**Step 2: Run tests to verify they fail**

Run: `cd helper && go test ./internal/ytdlp -run 'Snapshot|Probe' -count=1`

Expected: FAIL，当前实现把快照文件名硬编码为 `yt-dlp_macos`。

**Step 3: Generalize the snapshot constructor**

将构造函数改为接收内部固定用途和文件名：

```go
func createExecutableSnapshot(sourcePath, snapshotName string) (*ExecutableSnapshot, error)
```

`ExecutableSnapshot` 保存 `fileName`，所有 `Openat`/`Unlinkat`/校验使用该字段。调用者只能传包内常量，函数拒绝空值、路径分隔符、`.`、`..` 和非干净 basename。

**Step 4: Preserve parser behavior**

`probeAdjacent` 继续以 `yt-dlp_macos` 创建解析器快照，现有安全测试全部保持通过。

**Step 5: Run focused tests and commit**

Run: `cd helper && go test ./internal/ytdlp -run 'Snapshot|Probe' -count=1`

Expected: PASS。

```bash
git add helper/internal/ytdlp/snapshot.go helper/internal/ytdlp/probe.go helper/internal/ytdlp/probe_test.go
git commit -m "refactor: secure bundled executable snapshots"
```

### Task 3: 探测相邻 Deno 并接入助手生命周期

**Files:**
- Create: `helper/internal/ytdlp/runtime.go`
- Modify: `helper/internal/ytdlp/probe_test.go`
- Modify: `helper/cmd/web-video-harbor-helper/main.go`
- Modify: `helper/cmd/web-video-harbor-helper/main_test.go`
- Modify: `helper/internal/api/engine.go`
- Modify: `helper/internal/api/engine_test.go`
- Modify: `helper/internal/api/server.go`
- Modify: `helper/internal/api/server_test.go`
- Modify: `scripts/helper-status.zsh`
- Modify: `tests/scripts/macos_scripts_test.zsh`

**Step 1: Write failing runtime probe tests**

定义当前架构固定名称和严格版本输出：

```go
type RuntimeResult struct {
    Path string
    Version string
    Snapshot *ExecutableSnapshot
}
```

测试只允许相邻的 `deno_macos_arm64` 或 `deno_macos_x86_64`，运行 `--version` 后只接受第一行 `deno X.Y.Z`，并拒绝 PATH 搜索、符号链接、超长输出、错误架构名称和替换快照。

**Step 2: Verify failure**

Run: `cd helper && go test ./internal/ytdlp -run 'Runtime' -count=1`

Expected: FAIL，运行时探测尚不存在。

**Step 3: Implement `ProbeRuntime`**

根据 `runtime.GOARCH` 选择固定相邻文件，复用有界版本命令和安全快照。不支持的架构安全返回不可用。

**Step 4: Thread runtime through main and engine**

`main` 同时探测解析器与 Deno；只有二者和 FFmpeg 都可用时创建平台 runner。关闭顺序为：等待 engine 全部 worker → 关闭解析器快照 → 关闭 Deno 快照。

`api.NewEngine` 增加 `RuntimeResult` 参数，并传入 `ytdlp.Config`：

```go
RuntimePath: runtime.Path,
RuntimeSnapshot: runtime.Snapshot,
```

**Step 5: Add bounded health status**

健康响应新增：

```json
"javascriptRuntime": {"available": true, "version": "2.x.y"}
```

不得包含路径。`helper-status.zsh` 显示“JavaScript 解析环境：可用/不可用”。

**Step 6: Run tests and commit**

Run: `cd helper && go test ./cmd/web-video-harbor-helper ./internal/api ./internal/ytdlp -count=1`

Run: `zsh tests/scripts/macos_scripts_test.zsh`

Expected: PASS。

```bash
git add helper/internal/ytdlp/runtime.go helper/internal/ytdlp/probe_test.go helper/cmd/web-video-harbor-helper/main.go helper/cmd/web-video-harbor-helper/main_test.go helper/internal/api/engine.go helper/internal/api/engine_test.go helper/internal/api/server.go helper/internal/api/server_test.go scripts/helper-status.zsh tests/scripts/macos_scripts_test.zsh
git commit -m "feat: probe bundled JavaScript runtime"
```

### Task 4: 用固定 Deno 参数启动 yt-dlp

**Files:**
- Modify: `helper/internal/ytdlp/runner.go`
- Modify: `helper/internal/ytdlp/runner_test.go`

**Step 1: Write failing tests**

测试 `New` 必须同时验证 runtime 路径与快照；参数包含唯一固定值：

```go
"--js-runtimes", "deno:" + runtimeSnapshot.Path()
```

测试运行前后替换 Deno 快照会安全失败，且环境仍只有 `PATH`、`LANG`、`LC_ALL`，不包含 HOME、Cookie 或代理变量。

**Step 2: Verify failure**

Run: `cd helper && go test ./internal/ytdlp -run 'Runtime|FixedBinaryMinimalEnvironment' -count=1`

Expected: FAIL，Config 和参数尚未包含 runtime。

**Step 3: Implement minimal runtime validation**

扩展 `Config`/`Runner`，对解析器与运行时各自 acquire/verify/release。任一快照在 Start 前后变化时终止整个进程组并返回 `javascript_runtime` 或安全进程错误。

**Step 4: Run tests and commit**

Run: `cd helper && go test ./internal/ytdlp -count=1`

Expected: PASS。

```bash
git add helper/internal/ytdlp/runner.go helper/internal/ytdlp/runner_test.go
git commit -m "feat: run YouTube challenges with bundled Deno"
```

### Task 5: 实现 YouTube Chrome 兼容回退

**Files:**
- Modify: `helper/internal/ytdlp/runner.go`
- Modify: `helper/internal/ytdlp/runner_test.go`
- Modify: `tests/integration/fake_ytdlp_test.go`
- Modify: `tests/integration/helper_test.go`

**Step 1: Write table-driven failing tests**

覆盖：

- YouTube 默认尝试 `ConnectionResetError(54)` 后第二次参数包含 `--impersonate Chrome-136:Macos-15`；
- Bilibili 相同错误只执行一次；
- 登录、地区、会员、取消、FFmpeg、输出和 extractor 错误不回退；
- 最多两次，第二次失败不再切换；
- 两次尝试使用不同 staging，第一次生成的文件被清理；
- 第二次成功只发布一个最终文件。

**Step 2: Verify failure**

Run: `cd helper && go test ./internal/ytdlp -run 'Fallback|Impersonat' -count=1`

Expected: FAIL，当前 Runner 只执行一次。

**Step 3: Extract one-attempt execution**

把当前 `Run` 中“创建 staging → 启动进程 → 验证文件 → 发布”的内部阶段拆成一次尝试函数，但最终发布仍只由外层调用。内部结果带受控分类：

```go
type attemptMode uint8
const (
    attemptDefault attemptMode = iota
    attemptChromeMac
)
```

**Step 4: Add the single allowed fallback**

外层仅在 `platformurl.Classify` 为 YouTube 且第一次诊断明确包含 TLS/curl connection reset 时执行 `attemptChromeMac`。兼容参数是包内常量，不接受 Request 字段。

**Step 5: Run unit and integration tests**

Run: `cd helper && go test ./internal/ytdlp ./internal/api -count=1`

Run: `cd tests/integration && go test ./... -count=1`

Expected: PASS。

**Step 6: Commit**

```bash
git add helper/internal/ytdlp/runner.go helper/internal/ytdlp/runner_test.go tests/integration/fake_ytdlp_test.go tests/integration/helper_test.go
git commit -m "feat: retry filtered YouTube requests as Chrome"
```

### Task 6: 细分平台错误并更新扩展提示

**Files:**
- Modify: `helper/internal/ytdlp/runner.go`
- Modify: `helper/internal/ytdlp/runner_test.go`
- Modify: `helper/internal/api/engine.go`
- Modify: `helper/internal/api/engine_test.go`
- Modify: `extension/lib/helper-client.js`
- Modify: `extension/tests/helper-client.test.js`
- Modify: `extension/lib/popup-controller.js`
- Modify: `extension/tests/popup-controller.test.js`

**Step 1: Write failing classification tests**

新增固定代码：

```go
CodeNetworkFiltered      Code = "network_filtered"
CodeVerificationRequired Code = "verification_required"
CodeJavaScriptRuntime    Code = "javascript_runtime"
```

断言错误优先级：最终诊断出现 `Sign in to confirm you're not a bot` 时必须是验证错误，即使前一次诊断包含 connection reset；`No supported JavaScript runtime` 或 runtime 启动失败映射到运行环境；两次明确 reset 映射到网络过滤。

**Step 2: Verify failure**

Run: `cd helper && go test ./internal/ytdlp ./internal/api -run 'Diagnostic|PlatformError' -count=1`

Expected: FAIL，新代码不存在。

**Step 3: Implement bounded messages**

只向 API 返回固定中文消息，不返回原始 stderr、URL、路径或平台响应。扩展对新错误码显示相同语义并保留未知错误兜底。

**Step 4: Run Go and Node tests**

Run: `cd helper && go test ./internal/ytdlp ./internal/api -count=1`

Run: `node --test extension/tests/*.test.js`

Expected: PASS。

**Step 5: Commit**

```bash
git add helper/internal/ytdlp/runner.go helper/internal/ytdlp/runner_test.go helper/internal/api/engine.go helper/internal/api/engine_test.go extension/lib/helper-client.js extension/tests/helper-client.test.js extension/lib/popup-controller.js extension/tests/popup-controller.test.js
git commit -m "feat: explain platform network failures"
```

### Task 7: 固定获取并校验 Deno 供应链

**Files:**
- Create: `third_party/deno.env`
- Create: `scripts/fetch-deno.zsh`
- Create: `tests/scripts/fetch_deno_test.zsh`
- Modify: `Makefile`
- Modify: `.gitignore`

**Step 1: Write failing shell tests**

仿照 yt-dlp 获取测试，覆盖：固定 manifest 字段、arm64/x86_64 ZIP 与许可证哈希、测试来源只能位于 `work/`、符号链接拒绝、损坏缓存拒绝覆盖、并发发布、权限、架构和临时文件清理。

**Step 2: Verify failure**

Run: `zsh tests/scripts/fetch_deno_test.zsh`

Expected: FAIL，脚本不存在。

**Step 3: Implement fixed fetcher**

从 Deno 官方 GitHub Release 获取两个 macOS 架构 ZIP，在私有临时目录解压预期的单一 `deno` 普通文件，校验 ZIP/二进制/许可证 SHA-256，分别发布为 `deno_macos_arm64` 与 `deno_macos_x86_64`。不得执行下载得到的二进制来决定缓存身份。

**Step 4: Run tests and commit**

Run: `zsh tests/scripts/fetch_deno_test.zsh`

Expected: PASS。

```bash
git add third_party/deno.env scripts/fetch-deno.zsh tests/scripts/fetch_deno_test.zsh Makefile .gitignore
git commit -m "build: pin bundled Deno runtime"
```

### Task 8: 打包 Deno、更新版本与文档

**Files:**
- Modify: `scripts/package-macos.zsh`
- Modify: `tests/scripts/package_macos_test.zsh`
- Modify: `scripts/start-helper.zsh`
- Modify: `scripts/helper-status.zsh`
- Modify: `extension/manifest.json`
- Modify: `helper/cmd/web-video-harbor-helper/main.go`
- Modify: `README.md`
- Modify: `docs/安装使用说明.md`
- Modify: `THIRD_PARTY_NOTICES.md`
- Modify: `tests/repository_branding.test.mjs`
- Modify: `scripts/verify-doc-commands.zsh`

**Step 1: Write failing package/version tests**

断言 ZIP 包含两个 Deno 文件及许可证、权限正确、架构分别正确、哈希匹配、健康检查报告 runtime 可用，并拒绝 `.part`、缓存、测试 fixture、路径和密钥泄露。版本必须统一为 `0.2.1`。

**Step 2: Verify failure**

Run: `zsh tests/scripts/package_macos_test.zsh`

Run: `node --test tests/repository_branding.test.mjs`

Expected: FAIL，包内还没有 Deno且版本仍是 0.2.0。

**Step 3: Implement packaging and docs**

生产打包前运行两个固定获取脚本；复制两架构 Deno 到 `work/dist`，加入白名单和解包验证。更新版本、安装说明、第三方声明、故障提示和文档命令。

**Step 4: Run package-facing tests and commit**

Run: `zsh tests/scripts/fetch_deno_test.zsh`

Run: `zsh tests/scripts/package_macos_test.zsh`

Run: `node --test tests/repository_branding.test.mjs`

Run: `zsh scripts/verify-doc-commands.zsh`

Expected: PASS。

```bash
git add scripts/package-macos.zsh tests/scripts/package_macos_test.zsh scripts/start-helper.zsh scripts/helper-status.zsh extension/manifest.json helper/cmd/web-video-harbor-helper/main.go README.md docs/安装使用说明.md THIRD_PARTY_NOTICES.md tests/repository_branding.test.mjs scripts/verify-doc-commands.zsh
git commit -m "release: prepare WebVideoHarbor v0.2.1"
```

### Task 9: 完整验证、真实冒烟与交付包

**Files:**
- Modify only if a failing test reveals a scoped defect.
- Deliver: `outputs/WebVideoHarbor-macOS.zip`

**Step 1: Run full deterministic verification**

Run: `cd helper && go test -race ./... -count=1`

Run: `node --test extension/tests/*.test.js tests/*.test.mjs`

Run: `cd tests/integration && go test -race ./... -count=1`

Run: `zsh tests/scripts/fetch_yt_dlp_test.zsh`

Run: `zsh tests/scripts/fetch_deno_test.zsh`

Run: `zsh tests/scripts/macos_scripts_test.zsh`

Run: `zsh tests/scripts/package_macos_test.zsh`

Run: `zsh scripts/verify-doc-commands.zsh`

Run: `git diff --check`

Expected: 全部 PASS，且工作树只保留预期修改和既有 `outputs` 链接。

**Step 2: Run bounded live smoke tests**

- Bilibili：对用户 URL 执行无 Cookie 的极小片段测试，确认退出 0、音视频合并成功。
- YouTube：先做无下载元数据解析；确认默认 reset 后进入 Chrome 兼容路径。若 YouTube 要求验证，确认分类为 `verification_required`，不尝试 Cookie。

**Step 3: Build production ZIP**

先运行 `/Users/frank/.codex/scripts/ensure-central-outputs.zsh "/Users/frank/Documents/Codex/2026-07-23/neng"`，再运行 `zsh scripts/package-macos.zsh`。不得覆盖已有 ZIP；若输出冲突，立即报告。

**Step 4: Verify artifact**

记录 ZIP 的绝对路径、大小和 SHA-256；解包验证版本 `0.2.1`、Deno 两架构、yt-dlp、助手和扩展。

**Step 5: Final commit**

```bash
git add -u
git commit -m "test: verify v0.2.1 platform downloads"
```

