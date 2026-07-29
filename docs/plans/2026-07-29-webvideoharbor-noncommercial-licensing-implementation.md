# WebVideoHarbor Noncommercial Licensing Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 将 WebVideoHarbor 当前主分支及未来版本切换为仅限非商业使用的源码公开项目，同时准确保留历史 MIT 边界和第三方授权。

**Architecture:** 根目录使用未经修改的 PolyForm Noncommercial 1.0.0 正文，并通过独立 `NOTICE` 提供 PHPfrank Required Notice。README 和第三方声明描述新的许可边界，`TRADEMARKS.md` 将源码许可与官方品牌权利分离，现有仓库品牌测试负责防止这些声明后续漂移。

**Tech Stack:** Markdown、PolyForm Noncommercial License 1.0.0、Node.js 内置测试运行器、Git

---

### Task 1: 用仓库测试固定新的授权边界

**Files:**
- Modify: `tests/repository_branding.test.mjs`
- Test: `tests/repository_branding.test.mjs`

**Step 1: 写入预期失败的许可与品牌断言**

让测试读取 `NOTICE` 和 `TRADEMARKS.md`，并将 MIT 断言替换为以下行为断言：

```js
assert.match(license, /^# PolyForm Noncommercial License 1\.0\.0\n/);
assert.match(license, /noncommercial purpose is a permitted purpose/);
assert.match(license, /Personal use for research, experiment, and testing/);
assert.match(notice, /Required Notice: Copyright 2026-present PHPfrank\./);
assert.match(notice, /https:\/\/github\.com\/PHPfrank\/web-video-harbor/);
assert.match(readme, /源码公开/);
assert.match(readme, /PolyForm Noncommercial License 1\.0\.0/);
assert.match(readme, /未经 PHPfrank 事先书面许可.*商业用途/s);
assert.match(readme, /b56aa8c.*MIT License/s);
assert.match(trademarks, /WebVideoHarbor/);
assert.match(trademarks, /网页视频港/);
assert.match(trademarks, /不授予.*商标/s);
assert.match(notices, /WebVideoHarbor.*PolyForm Noncommercial/s);
```

同时删除 README 必须含“开源”和 MIT 链接的旧断言，保留免费、本地运行、隐私和使用边界等无关断言。

**Step 2: 运行测试确认失败**

Run: `node --test tests/repository_branding.test.mjs`

Expected: FAIL，至少报告 `NOTICE` 或 `TRADEMARKS.md` 不存在，证明测试先于实现生效。

**Step 3: 提交测试**

```bash
git add tests/repository_branding.test.mjs
git commit -m "test: define noncommercial licensing boundary"
```

### Task 2: 安装标准许可证和品牌声明

**Files:**
- Modify: `LICENSE`
- Create: `NOTICE`
- Create: `TRADEMARKS.md`
- Test: `tests/repository_branding.test.mjs`

**Step 1: 替换许可证正文**

将 `LICENSE` 完整替换为 PolyForm 官方仓库 `1.0.0/PolyForm-Noncommercial-1.0.0.md` 的原文，不修改标题、正文、定义或免责声明。

**Step 2: 添加 Required Notice**

创建 `NOTICE`：

```text
Required Notice: Copyright 2026-present PHPfrank.
Required Notice: The official WebVideoHarbor repository is https://github.com/PHPfrank/web-video-harbor.
```

**Step 3: 添加品牌政策**

创建 `TRADEMARKS.md`，覆盖以下规则：

- WebVideoHarbor、网页视频港和官方 Logo/视觉标识属于 PHPfrank 的官方品牌；
- PolyForm 源码许可证不授予商标或品牌使用权；
- 允许准确说明来源、兼容性和贡献关系；
- 衍生版本必须更换名称和标识并声明非官方；
- 未经书面许可，不得使用官方品牌发布应用、安装包、网站、账号或服务，也不得暗示背书。

**Step 4: 运行定向测试**

Run: `node --test tests/repository_branding.test.mjs`

Expected: FAIL，只剩 README 或 `THIRD_PARTY_NOTICES.md` 的旧授权描述不匹配。

**Step 5: 提交许可和品牌文件**

```bash
git add LICENSE NOTICE TRADEMARKS.md
git commit -m "legal: adopt noncommercial source license"
```

### Task 3: 更新公开说明和第三方边界

**Files:**
- Modify: `README.md`
- Modify: `THIRD_PARTY_NOTICES.md`
- Test: `tests/repository_branding.test.mjs`

**Step 1: 更新项目定位**

将 README 开头的“开源”改为“源码公开”，保留“完全免费”和“本地运行”。将“开源与第三方组件”改为“源码许可与第三方组件”。

**Step 2: 写清许可范围**

在 README 许可章节说明：

- 当前主分支及未来版本采用 PolyForm Noncommercial 1.0.0；
- 允许许可证列出的个人学习、研究、测试、业余项目和其他非商业用途；
- 未经 PHPfrank 事先书面许可，不得用于商业产品、收费服务、广告获利、企业内部商业活动或其他商业用途；
- `b56aa8c` 及更早修订仍按 MIT License 使用，既有授权不追溯撤销；
- 源码许可不授予项目品牌权利，并链接 `TRADEMARKS.md`；
- 第三方组件继续适用各自许可，并链接 `THIRD_PARTY_NOTICES.md`。

**Step 3: 更新第三方声明首段**

将 WebVideoHarbor 自有代码的 MIT 描述改为 PolyForm Noncommercial 1.0.0，并明确该变更不会替代、改变或扩展第三方组件的许可。

**Step 4: 运行测试确认通过**

Run: `node --test tests/repository_branding.test.mjs`

Expected: PASS。

**Step 5: 提交公开说明**

```bash
git add README.md THIRD_PARTY_NOTICES.md
git commit -m "docs: explain noncommercial use terms"
```

### Task 4: 验证完整变更

**Files:**
- Verify: `LICENSE`
- Verify: `NOTICE`
- Verify: `TRADEMARKS.md`
- Verify: `README.md`
- Verify: `THIRD_PARTY_NOTICES.md`
- Verify: `tests/repository_branding.test.mjs`

**Step 1: 比较官方许可证正文**

从 `https://raw.githubusercontent.com/polyformproject/polyform-licenses/1.0.0/PolyForm-Noncommercial-1.0.0.md` 下载临时副本，并运行：

```bash
cmp LICENSE /tmp/PolyForm-Noncommercial-1.0.0.md
```

Expected: 无输出，退出状态为 0。

**Step 2: 运行仓库文档和品牌测试**

Run: `node --test tests/repository_branding.test.mjs tests/recommendations.test.mjs`

Expected: 所有测试 PASS。

**Step 3: 运行核心测试**

Run: `make test`

Expected: Go 测试全部 PASS。

**Step 4: 检查格式和工作区**

Run: `git diff --check HEAD~3..HEAD`

Expected: 无输出，退出状态为 0。

Run: `git status --short --branch`

Expected: 工作区干净，分支只领先远端本次设计、计划和实施提交。

**Step 5: 提交实施计划**

```bash
git add docs/plans/2026-07-29-webvideoharbor-noncommercial-licensing-implementation.md
git commit -m "docs: plan noncommercial licensing transition"
```
