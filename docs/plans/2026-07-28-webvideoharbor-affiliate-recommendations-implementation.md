# WebVideoHarbor Affiliate Recommendations Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Publish a transparent two-card affiliate recommendation page on GitHub Pages and add a low-distraction, disclosed entry point to the extension settings page without adding tracking or changing download behavior.

**Architecture:** Add framework-free static HTML/CSS under `docs/` so GitHub Pages can publish directly from `main:/docs`. Keep both affiliate URLs exclusively on the recommendation page; the extension contains only the fixed GitHub Pages URL. Update repository privacy and product disclosures in the same change, with Node contract tests guarding the links, labels, privacy boundaries, and absence of tracking.

**Tech Stack:** Static HTML5/CSS, Chrome Extension Manifest V3, Node.js built-in test runner, GitHub Pages, GitHub CLI.

---

## Preconditions and scope

- Work from a dedicated worktree or clean feature branch created from commit `597ef71` or later.
- Read `docs/plans/2026-07-28-webvideoharbor-affiliate-recommendations-design.md` before implementation.
- Use `@frontend-design` before authoring the GitHub Pages UI, while preserving the approved restrained blue-gray direction.
- Do not add NAS, analytics, an ad SDK, remote JSON, prices, brand claims, or a release/version bump.
- Do not touch the existing task-local `outputs` link if it appears as untracked.

### Task 1: Build the tested GitHub Pages site

**Files:**

- Create: `tests/recommendations.test.mjs`
- Create: `docs/index.html`
- Create: `docs/recommendations.html`
- Create: `docs/site.css`
- Create: `docs/.nojekyll`

**Step 1: Write the failing static-site contract test**

Create `tests/recommendations.test.mjs` with this content:

```js
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const docsRoot = path.join(repoRoot, 'docs');

function read(name) {
  return fs.readFileSync(path.join(docsRoot, name), 'utf8');
}

test('GitHub Pages publishes a static project home and disclosed recommendation page', () => {
  for (const name of ['index.html', 'recommendations.html', 'site.css', '.nojekyll']) {
    assert.equal(fs.existsSync(path.join(docsRoot, name)), true, name);
  }

  const home = read('index.html');
  const recommendations = read('recommendations.html');

  assert.match(home, /WebVideoHarbor|网页视频港/);
  assert.match(home, /href=["']recommendations\.html["']/);
  assert.match(recommendations, /本页包含推广链接/);
  assert.match(recommendations, /不会增加你的购买价格/);
  assert.equal((recommendations.match(/class=["'][^"']*recommendation-card\b/g) || []).length, 2);
});

test('recommendation links are explicit, sponsored, static, and untracked by the project', () => {
  const recommendations = read('recommendations.html');

  for (const url of [
    'https://s.click.taobao.com/KXMdz3k',
    'https://www.aliyun.com/minisite/goods?userCode=c5z9bjlt',
  ]) {
    assert.match(recommendations, new RegExp(`href=["']${url.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}["']`));
  }

  assert.equal((recommendations.match(/rel=["']sponsored noopener noreferrer["']/g) || []).length, 2);
  assert.equal((recommendations.match(/>推广</g) || []).length, 2);
  assert.match(recommendations, /具体品牌、型号、容量、价格和售后以商家页面实时信息为准/);
  assert.doesNotMatch(recommendations, /1082\.05|到手价|券面额/);
  assert.doesNotMatch(recommendations, /<script\b|gtag|google-analytics|plausible|umami|matomo|tracking pixel/i);
});
```

**Step 2: Run the test to verify it fails**

Run:

```bash
node --test tests/recommendations.test.mjs
```

Expected: FAIL because `docs/index.html` and the other Pages files do not exist.

**Step 3: Implement the minimal static site**

Create `docs/index.html` as a semantic project landing page with:

- A header naming WebVideoHarbor Community Edition.
- A short statement that media processing remains local.
- Links to `安装使用说明.md`, `使用边界.md`, `../PRIVACY.md`, and the GitHub repository.
- A visibly secondary link to `recommendations.html` labeled “存储与云服务推荐”, followed by “页面可能包含推广链接”.
- No script tags, remote fonts, remote images, or analytics.

Create `docs/recommendations.html` with this exact information architecture:

```html
<main class="site-shell">
  <header class="page-header">
    <a class="back-link" href="index.html">← 返回网页视频港</a>
    <p class="eyebrow">可选推荐</p>
    <h1>存储与云服务</h1>
    <p>以下资源用于帮助保存或管理本地视频，不影响 WebVideoHarbor 的任何功能。</p>
  </header>

  <aside class="affiliate-disclosure" aria-label="推广关系说明">
    本页包含推广链接。如果你通过这些链接购买产品，项目维护者可能获得佣金，
    但不会增加你的购买价格。推荐内容不会影响 WebVideoHarbor 的功能。
  </aside>

  <section class="recommendation-grid" aria-label="推荐资源">
    <article class="recommendation-card">
      <span class="promotion-badge">推广</span>
      <h2>移动固态硬盘选购入口</h2>
      <p>适合保存体积较大的本地视频文件。</p>
      <p class="recommendation-note">具体品牌、型号、容量、价格和售后以商家页面实时信息为准。</p>
      <a class="external-link" href="https://s.click.taobao.com/KXMdz3k"
         target="_blank" rel="sponsored noopener noreferrer">前往了解 <span aria-hidden="true">↗</span></a>
    </article>

    <article class="recommendation-card">
      <span class="promotion-badge">推广</span>
      <h2>阿里云产品与对象存储</h2>
      <p>适合需要了解对象存储、云端保存或其他云产品的用户。</p>
      <p class="recommendation-note">适用范围、费用和返利资格以阿里云页面实时信息为准。</p>
      <a class="external-link" href="https://www.aliyun.com/minisite/goods?userCode=c5z9bjlt"
         target="_blank" rel="sponsored noopener noreferrer">前往了解 <span aria-hidden="true">↗</span></a>
    </article>
  </section>
</main>
```

Create `docs/site.css` with:

- The extension's existing warm canvas, white surfaces, dark ink, muted copy, and green accent values.
- A centered content width around `1040px` and a two-column recommendation grid above `720px`.
- A single-column layout below `720px`.
- Visible keyboard focus for every link.
- Minimum 44px target height for primary links.
- `prefers-reduced-motion` handling.
- No gradients, animation loops, or remote assets.

Create an empty `docs/.nojekyll` so Pages serves the static files without Jekyll processing.

**Step 4: Run the site contract test**

Run:

```bash
node --test tests/recommendations.test.mjs
```

Expected: 2 tests PASS.

**Step 5: Inspect the site locally**

Run:

```bash
python3 -m http.server 4173 --directory docs
```

Open `http://127.0.0.1:4173/` and `http://127.0.0.1:4173/recommendations.html` at desktop and narrow mobile widths. Verify readable focus states, two cards on desktop, one column on mobile, and no horizontal scrolling. Stop the server afterward.

**Step 6: Commit the static site**

```bash
git add tests/recommendations.test.mjs docs/index.html docs/recommendations.html docs/site.css docs/.nojekyll
git commit -m "feat: add disclosed recommendation site"
```

### Task 2: Add the disclosed extension settings entry

**Files:**

- Modify: `extension/tests/popup-ui.test.js`
- Modify: `extension/options.html`
- Modify: `extension/popup.css`

**Step 1: Write the failing extension UI test**

Append this test to `extension/tests/popup-ui.test.js`:

```js
test('options page links only to the disclosed project recommendation page', () => {
  const html = source('options.html');
  const extensionSources = [
    'options.html', 'options.js', 'popup.html', 'popup.js', 'background.js', 'content.js',
  ].map(source).join('\n');

  assert.match(html, /id=["']recommendations-link["']/);
  assert.match(html, /href=["']https:\/\/phpfrank\.github\.io\/web-video-harbor\/recommendations\.html["']/);
  assert.match(html, /页面可能包含推广链接/);
  assert.match(html, /target=["']_blank["']/);
  assert.match(html, /rel=["']noopener noreferrer["']/);
  assert.doesNotMatch(extensionSources, /s\.click\.taobao\.com|userCode=c5z9bjlt/);
});
```

**Step 2: Run the test to verify it fails**

Run:

```bash
node --test extension/tests/popup-ui.test.js
```

Expected: FAIL because `recommendations-link` is absent.

**Step 3: Add the settings-page card**

In `extension/options.html`, immediately after the existing privacy card, add:

```html
<section class="settings-card recommendations-card" aria-labelledby="recommendations-title">
  <div>
    <p class="section-kicker">可选推荐</p>
    <h2 id="recommendations-title">存储与云服务</h2>
    <p class="recommendations-description">
      查看适合保存和管理视频的存储与云服务。页面可能包含推广链接。
    </p>
  </div>
  <a id="recommendations-link" class="button button-secondary resource-link"
     href="https://phpfrank.github.io/web-video-harbor/recommendations.html"
     target="_blank" rel="noopener noreferrer">查看推荐资源</a>
</section>
```

Do not add JavaScript. The fixed anchor preserves a narrow data flow and cannot attach the active tab or media metadata.

In `extension/popup.css`, add styles that:

- Give `.recommendations-card` the same top margin and flat shadow treatment as `.privacy-card`.
- Lay out the copy and action side by side on wide settings pages and stack them below `560px`.
- Give `.recommendations-description` muted readable copy.
- Make `.resource-link` an inline-flex control with no underline and a visible `:focus-visible` outline.

**Step 4: Run the extension UI test**

Run:

```bash
node --test extension/tests/popup-ui.test.js
```

Expected: all tests PASS.

**Step 5: Commit the settings entry**

```bash
git add extension/tests/popup-ui.test.js extension/options.html extension/popup.css
git commit -m "feat: link settings to recommendation page"
```

### Task 3: Align repository and privacy disclosures

**Files:**

- Modify: `tests/repository_branding.test.mjs`
- Modify: `README.md`
- Modify: `PRIVACY.md`

**Step 1: Write the failing disclosure assertions**

In `tests/repository_branding.test.mjs`, after the existing README link assertions, add:

```js
assert.match(readme, /推荐资源/);
assert.match(readme, /可能包含推广链接/);
assert.match(readme, /https:\/\/phpfrank\.github\.io\/web-video-harbor\/recommendations\.html/);
```

After the existing privacy topic loop, add:

```js
for (const topic of [
  '扩展和本地助手不包含广告 SDK',
  '官网可能展示清晰标注的联盟推荐链接',
  '用户主动点击',
  '不增加自己的点击跟踪',
]) {
  assert.match(privacy, new RegExp(topic), `affiliate privacy disclosure is missing: ${topic}`);
}
```

**Step 2: Run the test to verify it fails**

Run:

```bash
node --test tests/repository_branding.test.mjs
```

Expected: FAIL because the new disclosure text is absent.

**Step 3: Update README product disclosure**

In `README.md`:

- Change the current statement that broadly says the project contains no advertising so it accurately says the extension and local helper contain no analytics, telemetry, ad SDK, or remote authorization service.
- Add a short `## 可选推荐资源` section after privacy and local security.
- Link to `https://phpfrank.github.io/web-video-harbor/recommendations.html`.
- State that the page may contain clearly marked affiliate links, that the project may receive commission, and that recommendations do not affect free functionality.

Do not put either affiliate URL directly in README.

**Step 4: Update the privacy boundary**

In `PRIVACY.md`:

- Preserve all current local-processing and non-collection statements.
- Replace “不包含分析、遥测、广告 SDK 或用户行为跟踪” with the more explicit “扩展和本地助手不包含广告 SDK、分析、遥测或用户行为跟踪”.
- Add a `## 可选推广链接` section stating:
  - The official website may show clearly marked affiliate recommendation links.
  - WebVideoHarbor does not send page URLs, media URLs, download history, or pairing tokens to the recommendation page.
  - The merchant receives a request only after the user actively clicks an external link and then applies its own terms and privacy policy.
  - WebVideoHarbor does not add its own click tracking or redirect service.

**Step 5: Run the repository disclosure test**

Run:

```bash
node --test tests/repository_branding.test.mjs
```

Expected: 2 tests PASS.

**Step 6: Commit the aligned disclosures**

```bash
git add tests/repository_branding.test.mjs README.md PRIVACY.md
git commit -m "docs: disclose optional affiliate recommendations"
```

### Task 4: Run the complete local verification

**Files:**

- Verify only; no planned file changes.

**Step 1: Run all JavaScript contract and unit tests**

Run:

```bash
node --test extension/tests/*.test.js tests/*.test.mjs
```

Expected: all tests PASS with zero failures.

**Step 2: Run the existing helper and syntax checks**

Run:

```bash
make check
```

Expected: all Go tests PASS and both extension syntax checks exit successfully.

**Step 3: Check formatting and accidental changes**

Run:

```bash
git diff --check
git status --short
```

Expected: no whitespace errors and no uncommitted implementation files. The task-local `outputs` link may remain untracked and must not be staged.

**Step 4: Recheck the two external URLs**

Run:

```bash
curl -fsSIL --max-redirs 5 'https://s.click.taobao.com/KXMdz3k'
curl -fsSIL --max-redirs 5 'https://www.aliyun.com/minisite/goods?userCode=c5z9bjlt'
```

Expected: each request reaches an HTTP success response or a normal merchant redirect chain without DNS, TLS, or 4xx/5xx failure.

**Step 5: Review the final commits**

Run:

```bash
git log -4 --oneline
```

Expected: separate commits for the static site, extension entry, disclosure updates, and the earlier design documentation.

### Task 5: Publish GitHub Pages and verify the live flow

**Files:**

- External GitHub repository settings only; no planned source changes.

**Step 1: Push the completed branch only after local verification**

If implementation was performed on `main`, run:

```bash
git push origin main
```

If implementation was performed on a feature branch, merge or open a pull request first, then push the approved integration branch. Do not enable Pages against an unreviewed branch.

**Step 2: Inspect current Pages configuration**

Run:

```bash
gh api repos/PHPfrank/web-video-harbor/pages
```

Expected: existing Pages configuration JSON, or HTTP 404 if Pages has never been enabled.

**Step 3: Configure Pages from `main:/docs`**

If Pages does not exist, run:

```bash
gh api --method POST repos/PHPfrank/web-video-harbor/pages \
  -f 'source[branch]=main' \
  -f 'source[path]=/docs'
```

If Pages already exists with another source, run:

```bash
gh api --method PUT repos/PHPfrank/web-video-harbor/pages \
  -f 'source[branch]=main' \
  -f 'source[path]=/docs'
```

Expected: Pages reports `main` and `/docs` as its source.

**Step 4: Check the Pages deployment**

Run:

```bash
gh api repos/PHPfrank/web-video-harbor/pages/builds/latest
curl -fsSI 'https://phpfrank.github.io/web-video-harbor/'
curl -fsSI 'https://phpfrank.github.io/web-video-harbor/recommendations.html'
```

Expected: the latest build eventually reports `built`, and both public pages return HTTP 200.

**Step 5: Verify the extension-to-site flow manually**

- Reload the unpacked extension in Chrome.
- Open the WebVideoHarbor settings page.
- Confirm the optional recommendation card appears after privacy and safety.
- Activate the link with both pointer and keyboard.
- Confirm it opens the fixed GitHub Pages recommendation page in a new tab.
- Confirm neither affiliate URL appears until the user reaches the recommendation page.
- Confirm both cards are marked “推广” and both merchant links require an additional user click.

**Step 6: Record publication status**

Document the public Pages URL in the implementation handoff. Do not create a new GitHub release or bump `VERSION`/`manifest.json` unless the user separately approves a release.

