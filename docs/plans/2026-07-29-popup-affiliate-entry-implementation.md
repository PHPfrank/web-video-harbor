# WebVideoHarbor Popup Recommendation Entry Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a disclosed, always-visible recommendation entry to the extension popup, highlight it after an observed download completes, and expose the same disclosed entry in the website header.

**Architecture:** Keep the merchant URLs isolated to `docs/recommendations.html`. The popup contains only the fixed GitHub Pages recommendation URL and derives a one-session highlight flag from task status transitions already returned by the local helper; no state is persisted and no analytics are added. The website adds a static navigation link to the same recommendation page.

**Tech Stack:** Manifest V3 extension HTML/CSS/JavaScript, framework-free GitHub Pages HTML/CSS, Node.js built-in test runner, Go test suite for regression verification.

---

### Task 1: Detect a download completion transition in popup memory

**Files:**
- Modify: `extension/tests/popup-controller.test.js`
- Modify: `extension/lib/popup-controller.js`

**Step 1: Write the failing controller test**

Add this test near the existing task refresh tests in `extension/tests/popup-controller.test.js`:

```js
test('recommendation highlight starts only after an observed task completes', async () => {
  const taskResults = [
    [{ id: 'history', title: 'old', status: 'completed', progress: 100 }],
    [
      { id: 'history', title: 'old', status: 'completed', progress: 100 },
      { id: 'active', title: 'new', status: 'downloading', progress: 80 },
    ],
    [
      { id: 'history', title: 'old', status: 'completed', progress: 100 },
      { id: 'active', title: 'new', status: 'completed', progress: 100 },
    ],
  ];
  const { controller, renderer } = harness({
    helper: { listTasks: async () => taskResults.shift() },
  });

  await controller.refreshTasks();
  assert.equal(renderer.tasks.at(-1).recommendationHighlighted, false);

  await controller.refreshTasks();
  assert.equal(renderer.tasks.at(-1).recommendationHighlighted, false);

  await controller.refreshTasks();
  assert.equal(renderer.tasks.at(-1).recommendationHighlighted, true);

  const fresh = harness({
    helper: {
      listTasks: async () => [
        { id: 'active', title: 'new', status: 'completed', progress: 100 },
      ],
    },
  });
  await fresh.controller.refreshTasks();
  assert.equal(fresh.renderer.tasks.at(-1).recommendationHighlighted, false);
});
```

Add a second focused assertion that a transition to `failed` or `canceled` leaves `recommendationHighlighted` false.

**Step 2: Run the test to verify it fails**

Run:

```bash
node --test extension/tests/popup-controller.test.js
```

Expected: FAIL because the task render view does not yet contain `recommendationHighlighted`.

**Step 3: Implement the minimal in-memory transition detector**

In `extension/lib/popup-controller.js`:

1. Add `recommendationHighlighted: false` to `model`.
2. Add this helper next to `tasksFingerprint`:

```js
function didTaskComplete(previousTasks, nextTasks) {
  const previousStatuses = new Map(
    previousTasks
      .filter((task) => task && typeof task.id === 'string')
      .map((task) => [task.id, task.status]),
  );
  return nextTasks.some((task) => task && task.status === 'completed'
    && previousStatuses.has(task.id)
    && previousStatuses.get(task.id) !== 'completed');
}
```

3. In `refreshTasks()`, before replacing `model.tasks`, set the flag when `didTaskComplete(model.tasks, nextTasks)` returns true.
4. In `renderTasks()`, pass `recommendationHighlighted: model.recommendationHighlighted` alongside the existing task view and focus state.

Do not write this flag to Chrome storage, add it to helper requests, or reset it during polling.

**Step 4: Run the focused tests**

Run:

```bash
node --test extension/tests/popup-controller.test.js
```

Expected: PASS, including historical-completed, completed-transition, failed, canceled, and fresh-controller cases.

**Step 5: Commit**

```bash
git add extension/tests/popup-controller.test.js extension/lib/popup-controller.js
git commit -m "feat: detect completed download for recommendation prompt"
```

### Task 2: Add the disclosed sticky recommendation bar to the popup

**Files:**
- Modify: `extension/tests/popup-ui.test.js`
- Modify: `extension/popup.html`
- Modify: `extension/popup.js`
- Modify: `extension/popup.css`

**Step 1: Write failing popup contract assertions**

Add a new test in `extension/tests/popup-ui.test.js` that requires:

```js
const html = source('popup.html');
const javascript = source('popup.js');
const css = source('popup.css');
const projectUrl = 'https://phpfrank.github.io/web-video-harbor/recommendations.html';

assert.match(html, /id=["']popup-recommendation-link["']/);
assert.match(html, new RegExp(projectUrl.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')));
assert.match(html, />推广</);
assert.match(html, /target=["']_blank["']/);
assert.match(html, /rel=["']noopener noreferrer["']/);
assert.match(html, /在新窗口打开/);
assert.match(javascript, /recommendationHighlighted/);
assert.match(javascript, /下载完成，需要更多存储空间？查看推荐/);
assert.match(css, /\.popup-recommendation\s*\{[^}]*position:\s*sticky/s);
assert.match(css, /\.popup-recommendation\s*\{[^}]*min-height:\s*44px/s);
assert.match(css, /\.popup-recommendation\[data-state=["']completed["']\]/);
assert.doesNotMatch(`${html}\n${javascript}`, /s\.click\.taobao\.com|userCode=c5z9bjlt/);
```

Update the existing semantic popup test so it permits exactly the fixed project recommendation URL but still rejects any other `http://` or `https://` URL in `popup.html`.

**Step 2: Run the UI contract test to verify it fails**

Run:

```bash
node --test extension/tests/popup-ui.test.js
```

Expected: FAIL because the popup recommendation markup and styles are absent.

**Step 3: Add semantic popup markup**

Append this link after the task section and before `</main>` in `extension/popup.html`:

```html
<a id="popup-recommendation-link" class="popup-recommendation"
  href="https://phpfrank.github.io/web-video-harbor/recommendations.html"
  target="_blank" rel="noopener noreferrer" data-state="default">
  <span class="popup-promotion-badge">推广</span>
  <span id="popup-recommendation-text" class="popup-recommendation-text">视频文件越来越多？查看存储与云服务推荐</span>
  <span class="popup-recommendation-arrow" aria-hidden="true">→</span>
  <span class="visually-hidden">（在新窗口打开）</span>
</a>
```

The extension must continue to contain neither merchant affiliate destination.

**Step 4: Render the completion state without changing the link**

In `extension/popup.js`:

1. Add `recommendationLink` and `recommendationText` to the `elements` object.
2. At the end of `renderer.renderTasks(view)`, derive the completed/default state from `view.recommendationHighlighted`.
3. Set `elements.recommendationLink.dataset.state` to `completed` or `default`.
4. Use `viewState.setText` to set exactly one of these strings:
   - Default: `视频文件越来越多？查看存储与云服务推荐`
   - Completed: `下载完成，需要更多存储空间？查看推荐`

Do not attach a click listener, append query parameters, call analytics, or use `chrome.storage`.

**Step 5: Add compact sticky styles**

In `extension/popup.css`, add styles for:

- `.popup-recommendation`: sticky bottom position, `z-index`, three-column grid, `min-height: 44px`, existing surface/border colors, no gradient, and a full-row click target.
- `.popup-promotion-badge`: visibly displays “推广” with a bordered compact badge.
- `.popup-recommendation-text`: readable 12px–13px copy with wrapping.
- `.popup-recommendation-arrow`: non-shrinking direction marker.
- `.popup-recommendation[data-state="completed"]`: uses `--color-accent-soft` and accent border, without animation.
- `.popup-recommendation:focus-visible`: matches the existing focus ring.

Use layout space instead of overlaying candidate or task controls. Keep existing `prefers-reduced-motion` behavior unchanged.

**Step 6: Run focused UI and controller tests**

Run:

```bash
node --test extension/tests/popup-ui.test.js extension/tests/popup-controller.test.js
```

Expected: PASS with no direct merchant URL and no unsafe HTML rendering.

**Step 7: Commit**

```bash
git add extension/tests/popup-ui.test.js extension/popup.html extension/popup.js extension/popup.css
git commit -m "feat: surface disclosed recommendations in popup"
```

### Task 3: Add a disclosed recommendation link to the website header

**Files:**
- Modify: `tests/recommendations.test.mjs`
- Modify: `docs/index.html`

**Step 1: Write the failing website assertion**

In the homepage test in `tests/recommendations.test.mjs`, add:

```js
assert.match(
  homepage,
  /<nav\b[^>]*class=["'][^"']*site-nav[^"']*["'][^>]*>[\s\S]*href=["']recommendations\.html["'][^>]*>推荐资源·推广<\/a>/,
);
```

Keep the existing static-page, no-script, and no-external-resource assertions.

**Step 2: Run the test to verify it fails**

Run:

```bash
node --test tests/recommendations.test.mjs
```

Expected: FAIL because the current top navigation has no disclosed recommendation entry.

**Step 3: Add the static header link**

In the `<nav class="site-nav">` block of `docs/index.html`, add:

```html
<a href="recommendations.html">推荐资源·推广</a>
```

Do not remove the existing lower-page recommendation section or change merchant links.

**Step 4: Run the website tests**

Run:

```bash
node --test tests/recommendations.test.mjs
```

Expected: PASS.

**Step 5: Commit**

```bash
git add tests/recommendations.test.mjs docs/index.html
git commit -m "feat: expose disclosed recommendations in site navigation"
```

### Task 4: Verify behavior, privacy boundaries, and layout

**Files:**
- Verify: `extension/popup.html`
- Verify: `extension/popup.js`
- Verify: `extension/popup.css`
- Verify: `extension/lib/popup-controller.js`
- Verify: `docs/index.html`
- Verify: `docs/recommendations.html`

**Step 1: Run the complete Node test suite**

Run:

```bash
node --test extension/tests/*.test.js tests/recommendations.test.mjs tests/repository_branding.test.mjs
```

Expected: 0 failures.

**Step 2: Run the complete Go and integration suites**

Run from `helper/`:

```bash
env GOCACHE=/private/tmp/wvh-go-cache-popup-feature-final go test ./...
```

Run from `tests/integration/`:

```bash
env GOCACHE=/private/tmp/wvh-go-cache-popup-integration-final go test ./...
```

Expected: 0 failures. These commands require permission to bind local test ports and inspect test processes.

**Step 3: Check privacy and fixed-link boundaries**

Run:

```bash
rg -n "s\.click\.taobao\.com|userCode=c5z9bjlt|gtag|google-analytics|plausible|umami|matomo" extension
```

Expected: no output.

Run:

```bash
rg -n "popup-recommendation-link|推荐资源·推广|页面可能包含推广链接" extension docs README.md PRIVACY.md
```

Expected: popup and website entries are visible and disclosures remain present.

**Step 4: Perform visual checks**

Serve the worktree locally and inspect these states at the popup's 400px width:

- Empty candidate/task lists.
- Multiple candidate and task cards with vertical scrolling.
- Default recommendation state.
- Completed recommendation state by temporarily setting `data-state="completed"` in browser developer tools only.
- Keyboard focus on the recommendation link.
- Website header at desktop and narrow mobile widths.

Save temporary screenshots only under `work/popup-recommendation-verification/`. Confirm no horizontal scroll, no hidden task actions, a visible “推广” badge, at least a 44px click target, and no animation.

Do not modify committed files to simulate visual state.

**Step 5: Check the final diff**

Run:

```bash
git diff --check
git status --short
git log --oneline --decorate -5
```

Expected: no whitespace errors, no uncommitted implementation files, and the design plus three implementation commits are present.

### Task 5: Hold release packaging for explicit approval

This change updates source files only. Do not replace the immutable v1.0.0 Release asset and do not silently bump the product version. After visual approval, present a separate choice to package and publish v1.0.1 or keep the source-only change for additional testing.

