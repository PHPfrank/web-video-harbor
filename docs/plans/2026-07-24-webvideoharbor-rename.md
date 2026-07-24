# WebVideoHarbor Rename Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 将项目所有当前生产界面、运行时标识、文档和 macOS 发布物统一更名为 WebVideoHarbor / 网页视频港。

**Architecture:** 保留本地端口、API、功能与安全边界，只替换品牌层、Go 内部模块前缀、助手二进制名以及默认本地目录。以现有测试作为契约，先改断言观察失败，再实施最小改名，最终重建并验证 universal macOS 发布包。

**Tech Stack:** Chrome Manifest V3、原生 HTML/CSS/JavaScript、Go、zsh、FFmpeg、Node.js test runner。

---

### Task 1: 建立新命名测试契约

**Files:**
- Modify: `extension/tests/popup-ui.test.js`
- Modify: `helper/internal/config/config_test.go`
- Modify: `helper/cmd/web-video-helper/main_test.go`
- Modify: `tests/scripts/macos_scripts_test.zsh`
- Modify: `tests/scripts/package_macos_test.zsh`
- Modify: `tests/integration/chrome_extension_smoke.mjs`

**Step 1: 将断言切换到新名称**

断言扩展名称为 `网页视频港`，状态目录为 `Library/Application Support/WebVideoHarbor`，下载目录为 `Downloads/WebVideoHarbor`，包名和顶层目录为 `WebVideoHarbor-macOS`，助手二进制为 `web-video-harbor-helper`。

**Step 2: 运行定向测试确认失败**

Run: `node --test extension/tests/popup-ui.test.js`

Run: `cd helper && go test ./internal/config ./cmd/web-video-helper`

Run: `zsh tests/scripts/macos_scripts_test.zsh`

Expected: FAIL，失败内容指向仍存在的旧显示名、目录或二进制名称。

**Step 3: 提交测试契约**

```bash
git add extension/tests/popup-ui.test.js helper/internal/config/config_test.go helper/cmd/web-video-helper/main_test.go tests/scripts/macos_scripts_test.zsh tests/scripts/package_macos_test.zsh tests/integration/chrome_extension_smoke.mjs
git commit -m "test: define WebVideoHarbor branding contract"
```

### Task 2: 更新扩展与端到端显示名

**Files:**
- Modify: `extension/manifest.json`
- Modify: `extension/popup.html`
- Modify: `extension/options.html`
- Modify: `tests/fixtures/site/index.html`
- Modify: `tests/integration/extension_helper_smoke.cjs`
- Modify: `scripts/run-smoke-test.zsh`

**Step 1: 实施最小显示名替换**

将 Chrome 扩展、弹窗、设置页和受控测试标题统一为 `网页视频港`；集成任务名改为 `网页视频港集成测试`。

**Step 2: 运行扩展测试**

Run: `node --test extension/tests/*.test.js`

Expected: PASS，74 项或更多测试全部通过。

**Step 3: 检查 JavaScript 与清单**

Run: `node --check extension/background.js && node --check extension/content.js && node --check extension/popup.js && node --check extension/options.js`

Expected: exit 0。

**Step 4: 提交扩展改名**

```bash
git add extension tests/fixtures/site/index.html tests/integration/extension_helper_smoke.cjs scripts/run-smoke-test.zsh
git commit -m "feat: rename extension to WebVideoHarbor"
```

### Task 3: 更新助手模块、二进制与默认目录

**Files:**
- Modify: `helper/go.mod`
- Modify: `helper/**/*.go`
- Modify: `tests/integration/go.mod`
- Modify: `tests/integration/*.go`
- Modify: `helper/internal/config/config.go`
- Modify: `scripts/build-macos.zsh`
- Modify: `scripts/helper-common.zsh`
- Modify: `scripts/start-helper.zsh`
- Modify: `scripts/stop-helper.zsh`
- Modify: `scripts/helper-status.zsh`
- Modify: `tests/scripts/macos_scripts_test.zsh`

**Step 1: 更换 Go 模块前缀**

将 `web-video-downloader/helper` 机械替换为 `web-video-harbor/helper`，保持包结构不变。

**Step 2: 更换助手文件名和默认目录**

将可执行文件统一为 `web-video-harbor-helper`，状态目录统一为 `~/Library/Application Support/WebVideoHarbor/`，下载目录统一为 `~/Downloads/WebVideoHarbor/`。

**Step 3: 运行 Go 测试**

Run: `cd helper && go test -race ./...`

Expected: PASS。

**Step 4: 运行生命周期测试**

Run: `zsh tests/scripts/macos_scripts_test.zsh`

Expected: PASS，启动、状态、停止、权限与目录断言全部使用新名称。

**Step 5: 提交助手改名**

```bash
git add helper tests/integration scripts tests/scripts/macos_scripts_test.zsh
git commit -m "feat: rename local helper and runtime paths"
```

### Task 4: 更新 GitHub 文档和发布包命名

**Files:**
- Modify: `README.md`
- Modify: `docs/安装使用说明.md`
- Modify: `scripts/package-macos.zsh`
- Modify: `tests/scripts/package_macos_test.zsh`

**Step 1: 更新 README 与安装说明**

标题使用 `WebVideoHarbor（网页视频港）`，说明 Chrome 扩展与 macOS 助手架构、MP4/HLS/视频号最佳努力兼容、安全边界和合法使用要求。所有命令、二进制、目录与界面名称使用新品牌。

**Step 2: 更新打包脚本**

发布文件改为 `WebVideoHarbor-macOS.zip`，顶层目录改为 `WebVideoHarbor-macOS/`，包内二进制使用 `web-video-harbor-helper`。保留原子发布、不覆盖、白名单和可重复归档规则。

**Step 3: 运行打包行为测试**

Run: `zsh tests/scripts/package_macos_test.zsh`

Expected: PASS，并输出稳定 SHA256。

**Step 4: 搜索残留名称**

Run: `rg -n --hidden --glob '!work/**' --glob '!.git/**' '网页视频下载器|web-video-downloader|网页视频下载器-macOS' README.md docs/安装使用说明.md extension helper scripts tests`

Expected: 生产代码和当前用户文档无旧名；历史计划文件不纳入失败范围。

**Step 5: 提交文档与打包改名**

```bash
git add README.md docs/安装使用说明.md scripts/package-macos.zsh tests/scripts/package_macos_test.zsh
git commit -m "docs: prepare WebVideoHarbor GitHub release"
```

### Task 5: 完整验证与新发布物

**Files:**
- Create: `outputs/WebVideoHarbor-macOS.zip`（通过集中输出链接发布）

**Step 1: 准备集中输出目录**

Run: `/Users/frank/.codex/scripts/ensure-central-outputs.zsh "/Users/frank/Documents/Codex/2026-07-23/neng"`

Expected: task-local `outputs/` 正确链接到集中输出目录且无冲突。

**Step 2: 运行完整 smoke**

Run: `/bin/zsh scripts/run-smoke-test.zsh`

Expected: Go、集成、Chrome、扩展、MP4 和 HLS 媒体验证全部通过。

**Step 3: 生成并验证新 ZIP**

Run: `/bin/zsh scripts/package-macos.zsh`

Expected: 新建 `outputs/WebVideoHarbor-macOS.zip`，不覆盖旧 ZIP；解包检查、universal 架构与源码重建全部通过。

**Step 4: 核对发布物**

Run: `shasum -a 256 outputs/WebVideoHarbor-macOS.zip && unzip -t outputs/WebVideoHarbor-macOS.zip`

Expected: 64 位 SHA256，ZIP 无错误。

**Step 5: 最终状态检查**

Run: `git status --short && git diff --check`

Expected: 工作区干净，无空白错误。
