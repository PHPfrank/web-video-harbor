# WebVideoHarbor v0.2.0 Platform Downloads Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add safe, local downloads for public, login-free YouTube and Bilibili single-video pages while preserving the existing MP4 and HLS workflows.

**Architecture:** The Chrome extension recognizes supported platform page URLs and submits the page URL plus a fixed quality enum. The Go helper validates and canonicalizes the platform URL, then invokes a pinned bundled `yt-dlp_macos` executable with fixed arguments and the existing FFmpeg installation. The runner stages all artifacts in a private task directory, publishes exactly one validated video file into the existing download directory, and converts raw extractor failures into stable Chinese errors.

**Tech Stack:** Chrome Extension Manifest V3, vanilla JavaScript with Node's built-in test runner, Go standard library, yt-dlp 2026.07.04, FFmpeg, zsh packaging scripts.

---

## Working conventions

- Work only in `.worktrees/v0.2-platform-downloads` on branch `feature/v0.2-platform-downloads`.
- Follow @superpowers:test-driven-development for every production change: red test, smallest implementation, green test, commit.
- Do not add Cookie access, playlist support, generic yt-dlp URLs, user-supplied yt-dlp arguments, or a self-update path.
- Do not log raw yt-dlp output, signed media URLs, page query strings, or the pairing token.
- Keep generated binaries, fake executables, caches, downloaded fixtures, and live-test media under `work/`.
- Before producing the release ZIP, run `/Users/frank/.codex/scripts/ensure-central-outputs.zsh "/Users/frank/Documents/Codex/2026-07-23/neng"`; user-facing artifacts go only through `outputs/`.
- The existing full smoke test needs `127.0.0.1:17432`. Temporarily stop the installed v0.1.0 helper immediately before that test and restart the same installed helper immediately afterward.

### Pinned third-party inputs

```text
YTDLP_VERSION=2026.07.04
YTDLP_MACOS_URL=https://github.com/yt-dlp/yt-dlp/releases/download/2026.07.04/yt-dlp_macos
YTDLP_MACOS_SHA256=498bd0dae17855c599d371d68ec5bafc439a9d8640e838be25c765a9792f261b
YTDLP_LICENSE_URL=https://raw.githubusercontent.com/yt-dlp/yt-dlp/2026.07.04/THIRD_PARTY_LICENSES.txt
YTDLP_LICENSE_SHA256=b085c65586a953cdb4b13c6390d63ec984d66912e4b6a19e66ba3582f2ed104b
```

## Task 1: Add the helper's platform URL trust boundary

**Files:**

- Create: `helper/internal/platformurl/platform.go`
- Create: `helper/internal/platformurl/platform_test.go`

**Step 1: Write the failing table test**

Cover these accepted inputs and canonical results:

```go
tests := []struct {
    raw, provider, canonical string
}{
    {"https://www.youtube.com/watch?v=_mVb1D8wHxg", "youtube", "https://www.youtube.com/watch?v=_mVb1D8wHxg"},
    {"https://youtube.com/shorts/abc_123-XYZ", "youtube", "https://www.youtube.com/shorts/abc_123-XYZ"},
    {"https://youtu.be/abc_123-XYZ?t=4", "youtube", "https://youtu.be/abc_123-XYZ"},
    {"https://www.bilibili.com/video/BV1K3Gz6pEoo/?spm_id_from=x", "bilibili", "https://www.bilibili.com/video/BV1K3Gz6pEoo"},
    {"https://www.bilibili.com/video/av170001?p=2", "bilibili", "https://www.bilibili.com/video/av170001?p=2"},
}
```

Also reject HTTP, credentials, ports, Unicode/lookalike hosts, suffix-confusion hosts, empty or malformed IDs, YouTube playlist/channel/search/live pages, Bilibili lists/bangumi/live pages, fragments carrying fake IDs, and excessive URLs.

**Step 2: Run the focused test and verify red**

Run:

```bash
cd helper && go test ./internal/platformurl -run TestClassify -v
```

Expected: FAIL because the package does not exist.

**Step 3: Implement the minimal classifier**

Expose only the trusted result:

```go
type Provider string

const (
    YouTube  Provider = "youtube"
    Bilibili Provider = "bilibili"
)

type Video struct {
    Provider     Provider
    CanonicalURL string
}

func Classify(raw string) (Video, error)
```

Use `url.ParseRequestURI`, require `https`, reject `User`, `Port`, and non-exact hosts, validate IDs with anchored ASCII regular expressions, discard tracking query parameters, and preserve only numeric Bilibili `p` when present. Do not follow redirects or make network requests in this package.

**Step 4: Run tests and verify green**

Run:

```bash
cd helper && go test ./internal/platformurl -v
```

Expected: PASS.

**Step 5: Commit**

```bash
git add helper/internal/platformurl
git commit -m "feat: validate supported platform video URLs"
```

## Task 2: Add the extension's platform-page classifier

**Files:**

- Create: `extension/lib/platform.js`
- Create: `extension/tests/platform.test.js`
- Modify: `extension/popup.html:56-59`
- Modify: `extension/tests/wiring.test.js`

**Step 1: Write failing JavaScript tests**

Test `candidateForPage({url, title})` with the same accepted and rejected URL classes as the Go classifier. Assert that accepted results contain only normalized page information:

```js
assert.deepEqual(platform.candidateForPage({
  url: 'https://www.youtube.com/watch?v=_mVb1D8wHxg&list=PLignored',
  title: 'Demo - YouTube',
}), {
  url: 'https://www.youtube.com/watch?v=_mVb1D8wHxg',
  kind: 'platform',
  provider: 'youtube',
  title: 'Demo - YouTube',
});
```

Reject playlist-only URLs. For a watch URL containing a `list` parameter, strip the playlist parameter and keep the current video.

**Step 2: Run red tests**

Run:

```bash
node --test extension/tests/platform.test.js extension/tests/wiring.test.js
```

Expected: FAIL because `platform.js` is missing and not wired.

**Step 3: Implement the UMD-style module**

Follow the existing `extension/lib/media.js` module pattern and export:

```js
return Object.freeze({ classifyPlatformUrl, candidateForPage, QUALITY_OPTIONS });
```

Use the fixed options:

```js
const QUALITY_OPTIONS = Object.freeze([
  { value: 'best', label: '最佳画质' },
  { value: '1080', label: '1080P' },
  { value: '720', label: '720P' },
]);
```

Load `lib/platform.js` before popup state/controller scripts in `popup.html`. Do not add it as a content script; platform recognition belongs to the popup and uses only the active tab URL/title.

**Step 4: Run green tests and syntax check**

Run:

```bash
node --test extension/tests/platform.test.js extension/tests/wiring.test.js
node --check extension/lib/platform.js
```

Expected: PASS.

**Step 5: Commit**

```bash
git add extension/lib/platform.js extension/tests/platform.test.js extension/popup.html extension/tests/wiring.test.js
git commit -m "feat: recognize YouTube and Bilibili video pages"
```

## Task 3: Extend the authenticated API contract and health response

**Files:**

- Modify: `helper/internal/api/engine.go:24-31`
- Modify: `helper/internal/api/server.go:104-159,193-199,324-350`
- Modify: `helper/internal/api/server_test.go`
- Modify: `extension/lib/helper-client.js`
- Modify: `extension/tests/helper-client.test.js`

**Step 1: Write failing API tests**

Add strict JSON contract cases:

```go
JobSpec{URL: youtubeURL, Title: "demo", MediaType: "platform", Quality: "best"}
JobSpec{URL: bilibiliURL, Title: "demo", MediaType: "platform", Quality: "1080"}
```

Reject platform jobs with a missing/unknown quality, non-platform jobs with a quality, and requests containing extra fields such as `cookies`, `headers`, `arguments`, `provider`, or `playlist`.

Assert `/health` returns:

```json
{
  "ready": true,
  "version": "0.2.0",
  "ffmpeg": true,
  "platformDownloader": {"available": true, "version": "2026.07.04"},
  "pid": 123
}
```

The client test must treat malformed or missing `platformDownloader` as unavailable without exposing paths.

**Step 2: Run red tests**

Run:

```bash
cd helper && go test ./internal/api -run 'Test.*(Health|Create)' -v
node --test extension/tests/helper-client.test.js
```

Expected: FAIL on the new fields and media type.

**Step 3: Implement the minimal contract**

Add `Quality string` to `JobSpec`. Add `PlatformDownloaderAvailable` and `PlatformDownloaderVersion` to `api.Options` and an immutable status struct to `Server`. Keep health unauthenticated but return only availability and a bounded version string.

In `handleCreate`, enforce:

```go
switch spec.MediaType {
case "mp4", "hls":
    valid = spec.Quality == ""
case "platform":
    valid = spec.Quality == "best" || spec.Quality == "1080" || spec.Quality == "720"
default:
    valid = false
}
```

Do not trust a provider value from the extension; the engine will call `platformurl.Classify`.

**Step 4: Run tests**

Run the same commands as Step 2. Expected: PASS.

**Step 5: Commit**

```bash
git add helper/internal/api/engine.go helper/internal/api/server.go helper/internal/api/server_test.go extension/lib/helper-client.js extension/tests/helper-client.test.js
git commit -m "feat: add platform task API contract"
```

## Task 4: Build deterministic yt-dlp arguments and progress parsing

**Files:**

- Create: `helper/internal/ytdlp/runner.go`
- Create: `helper/internal/ytdlp/runner_test.go`

**Step 1: Write failing pure-unit tests**

Test the exact format selectors:

```go
var selectors = map[Quality]string{
    QualityBest: "bv*+ba/b",
    Quality1080: "bv*[height<=1080]+ba/b[height<=1080]",
    Quality720:  "bv*[height<=720]+ba/b[height<=720]",
}
```

Assert every command contains fixed safety arguments and never contains cookie/config/update/playlist expansion flags:

```text
--ignore-config
--no-playlist
--max-downloads 1
--newline
--no-colors
--progress
--progress-template download:WVH_PROGRESS:%(progress._percent_str)s
--merge-output-format mp4/mkv
--ffmpeg-location <validated path>
--paths home:<private staging dir>
--output media.%(ext)s
```

Test progress parsing for whitespace and decimals, monotonic clamping to 0-99, ignored non-prefixed lines, and a bounded line length.

**Step 2: Verify red**

Run:

```bash
cd helper && go test ./internal/ytdlp -run 'Test(BuildArgs|ParseProgress)' -v
```

Expected: FAIL because the package does not exist.

**Step 3: Add the minimal public runner contract and pure helpers**

```go
type Quality string
type Progress struct { Percent float64 }
type ProgressFunc func(Progress)

type Config struct {
    BinaryPath string
    FFmpegPath string
    OutputDir  string
    OnProgress ProgressFunc
}

type Request struct {
    URL, Title string
    Quality    Quality
}

func New(Config) (*Runner, error)
func (r *Runner) Run(context.Context, Request) (string, error)
```

Keep command construction private and parameter-array based. No shell strings.

**Step 4: Run green tests**

Run the command from Step 2. Expected: PASS.

**Step 5: Commit**

```bash
git add helper/internal/ytdlp
git commit -m "feat: define safe yt-dlp invocation"
```

## Task 5: Execute, cancel, classify, clean, and publish platform downloads

**Files:**

- Modify: `helper/internal/ytdlp/runner.go`
- Modify: `helper/internal/ytdlp/runner_test.go`
- Modify: `helper/internal/output/name.go` only if a small reusable copy-to-reservation helper is required
- Modify: `helper/internal/output/name_test.go` if the output helper changes

**Step 1: Add failing helper-process tests**

Use the Go test binary as a fake yt-dlp child process rather than a shell script. Select behavior with a test-only environment variable and cover:

- success writes exactly one `media.mp4`, prints multiple `WVH_PROGRESS` lines, and results in a private published output;
- an existing output or symlink is never overwritten;
- two concurrent same-title downloads receive unique names;
- multiple final files, a symlink, a directory, zero-byte output, or an unsupported extension is rejected;
- cancellation terminates the child process group and removes `.part`, temporary audio/video, and the staging directory;
- the runner waits for stdout/stderr readers before returning;
- stderr storage is bounded and never returned verbatim;
- login, payment/private, geo restriction, extractor/update, FFmpeg missing, network, and generic failures map to stable codes.

Define stable codes such as:

```go
const (
    CodeCanceled       Code = "canceled"
    CodeLoginRequired  Code = "login_required"
    CodeAccessLimited  Code = "access_limited"
    CodeGeoRestricted  Code = "geo_restricted"
    CodeExtractor      Code = "extractor_outdated"
    CodeFFmpegMissing  Code = "ffmpeg_missing"
    CodeNetwork        Code = "network"
    CodeOutput         Code = "output"
    CodeProcess        Code = "platform_process"
)
```

**Step 2: Run and verify red**

Run:

```bash
cd helper && go test ./internal/ytdlp -run 'TestRunner' -v
```

Expected: FAIL because `Run` is not implemented.

**Step 3: Implement one secure execution path**

- Create a private `.web-video-platform-*` directory directly under the configured output directory and verify it is a real directory.
- Start the fixed executable with `exec.Command`, an argument slice, a minimal inherited environment, and a new process group.
- On context cancellation, send `SIGTERM` to the group, wait briefly, then `SIGKILL` if still alive.
- Parse only `WVH_PROGRESS:` lines from stdout.
- Hold only a bounded diagnostic tail in memory; classify it, then discard it.
- After exit 0, use `Lstat`/directory enumeration to require exactly one non-empty regular `.mp4`, `.mkv`, `.m4v`, or `.webm` final file in the owned staging directory.
- Open the staged file without following a replacement path, stream it into `output.ReserveAvailablePath`, sync, publish, and remove the owned staging tree.
- If cleanup fails after publication, return `output.NewPublishedError` so the engine records the successful file.

**Step 4: Run package and race tests**

Run:

```bash
cd helper && go test -race ./internal/ytdlp ./internal/output -v
```

Expected: PASS with no leaked helper process or staging directory.

**Step 5: Commit**

```bash
git add helper/internal/ytdlp helper/internal/output
git commit -m "feat: run and publish platform downloads safely"
```

## Task 6: Integrate platform work into the task engine

**Files:**

- Modify: `helper/internal/api/engine.go:33-74,183-220,223-428`
- Modify: `helper/internal/api/engine_test.go`

**Step 1: Write failing engine tests**

Inject a fake `platformRunner` factory. Cover:

- server-supplied provider is impossible; `platformurl.Classify` decides support;
- canonical URL reaches the runner;
- quality reaches the runner unchanged after enum validation;
- progress updates remain monotonic and stop at 99 before completion;
- successful output completes the task;
- safe yt-dlp errors become the agreed Chinese messages;
- cancel and retry use the existing manager lifecycle;
- retry retains only URL, title, media type, and quality;
- MP4 and HLS behavior remains unchanged.

Expected mapping examples:

```text
login_required   -> 当前视频需要登录，v0.2.0 暂不支持
access_limited   -> 当前内容受会员、付费或私有访问限制
geo_restricted   -> 当前网络所在地区无法访问此视频
extractor_outdated -> 平台解析规则已变化，请升级网页视频港
platform_process -> 平台暂时拒绝了下载，请稍后重试
```

**Step 2: Run red tests**

Run:

```bash
cd helper && go test ./internal/api -run 'TestEngine.*Platform|TestSafeFailure.*Platform' -v
```

Expected: FAIL because the engine supports only MP4/HLS.

**Step 3: Implement the minimal engine branch**

Add a `platformRunner` interface and per-attempt factory to `engineDeps`. Extend `Start` and `run` for `MediaType == "platform"`; classify and canonicalize before task creation so invalid platform URLs fail synchronously. In `runPlatform`, transition to downloading, forward progress, run the platform downloader, handle `output.PublishedPath`, then complete through the manager.

**Step 4: Run the full helper tests**

Run:

```bash
cd helper && go test -race ./...
```

Expected: PASS.

**Step 5: Commit**

```bash
git add helper/internal/api/engine.go helper/internal/api/engine_test.go
git commit -m "feat: execute platform tasks in download engine"
```

## Task 7: Discover and report the bundled downloader

**Files:**

- Create: `helper/internal/ytdlp/probe.go`
- Create: `helper/internal/ytdlp/probe_test.go`
- Modify: `helper/cmd/web-video-harbor-helper/main.go:22-110`
- Modify: `helper/cmd/web-video-harbor-helper/main_test.go`
- Modify: `scripts/helper-status.zsh`
- Modify: `tests/scripts/macos_scripts_test.zsh`

**Step 1: Write failing probe and startup tests**

Test that production resolves `yt-dlp_macos` adjacent to `os.Executable()`, rejects symlinks/non-regular/non-executable files, runs only `--version` with a short timeout, accepts a bounded `YYYY.MM.DD` version, and passes the validated path/version into both `NewEngine` and `api.New`.

Test startup when the parser is missing: the helper still serves MP4/HLS, health reports `available: false`, and platform task creation returns a safe “安装包缺少平台解析器” error.

**Step 2: Run red tests**

Run:

```bash
cd helper && go test ./internal/ytdlp ./cmd/web-video-harbor-helper -run 'Test.*(Probe|PlatformDownloader)' -v
zsh tests/scripts/macos_scripts_test.zsh
```

Expected: FAIL on missing probe/status support.

**Step 3: Implement discovery and health wiring**

Change the production startup dependency from a generic PATH lookup to two explicit paths: FFmpeg may still use `exec.LookPath("ffmpeg")`; yt-dlp must be adjacent to the helper binary. Print only `平台解析器: 可用（版本）` or `平台解析器: 不可用`, never the path.

**Step 4: Run tests**

Run the commands from Step 2. Expected: PASS.

**Step 5: Commit**

```bash
git add helper/internal/ytdlp/probe.go helper/internal/ytdlp/probe_test.go helper/cmd/web-video-harbor-helper scripts/helper-status.zsh tests/scripts/macos_scripts_test.zsh
git commit -m "feat: discover bundled platform downloader"
```

## Task 8: Add platform cards and quality selection to the popup

**Files:**

- Modify: `extension/popup.js:3-128,212-235`
- Modify: `extension/lib/popup-controller.js:22-64,123-135,199-301`
- Modify: `extension/lib/popup-state.js:84-145`
- Modify: `extension/popup.css`
- Modify: `extension/tests/popup-controller.test.js`
- Modify: `extension/tests/popup-state.test.js`
- Modify: `extension/tests/popup-ui.test.js`

**Step 1: Write failing controller/state/UI tests**

Cover:

- `bridge.getTabMedia()` prepends at most one platform candidate derived from active `tab.url` and `tab.title`;
- media candidates remain available on the same page without duplication;
- platform card label is `YouTube` or `哔哩哔哩`, detail says `仅支持无需登录即可观看的公开视频`;
- quality select defaults to `best` and exposes exactly best/1080/720;
- download sends `{url,title,mediaType:'platform',quality:'1080'}`;
- platform card is disabled when the bundled parser or FFmpeg is unavailable;
- HLS selection continues to use variant URLs, not platform quality values;
- rescan and focus restoration still work.

**Step 2: Run red tests**

Run:

```bash
node --test extension/tests/popup-controller.test.js extension/tests/popup-state.test.js extension/tests/popup-ui.test.js
```

Expected: FAIL because platform candidates are not represented.

**Step 3: Implement a typed candidate choice path**

Keep HLS variant selection and add a separate platform-quality map. Platform candidates skip `/v1/inspect`; download uses their page URL and fixed quality. Update capability calculation to include `health.platformDownloader.available` and FFmpeg.

Render a quality `<select>` for platform cards using option values, while preserving the HLS selector's URL values. Use text content only; never insert page titles as HTML.

**Step 4: Run all extension tests and syntax checks**

Run:

```bash
node --test extension/tests/*.test.js
for file in extension/background.js extension/content.js extension/popup.js extension/options.js extension/lib/*.js; do node --check "$file"; done
```

Expected: PASS.

**Step 5: Commit**

```bash
git add extension/popup.js extension/lib/popup-controller.js extension/lib/popup-state.js extension/popup.css extension/tests
git commit -m "feat: download platform pages from extension popup"
```

## Task 9: Pin, fetch, license, and package yt-dlp

**Files:**

- Create: `third_party/yt-dlp.env`
- Create: `scripts/fetch-yt-dlp.zsh`
- Create: `tests/scripts/fetch_yt_dlp_test.zsh`
- Modify: `scripts/package-macos.zsh`
- Modify: `tests/scripts/package_macos_test.zsh`
- Modify: `THIRD_PARTY_NOTICES.md`

**Step 1: Write failing script/package tests**

Tests must prove:

- the pinned version and both SHA-256 values match the constants at the top of this plan;
- downloads use temporary files inside `work/`, fail closed on a checksum mismatch, and never overwrite a previously verified cache with a bad response;
- production URLs are fixed GitHub HTTPS URLs and cannot be overridden;
- test-only fixture injection is accepted only with `WEB_VIDEO_PACKAGE_TESTING=1` and a real source directory inside repository `work/`;
- the package contains `work/dist/yt-dlp_macos` and `licenses/yt-dlp-THIRD_PARTY_LICENSES.txt`;
- the parser is executable, non-symlink, reports `2026.07.04`, and `/usr/bin/lipo -verify_arch arm64 x86_64` succeeds;
- package validation rejects parser `.part` files, unexpected binaries, logs, credentials, or a modified license;
- `THIRD_PARTY_NOTICES.md` names yt-dlp, its version, upstream URL, and packaged license path.

Build a tiny universal fake parser in `work/` for deterministic package tests; do not access the network from automated tests.

**Step 2: Run red tests**

Run:

```bash
zsh tests/scripts/fetch_yt_dlp_test.zsh
zsh tests/scripts/package_macos_test.zsh
```

Expected: FAIL because the dependency manifest/fetcher is missing.

**Step 3: Implement verified acquisition and packaging**

The fetcher must:

1. source and validate `third_party/yt-dlp.env` against strict version/hash patterns;
2. use `curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error` with fixed URLs;
3. write random temp files under `work/vendor/`;
4. verify `shasum -a 256` before chmod or rename;
5. publish cache files without following symlinks;
6. print the verified version and paths without exposing any user state.

The package script calls the fetcher, copies the verified executable and license, updates allowlists and executable checks, and retains its current no-clobber and reproducibility guarantees.

**Step 4: Run green tests**

Run the commands from Step 2. Expected: PASS without network.

**Step 5: Commit**

```bash
git add third_party/yt-dlp.env scripts/fetch-yt-dlp.zsh tests/scripts/fetch_yt_dlp_test.zsh scripts/package-macos.zsh tests/scripts/package_macos_test.zsh THIRD_PARTY_NOTICES.md
git commit -m "build: bundle pinned yt-dlp for macOS"
```

## Task 10: Set v0.2.0 versions and update user documentation

**Files:**

- Modify: `extension/manifest.json:3-5`
- Modify: `helper/cmd/web-video-harbor-helper/main.go:22`
- Modify: `scripts/build-macos.zsh:52-60`
- Modify: `README.md`
- Modify: `docs/安装使用说明.md`
- Modify: `tests/repository_branding.test.mjs`
- Modify: `tests/scripts/macos_scripts_test.zsh`
- Modify: `tests/scripts/package_macos_test.zsh`

**Step 1: Write failing version/documentation tests**

Require extension version `0.2.0`, packaged helper output `web-video-harbor-helper 0.2.0`, and documentation that clearly distinguishes:

- supported public single-video pages;
- best/1080/720 behavior and MP4/MKV output;
- no Cookie/login/member/DRM/playlist support;
- yt-dlp bundled but not silently updated;
- upgrade steps: stop helper, replace package, reload extension, start helper, preserve existing pairing state;
- troubleshooting for parser missing/outdated and YouTube PO-token limitations.

**Step 2: Run red tests**

Run:

```bash
node --test tests/repository_branding.test.mjs
zsh tests/scripts/macos_scripts_test.zsh
zsh tests/scripts/verify-doc-commands.zsh
```

Expected: FAIL on v0.1.0/dev and missing platform documentation.

**Step 3: Implement version injection and docs**

Change `const version` to `var version = "dev"`; build release binaries with `-ldflags '-X main.version=0.2.0'`. Keep ordinary local `go test` builds at `dev`. Update the manifest description without promising universal support for every platform video.

**Step 4: Run green tests**

Run the commands from Step 2. Expected: PASS.

**Step 5: Commit**

```bash
git add extension/manifest.json helper/cmd/web-video-harbor-helper/main.go scripts/build-macos.zsh README.md docs/安装使用说明.md tests
git commit -m "docs: prepare WebVideoHarbor v0.2.0"
```

## Task 11: Extend deterministic integration and browser smoke tests

**Files:**

- Create: `tests/integration/fake_ytdlp_test.go` or a test-only helper under `tests/integration/`
- Modify: `tests/integration/helper_test.go`
- Modify: `tests/integration/chrome_extension_smoke.mjs`
- Modify: `tests/integration/extension_helper_smoke.cjs`
- Modify: `scripts/run-smoke-test.zsh`

**Step 1: Write failing end-to-end tests**

Add a fake platform downloader that creates a small, valid audio/video file with FFmpeg inside the smoke `work/` tree. Verify through the authenticated API and real Chrome popup:

- a YouTube watch tab shows a YouTube platform card without media network candidates;
- selecting 720P creates a platform task with quality `720`;
- progress moves from queued to downloading/merging/completed;
- output contains both audio and video according to `ffprobe`;
- cancellation/retry and Finder-reveal test doubles work;
- MP4, single HLS, master HLS, and WeChat-style extensionless MP4 smoke cases remain green.

**Step 2: Run red focused integration tests**

Run with the installed helper stopped so port 17432 is free:

```bash
./scripts/run-smoke-test.zsh
```

Expected: FAIL only on the missing platform smoke expectations.

**Step 3: Wire the test-only parser path**

Use build-tagged/test-injected dependencies already established by the integration harness. Never add a production environment variable that permits arbitrary yt-dlp paths.

**Step 4: Run the full deterministic smoke suite**

Run:

```bash
./scripts/run-smoke-test.zsh
```

Expected: `Smoke test 全部通过。` and audio/video validation for direct, HLS, and platform outputs.

Restart the exact installed helper that was stopped before the smoke run and verify `/health` reports the original running version.

**Step 5: Commit**

```bash
git add tests/integration scripts/run-smoke-test.zsh
git commit -m "test: cover platform downloads end to end"
```

## Task 12: Perform live compatibility checks without credentials

**Files:**

- Create only transient evidence under: `work/live-platform-checks/`
- Do not commit downloaded media or signed URLs.

**Step 1: Verify the pinned official binary**

Run:

```bash
work/vendor/yt-dlp_macos --version
```

Expected: `2026.07.04`.

**Step 2: Run metadata-only checks on the user-provided URLs**

Use the same fixed no-config/no-cookie/no-playlist arguments as production with `--simulate`. Test:

```text
https://www.youtube.com/watch?v=_mVb1D8wHxg
https://www.bilibili.com/video/BV1K3Gz6pEoo/
```

Do not print or retain full signed media URLs. Record only provider, success/failure class, selected height/container, and whether separate audio/video formats were chosen.

**Step 3: Run one small public test download per platform when legally suitable**

Use a short official or openly licensed sample, write only under `work/live-platform-checks/`, and verify with `ffprobe` that the output has one video and one audio stream. If no clearly suitable Bilibili sample is available, keep Bilibili live verification metadata-only and rely on deterministic download tests plus user acceptance.

**Step 4: Report platform limitations separately**

Distinguish application regression from upstream restrictions such as YouTube PO-token requirements, login requirements, rate limits, or a temporarily broken extractor.

**Step 5: Do not commit live artifacts**

Confirm:

```bash
git status --short
```

Expected: no live media or signed URL artifact is tracked.

## Task 13: Verify, review, package, and hand off

**Files:**

- Modify only files required by review findings.
- Create user-facing ZIP through the repository's `outputs/` link only.

**Step 1: Run the complete automated verification**

Temporarily stop the installed helper, then run:

```bash
cd helper && go test -race ./...
node --test tests/*.test.mjs tests/integration/*.test.mjs extension/tests/*.test.js
zsh tests/scripts/fetch_yt_dlp_test.zsh
zsh tests/scripts/macos_scripts_test.zsh
zsh tests/scripts/package_macos_test.zsh
./scripts/run-smoke-test.zsh
git diff --check
```

Expected: all pass. Restart the installed helper afterward even if verification fails; use a trap or equivalent recovery mechanism.

**Step 2: Request independent code review**

Use @superpowers:requesting-code-review. Review security boundaries, process cancellation, output publication, log redaction, test-only seams, packaging checksums, extension regressions, and whether any login/Cookie path was accidentally introduced.

**Step 3: Fix review findings test-first**

For each accepted finding, add a reproducing test, implement the smallest correction, rerun focused and full tests, and commit separately.

**Step 4: Build the user-facing package**

Run:

```bash
/Users/frank/.codex/scripts/ensure-central-outputs.zsh "/Users/frank/Documents/Codex/2026-07-23/neng"
./scripts/package-macos.zsh
```

Expected: a new non-overwriting `outputs/WebVideoHarbor-macOS.zip` containing the v0.2.0 extension, universal helper, pinned universal `yt-dlp_macos`, and license files.

**Step 5: Audit and report the artifact**

Verify ZIP integrity, unique top-level directory, executable modes, helper/extension/parser versions, SHA-256, absence of credentials/logs/test artifacts, and audio/video smoke output. Report the task-local clickable ZIP path and digest. Do not push, merge, tag, or publish a GitHub Release until the user authorizes that separate external action.

**Step 6: Finish the branch**

Use @superpowers:verification-before-completion, then @superpowers:finishing-a-development-branch to present merge/PR/keep-worktree choices with the exact verification evidence.

