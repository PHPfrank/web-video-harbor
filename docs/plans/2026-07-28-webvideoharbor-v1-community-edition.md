# WebVideoHarbor v1.0.0 Community Edition Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Ship a completely free MIT-licensed v1.0.0 Community Edition whose generic MP4/WebM/HLS features remain on by default while bundled experimental platform compatibility is locally consent-gated and off by default.

**Architecture:** Add a secure, versioned settings store beside the helper configuration and expose it through a paired loopback API. Both the Chrome extension and Go task engine enforce the experimental-platform switch; the extension hides platform-page candidates while disabled, and the engine rejects platform or platform-page jobs and retries before starting any downloader. Keep yt-dlp and Deno pinned and bundled, retain the no-Cookie/no-DRM boundaries, and publish only through GitHub after deterministic tests and local macOS verification.

**Tech Stack:** Chrome Extension Manifest V3, vanilla JavaScript with `node:test`, Go 1.x standard library, FFmpeg, pinned yt-dlp and Deno, zsh packaging tests, GitHub CLI.

---

## Working rules

- Execute in an isolated Git worktree created with `superpowers:using-git-worktrees`.
- Use `superpowers:test-driven-development` for every behavior change.
- Do not stage or commit the task-local `outputs` symlink.
- Keep generated packages, fixtures, logs and release-note drafts under `work/` until the final package script publishes through `outputs/`.
- Do not push, tag, create a Release, or delete v0.2.1 until the user separately authorizes the external GitHub step.
- Do not weaken the existing URL, process, filesystem, secret-redaction or third-party-binary checks.
- Do not add Cookie permissions, account access, arbitrary yt-dlp flags, proxy controls, custom binaries, DRM support, analytics or remote services.
- When the switch is turned off, already-running jobs may finish deterministically; new and retried experimental-platform jobs must be rejected immediately.

### Task 1: Add the secure local compatibility settings store

**Files:**
- Create: `helper/internal/settings/store.go`
- Create: `helper/internal/settings/store_test.go`

**Step 1: Write the failing default and persistence tests**

Create table-driven tests covering:

```go
func TestOpenMissingSettingsDefaultsToDisabled(t *testing.T) {
    store := Open(filepath.Join(privateTempDir(t), "settings.json"))
    got := store.Snapshot()
    if got.ExperimentalPlatformCompatibilityEnabled || got.PlatformNoticeVersion != "" {
        t.Fatalf("Snapshot() = %#v, want disabled", got)
    }
}

func TestSetPlatformCompatibilityPersistsSecurely(t *testing.T) {
    path := filepath.Join(privateTempDir(t), "settings.json")
    store := Open(path)
    got, err := store.SetPlatformCompatibility(true, CurrentPlatformNoticeVersion)
    if err != nil || !got.ExperimentalPlatformCompatibilityEnabled {
        t.Fatalf("SetPlatformCompatibility() = %#v, %v", got, err)
    }
    reopened := Open(path)
    if !reopened.Snapshot().ExperimentalPlatformCompatibilityEnabled {
        t.Fatal("enabled setting did not persist")
    }
    assertMode(t, path, 0o600)
}
```

Also test:

- `CurrentPlatformNoticeVersion` is a fixed bounded ASCII value such as `2026-07-28-v1`;
- enabling rejects an empty, stale, oversized or control-character notice version;
- disabling clears the persisted acknowledgment version;
- a missing file does not create anything until the first update;
- malformed JSON safely reports `Enabled == false`;
- malformed regular files can be repaired by an explicit valid update;
- symlinks, non-regular paths, insecure file permissions and insecure parent directories never enable compatibility;
- writes use a same-directory random temporary file, mode `0600`, file sync, atomic rename and parent-directory sync;
- short writes and sync/rename failures leave the last valid file intact;
- concurrent readers and writers are race-free and always return complete snapshots;
- the settings JSON rejects unknown fields and multiple JSON values;
- no temporary artifacts remain after success or failure.

**Step 2: Run the focused test and verify it fails**

Run from `helper/`:

```bash
go test ./internal/settings -run 'TestOpenMissing|TestSetPlatform' -count=1
```

Expected: FAIL because `helper/internal/settings` does not exist.

**Step 3: Implement the minimal store**

Use this public surface:

```go
package settings

const CurrentPlatformNoticeVersion = "2026-07-28-v1"

type Snapshot struct {
    ExperimentalPlatformCompatibilityEnabled bool   `json:"experimental_platform_compatibility_enabled"`
    PlatformNoticeVersion                    string `json:"platform_notice_version,omitempty"`
}

type Store struct {
    mu       sync.RWMutex
    path     string
    snapshot Snapshot
    loadErr  error
}

func Open(path string) *Store
func PathForConfig(configPath string) (string, error)
func (s *Store) Snapshot() Snapshot
func (s *Store) Enabled() bool
func (s *Store) SetPlatformCompatibility(enabled bool, noticeVersion string) (Snapshot, error)
```

`Open` must fail closed: it returns a usable in-memory store even when the existing settings file is malformed, but `Snapshot()` must remain disabled. A valid explicit `SetPlatformCompatibility` may replace a malformed regular `0600` file; it must never follow or replace a symlink. `PathForConfig` returns `settings.json` next to the resolved config file so `--config` test instances remain isolated.

**Step 4: Run package tests and the race detector**

Run from `helper/`:

```bash
go test ./internal/settings -count=1
go test -race ./internal/settings -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add helper/internal/settings/store.go helper/internal/settings/store_test.go
git commit -m "feat: persist disabled-by-default platform settings"
```

### Task 2: Expose paired settings API with versioned acknowledgment

**Files:**
- Modify: `helper/internal/api/server.go`
- Modify: `helper/internal/api/server_test.go`

**Step 1: Write failing API tests**

Add a test settings fake implementing:

```go
type SettingsService interface {
    Snapshot() settings.Snapshot
    SetPlatformCompatibility(bool, string) (settings.Snapshot, error)
}
```

Cover these requests:

```text
GET /v1/settings
PUT /v1/settings/platform-compatibility
```

The authenticated GET response must be exactly:

```json
{
  "experimentalPlatformCompatibilityEnabled": false,
  "platformNoticeVersion": "",
  "currentPlatformNoticeVersion": "2026-07-28-v1"
}
```

The PUT request shape must be strict:

```json
{
  "enabled": true,
  "acknowledged": true,
  "noticeVersion": "2026-07-28-v1"
}
```

Test that enabling fails before calling the store when `acknowledged` is false or `noticeVersion` is stale. Test that disabling accepts `{ "enabled": false }`, clears the notice version, is idempotent, and requires authentication. Test content type, body-size, unknown-field, multiple-JSON, CORS preflight and method restrictions. Test that store failures return only a fixed `settings_unavailable` message and never expose a path or raw error.

**Step 2: Run focused server tests and verify failure**

Run from `helper/`:

```bash
go test ./internal/api -run 'TestSettings|TestPreflight' -count=1
```

Expected: FAIL with 404 or missing `Options.Settings`.

**Step 3: Implement the settings routes**

Add `Settings SettingsService` to `api.Options`, reject a nil dependency in `New`, store it on `Server`, and update `allowedMethods`/`routeV1`.

Use response-only camelCase DTOs so the on-disk snake_case representation never leaks into the browser contract. When enabling, require `acknowledged == true` and an exact match to `settings.CurrentPlatformNoticeVersion`. When disabling, ignore acknowledgment fields only if they are absent; strict JSON must continue rejecting unknown fields.

Add stable errors:

```text
invalid_acknowledgment -> 400 -> 请先阅读并确认实验性平台兼容使用边界
notice_outdated        -> 409 -> 使用提示已更新，请重新阅读后确认
settings_unavailable   -> 500 -> 无法保存本地设置
```

**Step 4: Run API tests**

Run from `helper/`:

```bash
go test ./internal/api -run 'TestSettings|TestPreflight|TestNew' -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add helper/internal/api/server.go helper/internal/api/server_test.go
git commit -m "feat: expose paired platform compatibility settings"
```

### Task 3: Gate platform-page tasks and retries in the Go engine

**Files:**
- Create: `helper/internal/platformscope/scope.go`
- Create: `helper/internal/platformscope/scope_test.go`
- Modify: `helper/internal/api/engine.go`
- Modify: `helper/internal/api/engine_test.go`

**Step 1: Write failing exact-host classification tests**

Implement tests for `platformscope.IsExperimentalPage(rawURL string) bool`.

Accepted page hosts:

```text
youtube.com and subdomains
youtu.be
bilibili.com and subdomains
weixin.qq.com and subdomains
wechat.com and subdomains
```

Reject HTTP URLs, credentials, ports, Unicode/lookalike hosts, suffix-confusion hosts such as `youtube.com.example`, malformed URLs, fragments containing fake hosts and unrelated CDNs. Do not classify media URLs by path or query text.

**Step 2: Write failing engine gate tests**

Extend `JobSpec` with an in-memory-only source context:

```go
type JobSpec struct {
    URL       string `json:"url"`
    PageURL   string `json:"pageUrl,omitempty"`
    Title     string `json:"title"`
    MediaType string `json:"mediaType"`
    Quality   string `json:"quality,omitempty"`
}
```

Add a narrow provider to `engineDeps` and `Engine`:

```go
type platformCompatibility interface {
    Enabled() bool
}
```

Test that a disabled provider rejects, before creating a manager task or runner:

- every `mediaType: platform` job;
- generic MP4/HLS jobs whose `PageURL` is a classified experimental platform page;
- retry of a previously failed/canceled experimental-platform job after the setting is turned off.

Test that ordinary MP4/HLS from `https://example.com/watch` still works while disabled. Test that enabling allows the existing strict platform URL flow. Test that a running task is not canceled merely because the setting later changes.

**Step 3: Run focused tests and verify failure**

Run from `helper/`:

```bash
go test ./internal/platformscope ./internal/api -run 'TestExperimental|TestEngine.*Compatibility' -count=1
```

Expected: FAIL because the classifier and engine provider do not exist.

**Step 4: Implement the classifier and fail-closed engine checks**

Add a safe error type:

```go
type PlatformCompatibilityDisabledError struct{}

func (*PlatformCompatibilityDisabledError) Error() string {
    return "experimental platform compatibility is disabled"
}
func (*PlatformCompatibilityDisabledError) SafeMessage() string {
    return "实验性平台兼容尚未开启"
}
```

Check the gate after validating media type and page URL shape but before canonicalizing platform URLs, probing dependencies, creating tasks or launching workers. Recheck in `Retry` before creating the retry task. Store `PageURL` only in the engine's in-memory `specs` map; do not add it to `tasks.Task`, logs or output filenames.

Map the safe error in `server.go` to code `platform_compatibility_disabled` and HTTP 409 before the generic safe-message branch.

**Step 5: Run engine, URL and race tests**

Run from `helper/`:

```bash
go test ./internal/platformscope ./internal/api -count=1
go test -race ./internal/api -count=1
```

Expected: PASS.

**Step 6: Commit**

```bash
git add helper/internal/platformscope helper/internal/api/engine.go helper/internal/api/engine_test.go helper/internal/api/server.go helper/internal/api/server_test.go
git commit -m "feat: gate experimental platform jobs in helper"
```

### Task 4: Wire settings into helper startup and health-safe lifecycle

**Files:**
- Modify: `helper/cmd/web-video-harbor-helper/main.go`
- Modify: `helper/cmd/web-video-harbor-helper/main_test.go`
- Modify: `tests/integration/helper_test.go`

**Step 1: Write failing startup tests**

Update `appDeps` so tests can inject `openSettings`. Verify:

- the settings path is adjacent to the selected config path;
- first start passes a disabled store to both `NewEngine` and `api.New`;
- an existing enabled settings file is respected;
- malformed settings start fail closed instead of enabling platform work;
- `--print-token` exits before probing or changing settings;
- startup output does not print consent state, settings path or notice version;
- shutdown keeps the settings store usable until HTTP and engine shutdown complete.

**Step 2: Run focused tests and verify failure**

Run from `helper/`:

```bash
go test ./cmd/web-video-harbor-helper -run 'Test.*Settings|TestRunPrintToken' -count=1
```

Expected: FAIL because startup does not construct the store.

**Step 3: Implement wiring**

After loading `config.json`, derive and open `settings.json`. Pass the same concurrency-safe store to:

```go
api.NewEngine(..., compatibilityStore)
api.New(api.Options{..., Settings: compatibilityStore})
```

Update production and test constructor signatures directly; do not use globals or environment-variable backdoors.

**Step 4: Run helper and integration tests**

Run from `helper/`:

```bash
go test ./... -count=1
```

Then run from `tests/integration/`:

```bash
go test ./... -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add helper/cmd/web-video-harbor-helper/main.go helper/cmd/web-video-harbor-helper/main_test.go tests/integration/helper_test.go
git commit -m "feat: wire compatibility settings into helper startup"
```

### Task 5: Add strict settings support to the extension helper client

**Files:**
- Modify: `extension/lib/helper-client.js`
- Modify: `extension/tests/helper-client.test.js`

**Step 1: Write failing normalization and request tests**

Test these new methods:

```js
const settings = await client.getSettings();
await client.setPlatformCompatibility({
  enabled: true,
  acknowledged: true,
  noticeVersion: settings.currentPlatformNoticeVersion,
});
await client.setPlatformCompatibility({ enabled: false });
```

`getSettings()` must normalize malformed or missing properties to disabled and accept only the exact bounded notice-version pattern. The PUT method must construct the exact body, use authentication, and refuse a local enable call without explicit acknowledgment or a valid notice version.

Add fixed messages for:

```text
platform_compatibility_disabled
invalid_acknowledgment
notice_outdated
settings_unavailable
```

Ensure server-provided message text, paths and URLs are never surfaced.

**Step 2: Run focused test and verify failure**

```bash
node --test extension/tests/helper-client.test.js
```

Expected: FAIL because the methods do not exist.

**Step 3: Implement minimal client methods**

Add pure `normalizeSettings` and reuse the existing authenticated `request` function. Keep `BASE_URL` fixed and do not mirror the consent flag to `chrome.storage.local`; the helper is the authoritative source.

**Step 4: Run helper-client and syntax tests**

```bash
node --test extension/tests/helper-client.test.js
node --check extension/lib/helper-client.js
```

Expected: PASS.

**Step 5: Commit**

```bash
git add extension/lib/helper-client.js extension/tests/helper-client.test.js
git commit -m "feat: add extension client for compatibility settings"
```

### Task 6: Build the accessible opt-in settings experience

**Files:**
- Create: `extension/lib/platform-settings.js`
- Create: `extension/tests/platform-settings.test.js`
- Modify: `extension/options.html`
- Modify: `extension/options.js`
- Modify: `extension/popup.css`
- Modify: `extension/tests/popup-ui.test.js`
- Modify: `extension/manifest.json`

**Step 1: Write failing controller tests**

Create a pure controller with injected client and view:

```js
const controller = createPlatformSettingsController({ client, view });
await controller.load();
controller.requestEnable();
await controller.confirmEnable();
await controller.disable();
```

Test:

- initial loading renders the helper's disabled state;
- requesting enable shows the notice but does not call PUT;
- cancel leaves the setting disabled;
- confirm sends the current notice version from GET, never a hard-coded stale value;
- UI shows enabled only after PUT succeeds;
- failed PUT restores the authoritative old state and a short error;
- disable is immediate, idempotent and requires no acknowledgment;
- reopening options reads helper state instead of browser cache;
- concurrent clicks coalesce so older responses cannot overwrite newer state.

**Step 2: Run focused tests and verify failure**

```bash
node --test extension/tests/platform-settings.test.js extension/tests/popup-ui.test.js
```

Expected: FAIL because the controller and markup are missing.

**Step 3: Implement controller and accessible markup**

Add an “实验性平台兼容” card below pairing. Use a real checkbox or switch with a visible status, and an accessible `<dialog>` with:

```text
此兼容功能仅用于技术研究，以及处理您拥有版权、已获授权或平台明确允许下载的内容。
请勿用于会员、付费、私有、加密、DRM 内容，也不要规避登录、地区或访问限制。
```

Buttons: `取消` and `我已了解并继续`. Escape/cancel must not enable. Disable the control while saving. If the helper is unpaired or unreachable, show the setting as unavailable and disabled rather than relying on stale Chrome storage.

Load `lib/platform-settings.js` before `options.js`. Add it to the packager whitelist later in Task 10. Do not add new extension permissions.

**Step 4: Run all extension unit tests**

```bash
node --test extension/tests/*.test.js
node --check extension/options.js
node --check extension/lib/platform-settings.js
```

Expected: PASS.

**Step 5: Commit**

```bash
git add extension/lib/platform-settings.js extension/tests/platform-settings.test.js extension/options.html extension/options.js extension/popup.css extension/tests/popup-ui.test.js extension/manifest.json
git commit -m "feat: require explicit platform compatibility consent"
```

### Task 7: Hide experimental page candidates while disabled

**Files:**
- Modify: `extension/lib/platform.js`
- Modify: `extension/lib/popup-state.js`
- Modify: `extension/lib/popup-controller.js`
- Modify: `extension/popup.js`
- Modify: `extension/tests/platform.test.js`
- Modify: `extension/tests/popup-state.test.js`
- Modify: `extension/tests/popup-controller.test.js`
- Modify: `extension/tests/popup-ui.test.js`
- Modify: `extension/tests/wiring.test.js`

**Step 1: Write failing platform-page and popup tests**

Add `isExperimentalPlatformPage(value)` to the JavaScript platform library with the same exact-host set as the Go classifier. Test malformed, credentialed, ported and suffix-confusion URLs.

Test popup behavior:

- on an ordinary page while disabled, captured MP4/HLS candidates remain visible;
- on YouTube, Bilibili or WeChat pages while disabled, neither synthetic platform cards nor captured generic media candidates are shown;
- the empty state says “实验性平台兼容尚未开启，可在设置中阅读说明后开启”;
- while enabled, the existing canonical YouTube/Bilibili card and captured WeChat MP4/HLS candidates remain available;
- if settings cannot be read, fail closed for experimental pages but do not hide ordinary-page media;
- refreshing/rescanning re-reads authoritative helper settings;
- every submitted task includes `pageUrl` from the normalized candidate or trusted current tab, while titles and media URLs retain existing bounds;
- `platform_compatibility_disabled` is rendered as a fixed short message if a stale popup attempts a task after the switch was closed.

**Step 2: Run focused tests and verify failure**

```bash
node --test extension/tests/platform.test.js extension/tests/popup-state.test.js extension/tests/popup-controller.test.js extension/tests/popup-ui.test.js
```

Expected: FAIL because popup candidate discovery is not gated.

**Step 3: Implement official-extension gating**

In `popup.js`, read helper settings with a safe disabled fallback, classify the trusted active tab URL, and filter the entire candidate list only when the active page is experimental and compatibility is off. Prepend the canonical platform page candidate only while enabled.

Return a bounded flag such as `experimentalPlatformBlocked` from `bridge.getTabMedia()` so `popup-state.js` can render the correct empty message. In `popup-controller.js`, include:

```js
pageUrl: candidate.pageUrl || model.pageUrl
```

in `createTask` specs. Do not persist page URLs in Chrome local storage beyond the existing bounded session candidate store.

**Step 4: Run all extension tests and syntax checks**

```bash
node --test extension/tests/*.test.js
node --check extension/popup.js
node --check extension/lib/platform.js
node --check extension/lib/popup-controller.js
node --check extension/lib/popup-state.js
```

Expected: PASS.

**Step 5: Commit**

```bash
git add extension/lib/platform.js extension/lib/popup-state.js extension/lib/popup-controller.js extension/popup.js extension/tests/platform.test.js extension/tests/popup-state.test.js extension/tests/popup-controller.test.js extension/tests/popup-ui.test.js extension/tests/wiring.test.js
git commit -m "feat: hide experimental media until locally enabled"
```

### Task 8: Verify extension-to-helper enforcement end to end

**Files:**
- Modify: `tests/integration/extension_helper_smoke.cjs`
- Modify: `tests/integration/helper_test.go`
- Modify: `tests/integration/chrome_extension_smoke.mjs`

**Step 1: Write failing integration cases**

Extend the fake platform runner to record invocations. Test through the HTTP API:

1. GET settings reports disabled.
2. Ordinary MP4 task on an ordinary page succeeds while disabled.
3. Platform task and WeChat-page generic MP4 task return `platform_compatibility_disabled` and never invoke the fake runner/downloader.
4. Stale or unacknowledged enable requests fail.
5. A current acknowledged enable succeeds.
6. The same platform and WeChat-page jobs enter the existing controlled flows.
7. Disabling again prevents new jobs and retries.

The Chrome smoke test must open the options page, confirm the switch is initially off, verify a platform-page popup has no platform card, enable with the dialog, reopen the popup and verify the card appears, then disable again.

**Step 2: Run focused integrations and verify failure**

Run from `tests/integration/`:

```bash
go test ./... -run 'Test.*Compatibility|TestExtensionHelper' -count=1
```

Then from repository root:

```bash
node --test tests/integration/chrome_launch.test.mjs
```

Expected: FAIL until both sides are wired.

**Step 3: Add only the fixtures and hooks needed by the tests**

Keep all fake media and fake yt-dlp artifacts under `work/` or existing test fixture directories. Do not contact real platforms in automated tests and do not use accounts, cookies or protected content.

**Step 4: Run integration suites**

From `tests/integration/`:

```bash
go test ./... -count=1
```

From repository root:

```bash
node --test tests/integration/chrome_launch.test.mjs
```

Expected: PASS. If Chrome is unavailable, record the exact environment skip and retain deterministic helper/extension integration coverage.

**Step 5: Commit**

```bash
git add tests/integration/extension_helper_smoke.cjs tests/integration/helper_test.go tests/integration/chrome_extension_smoke.mjs
git commit -m "test: cover compatibility consent end to end"
```

### Task 9: Add MIT license and rewrite community documentation

**Files:**
- Create: `LICENSE`
- Create: `PRIVACY.md`
- Create: `docs/使用边界.md`
- Modify: `README.md`
- Modify: `docs/安装使用说明.md`
- Modify: `THIRD_PARTY_NOTICES.md`
- Modify: `docs/plans/2026-07-27-webvideoharbor-commercialization-design.md`
- Modify: `docs/plans/2026-07-27-webvideoharbor-v0.3-commercial-editions.md`
- Modify: `tests/repository_branding.test.mjs`
- Modify: `tests/scripts/macos_scripts_test.zsh`

**Step 1: Write failing repository policy tests**

Change the branding test to assert:

- root `LICENSE` contains the unmodified MIT grant and `Copyright (c) 2026 PHPfrank`;
- manifest and README identify `1.0.0` and `Community Edition`;
- README describes a free, open-source, local technical project without Pro, activation, payment or success-rate claims;
- README foregrounds MP4, WebM and non-encrypted M3U8/HLS;
- platform names appear only in compatibility/boundary/troubleshooting sections, not in the title or opening marketing description;
- README links `LICENSE`, `PRIVACY.md`, `docs/使用边界.md`, installation guide and third-party notices;
- privacy documentation states no upload, analytics, Cookie reading or account access;
- boundary documentation states the experimental module is off by default and excludes login, membership, paid, private, encrypted, DRM, region bypass and bot-verification bypass;
- both old commercial plans start with a clear superseded notice pointing to the 2026-07-28 design;
- no production source or current docs contain activation keys, Pro gates, payment links or remote licensing endpoints.

**Step 2: Run policy tests and verify failure**

```bash
node --test tests/repository_branding.test.mjs
```

Expected: FAIL because `LICENSE`, privacy and boundary docs do not exist.

**Step 3: Write the license and documents**

Use the standard MIT text without custom restrictions. The usage boundary belongs in documentation, not as a restriction inserted into the MIT license.

Rewrite the opening README approximately as:

```markdown
# WebVideoHarbor（网页视频港）

WebVideoHarbor 是一个完全免费、开源、本地运行的网页媒体技术项目，
用于学习网页媒体识别、非加密 HLS 分片处理，以及浏览器扩展与 macOS 本地助手通信。
```

Document the experimental switch accurately in a later “兼容性与使用边界” section. Do not conceal bundled yt-dlp/Deno. State that free/open source and learning purpose do not grant permission to download third-party works.

Update `THIRD_PARTY_NOTICES.md` to distinguish WebVideoHarbor's MIT license from every bundled component and provide the pinned upstream source/version/license references. Do not claim that MIT covers yt-dlp's bundled third-party components.

**Step 4: Run docs and branding checks**

```bash
node --test tests/repository_branding.test.mjs
zsh tests/scripts/macos_scripts_test.zsh
zsh scripts/verify-doc-commands.zsh
```

Expected: PASS. If the script suite still expects 0.2.1, defer only those version assertions to Task 10; all content checks must pass now.

**Step 5: Commit**

```bash
git add LICENSE PRIVACY.md docs/使用边界.md README.md docs/安装使用说明.md THIRD_PARTY_NOTICES.md docs/plans/2026-07-27-webvideoharbor-commercialization-design.md docs/plans/2026-07-27-webvideoharbor-v0.3-commercial-editions.md tests/repository_branding.test.mjs tests/scripts/macos_scripts_test.zsh
git commit -m "docs: publish MIT community edition boundaries"
```

### Task 10: Bump every product and package surface to v1.0.0

**Files:**
- Create: `VERSION`
- Modify: `extension/manifest.json`
- Modify: `scripts/build-macos.zsh`
- Modify: `scripts/package-macos.zsh`
- Modify: `scripts/verify-doc-commands.zsh`
- Modify: `tests/scripts/macos_scripts_test.zsh`
- Modify: `tests/scripts/package_macos_test.zsh`
- Modify: `tests/repository_branding.test.mjs`
- Modify: `helper/internal/api/engine.go`
- Modify: `helper/internal/api/engine_test.go`
- Modify: `helper/internal/ytdlp/runner.go`

**Step 1: Update tests to expect one version source**

Add a root plain-text `VERSION` file to remove duplicated production version literals:

```text
1.0.0
```

Modify all build/package tests to reject whitespace, extra lines and invalid semantic versions. Let scripts read it from the repository root. Keep `main.version = "dev"` for non-release Go builds and inject `1.0.0` through ldflags.

Update expected archive name to:

```text
WebVideoHarbor-macOS-v1.0.0.zip
```

Update the extension packager whitelist for `lib/platform-settings.js`, and package root entries for `LICENSE`, `PRIVACY.md` and `docs/使用边界.md`.

**Step 2: Run focused tests and verify failure**

```bash
node --test tests/repository_branding.test.mjs
WEB_VIDEO_HELPER_FOCUSED_CASE=version zsh tests/scripts/macos_scripts_test.zsh
zsh tests/scripts/package_macos_test.zsh
```

Expected: FAIL on old 0.2.1 literals and missing package entries.

**Step 3: Implement the version and package updates**

Replace user-facing `v0.2.1` error messages in `engine.go` and `runner.go` with version-neutral wording such as “当前版本不读取登录信息”. Do not change pinned yt-dlp `2026.07.04` or Deno `2.8.1` unless a separate dependency review requires it.

Ensure the packager:

- refuses to overwrite an existing v1.0.0 archive;
- copies root MIT `LICENSE` into the package root;
- includes privacy and boundary docs;
- includes the new extension controller file and no unexpected files;
- still copies Go, yt-dlp and Deno licenses separately;
- verifies unpacked helper version `1.0.0` and extension manifest `1.0.0`;
- rebuilds the unpacked source deterministically;
- retains archive reproducibility and safe path checks.

**Step 4: Run build, script and package suites**

```bash
node --test tests/repository_branding.test.mjs
zsh tests/scripts/macos_scripts_test.zsh
zsh tests/scripts/package_macos_test.zsh
```

Expected: PASS and a test-only archive under `work/`, never `outputs/`.

**Step 5: Commit**

```bash
git add VERSION extension/manifest.json scripts/build-macos.zsh scripts/package-macos.zsh scripts/verify-doc-commands.zsh tests/scripts/macos_scripts_test.zsh tests/scripts/package_macos_test.zsh tests/repository_branding.test.mjs helper/internal/api/engine.go helper/internal/api/engine_test.go helper/internal/ytdlp/runner.go
git commit -m "release: prepare v1.0.0 community edition"
```

### Task 11: Run the complete deterministic verification matrix

**Files:**
- Modify only tests or production files required by a demonstrated failure; do not opportunistically refactor.

**Step 1: Run formatting and static checks**

```bash
gofmt -w helper/internal/settings/*.go helper/internal/platformscope/*.go helper/internal/api/*.go helper/cmd/web-video-harbor-helper/*.go tests/integration/*.go
git diff --check
node --check extension/background.js
node --check extension/content.js
node --check extension/options.js
node --check extension/popup.js
node --check extension/lib/helper-client.js
node --check extension/lib/platform-settings.js
node --check extension/lib/platform.js
node --check extension/lib/popup-controller.js
node --check extension/lib/popup-state.js
```

Expected: PASS with no formatting diff after `gofmt`.

**Step 2: Run all extension and repository tests**

```bash
node --test extension/tests/*.test.js
node --test tests/repository_branding.test.mjs
node --test tests/integration/chrome_launch.test.mjs
```

Expected: PASS or a documented environment-only Chrome skip.

**Step 3: Run all Go suites with race checks on changed concurrency code**

From `helper/`:

```bash
go test ./... -count=1
go test -race ./internal/settings ./internal/api -count=1
```

From `tests/integration/`:

```bash
go test ./... -count=1
```

Expected: PASS.

**Step 4: Run all zsh suites**

```bash
zsh tests/scripts/fetch_yt_dlp_test.zsh
zsh tests/scripts/fetch_deno_test.zsh
zsh tests/scripts/macos_scripts_test.zsh
zsh tests/scripts/package_macos_test.zsh
zsh scripts/verify-doc-commands.zsh
```

Expected: PASS.

**Step 5: Review for forbidden regressions**

```bash
rg -n "Cookie|cookie|DRM|activation|activate|license key|payment|Pro" extension helper scripts README.md PRIVACY.md docs/使用边界.md
rg -n "0\.2\.1" extension helper scripts tests README.md docs/安装使用说明.md
git status --short
```

Expected:

- Cookie/DRM matches occur only in prohibitions, error boundaries or tests;
- no production activation/payment/Pro implementation exists;
- no current production/version file contains `0.2.1`;
- only intended source changes and the ignored/untracked task-local `outputs` link appear.

**Step 6: Request code review before release packaging**

Use `superpowers:requesting-code-review`. Resolve only verified findings, rerun the affected focused tests, then rerun the full matrix.

**Step 7: Commit verification-only fixes if needed**

```bash
git add <only-the-files-changed-for-a-demonstrated-failure>
git commit -m "fix: close v1 community edition verification gaps"
```

If no fixes were needed, do not create an empty commit.

### Task 12: Build and manually accept the macOS v1.0.0 package

**Files:**
- Generated: `outputs/WebVideoHarbor-macOS-v1.0.0.zip`
- Generated: `work/release/v1.0.0/`

**Step 1: Ensure centralized outputs before creating the deliverable**

```bash
/Users/frank/.codex/scripts/ensure-central-outputs.zsh "/Users/frank/Documents/Codex/2026-07-23/neng"
```

Expected: task-local `outputs/` points to the centralized dated output directory without conflict. If the helper reports a conflict, stop without overwriting either side.

**Step 2: Build the release archive**

```bash
zsh scripts/package-macos.zsh
```

Expected: creates exactly one non-overwriting `outputs/WebVideoHarbor-macOS-v1.0.0.zip`.

**Step 3: Record and verify the artifact**

```bash
/usr/bin/shasum -a 256 outputs/WebVideoHarbor-macOS-v1.0.0.zip
/usr/bin/unzip -t outputs/WebVideoHarbor-macOS-v1.0.0.zip
```

Unpack only under `work/release/v1.0.0/` and verify:

- helper `--version` prints `1.0.0`;
- extension manifest is `1.0.0`;
- MIT, Go, yt-dlp and Deno license files are present;
- no settings file is preseeded as enabled;
- no activation, payment or Pro artifacts exist;
- binary architectures and pinned dependency hashes match the package tests.

**Step 4: Perform local UI acceptance**

Stop the currently running old helper, preserve the user's real config/token, and start the unpacked v1 helper. Load the unpacked extension as a separate development extension or reload only after recording the existing path.

Accept these flows:

1. ordinary MP4 download succeeds with experimental compatibility off;
2. non-encrypted M3U8 succeeds with compatibility off;
3. supported platform pages show no platform candidate while off;
4. options displays the full notice and cancel keeps the switch off;
5. explicit confirmation turns the switch on;
6. public, login-free YouTube and Bilibili sample pages enter the existing controlled flow without Cookie access;
7. WeChat exposed MP4/HLS is visible only while enabled;
8. turning the switch off blocks new platform tasks and retries;
9. protected/login/verification/DRM cases stop with boundary-safe errors;
10. restart preserves an explicitly enabled or disabled state correctly.

Use only content the user owns, has permission to use, or a clearly authorized short test fixture. Do not use accounts, Cookies, member-only or protected media.

**Step 5: Restore the user's previous running state on failure**

If acceptance fails, stop the v1 helper, restore the previous helper path and leave the old GitHub Release untouched. Record diagnostics only under `work/release/v1.0.0/` with secrets and signed URLs removed.

**Step 6: Apply verification-before-completion**

Use `superpowers:verification-before-completion` and cite the fresh test outputs, archive path, size, SHA-256 and manual acceptance results before claiming the package is ready.

### Task 13: Publish v1.0.0, verify GitHub, then retire v0.2.1

**Files:**
- Generated: `work/release/v1.0.0/release-notes.md`
- External state: GitHub repository `PHPfrank/web-video-harbor`

**Step 1: Prepare neutral release notes**

The title is:

```text
WebVideoHarbor v1.0.0 Community Edition
```

Release notes lead with free/open-source/local media technology, default MP4/WebM/non-encrypted HLS support, the disabled-by-default experimental compatibility switch, MIT license, privacy boundary and SHA-256. Platform names may appear only in a compatibility/boundary paragraph, not the title or opening pitch.

**Step 2: Confirm clean commit and remote target**

```bash
git status --short
git log -5 --oneline
git remote -v
gh auth status
```

Expected: only the task-local untracked `outputs` link may remain; remote is the intended `PHPfrank/web-video-harbor` repository; GitHub authentication belongs to the user's intended account.

**Step 3: Ask for explicit external-write approval**

Before any push, tag, Release creation or deletion, show the user:

- branch and commits to push;
- exact v1.0.0 archive path, size and SHA-256;
- repository URL;
- exact old objects scheduled for deletion: Release `v0.2.1` and remote tag `v0.2.1`.

Do not continue without approval.

**Step 4: Push source and publish v1.0.0**

After approval:

```bash
git push origin main
git tag -a v1.0.0 -m "WebVideoHarbor v1.0.0 Community Edition"
git push origin v1.0.0
gh release create v1.0.0 outputs/WebVideoHarbor-macOS-v1.0.0.zip --title "WebVideoHarbor v1.0.0 Community Edition" --notes-file work/release/v1.0.0/release-notes.md
```

Expected: all commands succeed and the Release URL is captured.

**Step 5: Independently verify the published Release**

Download the GitHub asset into a new directory below `work/release/v1.0.0/github-download/`, not over the local artifact. Verify its SHA-256, ZIP integrity, contents, helper version, extension version and license files match the locally accepted artifact.

Expected: byte-for-byte SHA-256 match.

**Step 6: Ask for final destructive confirmation**

Even though the design chose removal, immediately before deletion restate that deleted Release assets cannot be recovered from GitHub and existing downloads/forks/caches cannot be recalled. Ask the user to confirm deletion of exactly:

```text
GitHub Release v0.2.1
remote Git tag refs/tags/v0.2.1
```

**Step 7: Delete only the confirmed old Release and remote tag**

After confirmation:

```bash
gh release delete v0.2.1 --yes
git push origin :refs/tags/v0.2.1
```

Do not rewrite commit history and do not delete unrelated local or remote tags.

**Step 8: Verify final GitHub state**

```bash
gh release view v1.0.0
gh release view v0.2.1
git ls-remote --tags origin
```

Expected:

- v1.0.0 exists with the verified asset;
- v0.2.1 Release returns not found;
- `refs/tags/v1.0.0` exists;
- `refs/tags/v0.2.1` does not exist;
- repository default branch and history remain intact.

## Final acceptance checklist

- [ ] Software is fully free; no commercial edition or activation path remains.
- [ ] Root first-party license is standard MIT with the confirmed copyright holder.
- [ ] Generic MP4/WebM/non-encrypted HLS works by default.
- [ ] Experimental platform compatibility is bundled but disabled on first run.
- [ ] Explicit current-version acknowledgment is required to enable it.
- [ ] Extension and helper both enforce the switch.
- [ ] Generic candidates captured on YouTube, Bilibili and WeChat pages cannot bypass the disabled state in the official extension.
- [ ] New and retried experimental jobs are rejected while disabled; running jobs finish deterministically.
- [ ] No Cookie, login, member, paid, private, encrypted, DRM, region-bypass or verification-bypass capability was added.
- [ ] No analytics, remote service, account system or browser-history upload was added.
- [ ] README promotion is platform-neutral while compatibility disclosures remain accurate.
- [ ] Third-party versions, licenses and source references remain complete.
- [ ] All JavaScript, Go, race, integration, zsh, packaging and documentation tests pass.
- [ ] macOS v1.0.0 archive is locally accepted and independently verified after GitHub download.
- [ ] v0.2.1 is deleted only after verified v1.0.0 publication and immediate user confirmation.
