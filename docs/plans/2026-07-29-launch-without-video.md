# WebVideoHarbor 无视频首发准备 Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 在取消宣传视频后，让普通 macOS 用户仍能从 GitHub 快速下载安装，并准备一套可直接复制发布的三图首发内容。

**Architecture:** 不改程序功能，只调整公开说明和推广素材。README 顶部增加正式安装包入口，将“普通用户安装”和“开发者源码构建”明确分开；推广包新增无视频发布清单，复用已经验收的核心截图和统一产品口径。

**Tech Stack:** Markdown、GitHub Releases、GitHub Pages、现有 PNG 宣传图。

---

### Task 1: 优化 README 的普通用户下载路径

**Files:**
- Modify: `README.md`

**Step 1: 记录当前问题**

确认 README 的“快速开始”要求用户先安装 Go 并自行构建，但没有在顶部提供 v1.0.0 Release 或安装包直链。

**Step 2: 添加直接下载入口**

在版本说明后增加“下载与安装”区块，包含 v1.0.0 发布页、macOS 安装包直链、安装使用说明和 SHA-256。

**Step 3: 区分普通安装与源码构建**

将原“快速开始”改成“从源码运行（开发者）”，避免普通用户误以为必须安装 Go。

**Step 4: 验证文档链接**

运行：

```bash
rg -n "v1.0.0|WebVideoHarbor-macOS-v1.0.0.zip|从源码运行" README.md
```

预期：README 同时包含版本页、安装包、安装说明和开发者入口。

### Task 2: 生成无视频首发执行包

**Files:**
- Create: `outputs/WebVideoHarbor-第一周推广素材包/11-无视频首发执行包.md`
- Modify: `outputs/WebVideoHarbor-第一周推广素材包/00-使用说明.md`
- Modify: `outputs/WebVideoHarbor-第一周推广素材包/09-首周发布日历.md`

**Step 1: 更新输出目录映射**

运行：

```bash
/Users/frank/.codex/scripts/ensure-central-outputs.zsh /Users/frank/Documents/Codex/2026-07-23/neng
```

预期：返回任务的中央输出路径且无冲突。

**Step 2: 编排三图顺序**

固定使用：项目主页、发现可下载视频、下载完成并定位文件。取消所有视频相关要求。

**Step 3: 编写可直接复制的首发文字**

提供朋友圈/微信群通用首发文案、配图顺序、正式链接和合法使用边界；正文控制在手机一屏半左右。

**Step 4: 添加发布前检查**

检查链接、版本号、截图隐私、回复入口和首日记录项。

**Step 5: 清理当前流程中的视频依赖**

将素材包使用说明和首周发布日历调整为“三图 + 短文”路线。视频脚本保留为以后可选资料，但不再作为当前首发前置条件。

### Task 3: 做发布前验证

**Files:**
- Verify: `README.md`
- Verify: `outputs/WebVideoHarbor-第一周推广素材包/11-无视频首发执行包.md`

**Step 1: 检查 Markdown 内容**

运行：

```bash
rg -n "v1.0.0|github.com/PHPfrank/web-video-harbor|仅用于|视频" README.md outputs/WebVideoHarbor-第一周推广素材包/11-无视频首发执行包.md
```

预期：版本和链接一致，无视频首发文件不要求录制或上传视频。

**Step 2: 检查公开入口**

用 GitHub API 和 HTTP 请求确认仓库公开、Release 非草稿、安装包存在、官网返回 200。

**Step 3: 检查工作区变更**

运行：

```bash
git diff --check
git diff -- README.md docs/plans/2026-07-29-launch-without-video.md
```

预期：无空白错误，只包含本轮文档改动。
