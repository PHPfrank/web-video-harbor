# macOS Web Video Downloader Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a Chrome extension plus a loopback-only macOS helper that detects direct MP4 and non-DRM M3U8 media, downloads it safely, and offers best-effort WeChat Channels compatibility.

**Architecture:** A Manifest V3 extension collects media URLs from the DOM and observed tab requests, then sends authenticated JSON requests to a Go helper bound to `127.0.0.1:17432`. The helper validates remote targets, inspects HLS manifests, streams direct files, invokes FFmpeg for HLS, and exposes task progress to the extension popup.

**Tech Stack:** Go 1.22+ standard library, FFmpeg, Chrome Manifest V3, vanilla HTML/CSS/JavaScript, Node.js built-in test runner.

---

## Execution rules

- Use @superpowers:test-driven-development for every behavior change: failing test, smallest implementation, passing test.
- Keep runtime dependencies at zero beyond FFmpeg; use the Go and browser standard libraries.
- Keep build products, fixtures generated at runtime, logs, and screenshots under `work/`.
- Before creating the final ZIP in `outputs/`, run `/Users/frank/.codex/scripts/ensure-central-outputs.zsh "/Users/frank/Documents/Codex/2026-07-23/neng"`.
- Commit after every task. Do not include `outputs/`, `work/`, binaries, logs, tokens, or user downloads in Git.
- Never add Cookie export, DRM bypass, private-network fetching, or login circumvention.

### Task 1: Toolchain and project skeleton

**Files:**
- Create: `.gitignore`
- Create: `Makefile`
- Create: `helper/go.mod`
- Create: `helper/cmd/web-video-helper/main.go`
- Create: `extension/manifest.json`
- Create: `extension/background.js`
- Create: `extension/content.js`

**Step 1: Verify and prepare the toolchain**

Run:

```bash
go version
ffmpeg -version
node --version
```

Expected before setup: the existing Go is older than 1.22 and FFmpeg is missing. Install current Go and FFmpeg with Homebrew, then use the Homebrew Go path for all later commands.

**Step 2: Create ignore rules**

Ignore `work/`, `outputs/`, `helper/bin/`, `*.log`, `.DS_Store`, and any local configuration containing a pairing token.

**Step 3: Write the smallest helper entry point**

`main.go` should print version information when invoked with `--version`. Do not start the HTTP server yet.

**Step 4: Add the extension shell**

Create a valid Manifest V3 manifest with `storage`, `tabs`, `scripting`, and `webRequest` permissions; `<all_urls>` host permission; a service worker; and content scripts loaded on HTTP and HTTPS pages.

**Step 5: Verify the skeleton**

Run:

```bash
cd helper && go test ./...
node --check extension/background.js
node --check extension/content.js
```

Expected: all commands exit 0.

**Step 6: Commit**

```bash
git add .gitignore Makefile helper extension
git commit -m "chore: scaffold video downloader"
```

### Task 2: URL safety and output filenames

**Files:**
- Create: `helper/internal/safety/url.go`
- Create: `helper/internal/safety/url_test.go`
- Create: `helper/internal/output/name.go`
- Create: `helper/internal/output/name_test.go`

**Step 1: Write failing URL validation tests**

Cover these cases:

```go
func TestValidateRemoteURL(t *testing.T) {
    // allow https://media.example.com/video.mp4
    // reject file:, ftp:, missing host, credentials in URL
    // reject localhost, 127.0.0.1, ::1, 10/8, 172.16/12,
    // 192.168/16, link-local, multicast and unspecified addresses
}
```

Use an injected resolver so DNS answers can be tested without network access. Require every resolved address to be public. Export `ValidateRemoteURL(ctx, rawURL, resolver)` and a safe redirect callback that revalidates every redirect target.

**Step 2: Run the safety tests and confirm failure**

Run: `cd helper && go test ./internal/safety -run TestValidateRemoteURL -v`

Expected: FAIL because the package or function does not exist.

**Step 3: Implement URL validation**

Parse with `net/url`, allow only `http` and `https`, forbid `User`, require hostname, resolve with a narrow interface, and reject non-public addresses with `net.IP` checks. Return stable Chinese-facing error codes separately from internal details.

**Step 4: Write failing filename tests**

Test removal or replacement of `/`, `:`, control characters, leading dots, trailing spaces, empty names, overlong names, and an existing `视频.mp4` becoming `视频 (2).mp4`.

**Step 5: Implement filename handling**

Export `SanitizeBaseName(string) string` and `NextAvailablePath(dir, base, ext string) (string, error)`. Preserve Chinese characters, cap the base name by rune count, and never overwrite an existing path.

**Step 6: Run tests**

Run: `cd helper && go test ./internal/safety ./internal/output -v`

Expected: PASS.

**Step 7: Commit**

```bash
git add helper/internal/safety helper/internal/output
git commit -m "feat: validate download targets and filenames"
```

### Task 3: Media classification and HLS inspection

**Files:**
- Create: `helper/internal/media/media.go`
- Create: `helper/internal/media/media_test.go`
- Create: `helper/internal/hls/parser.go`
- Create: `helper/internal/hls/parser_test.go`
- Create: `helper/internal/hls/testdata/master.m3u8`
- Create: `helper/internal/hls/testdata/media.m3u8`
- Create: `helper/internal/hls/testdata/encrypted.m3u8`

**Step 1: Write failing classification tests**

Classify URLs and content types as `mp4`, `hls`, or `unknown`. Query strings must not hide `.mp4` or `.m3u8`. Recognize `application/vnd.apple.mpegurl`, `application/x-mpegurl`, and `video/mp4`.

**Step 2: Implement the classifier and verify**

Run: `cd helper && go test ./internal/media -v`

Expected: PASS.

**Step 3: Write failing HLS parser tests**

Parse a master playlist such as:

```text
#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=5200000,RESOLUTION=1920x1080,CODECS="avc1.640028,mp4a.40.2"
1080/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2400000,RESOLUTION=1280x720
720/index.m3u8
```

Expected result: two absolute variants sorted highest quality first, with labels `1080p` and `720p`. A media playlist returns a single `原始画质` option. Any `#EXT-X-KEY` with a method other than `NONE` returns a typed unsupported-encryption error.

**Step 4: Implement the parser**

Use `bufio.Scanner`, parse the attribute list without splitting quoted commas, resolve relative URLs against the manifest URL, cap the manifest at 2 MiB, and reject malformed or empty playlists.

**Step 5: Run tests**

Run: `cd helper && go test ./internal/media ./internal/hls -v`

Expected: PASS.

**Step 6: Commit**

```bash
git add helper/internal/media helper/internal/hls
git commit -m "feat: inspect mp4 and hls media"
```

### Task 4: Task lifecycle manager

**Files:**
- Create: `helper/internal/tasks/model.go`
- Create: `helper/internal/tasks/manager.go`
- Create: `helper/internal/tasks/manager_test.go`

**Step 1: Write failing lifecycle tests**

Test `queued -> downloading -> merging -> completed`, failure with a user-facing message, cancellation via context, retry creating a fresh attempt, invalid transitions, and thread-safe list/get operations.

Use this public shape:

```go
type Status string
const (
    Queued Status = "queued"
    Downloading Status = "downloading"
    Merging Status = "merging"
    Completed Status = "completed"
    Failed Status = "failed"
    Canceled Status = "canceled"
)

type Task struct {
    ID string `json:"id"`
    URL string `json:"url"`
    Title string `json:"title"`
    Status Status `json:"status"`
    Progress float64 `json:"progress"`
    OutputPath string `json:"outputPath,omitempty"`
    Error string `json:"error,omitempty"`
}
```

**Step 2: Run and confirm failure**

Run: `cd helper && go test ./internal/tasks -v`

Expected: FAIL because the manager is not implemented.

**Step 3: Implement the manager**

Guard state with `sync.RWMutex`, return copies rather than mutable internal pointers, keep a cancel function per active task, and generate IDs with `crypto/rand` rather than a new dependency.

**Step 4: Run tests including race detection**

Run: `cd helper && go test -race ./internal/tasks -v`

Expected: PASS with no race report.

**Step 5: Commit**

```bash
git add helper/internal/tasks
git commit -m "feat: manage download task lifecycle"
```

### Task 5: Direct MP4 downloader

**Files:**
- Create: `helper/internal/download/direct.go`
- Create: `helper/internal/download/direct_test.go`

**Step 1: Write failing download tests**

Use `httptest.Server` and a temporary directory to test streaming a file, progress callbacks, cancellation, HTTP errors, three attempts for transient failures, redirect validation, partial-file cleanup, and atomic finalization.

**Step 2: Run and confirm failure**

Run: `cd helper && go test ./internal/download -run TestDirect -v`

Expected: FAIL.

**Step 3: Implement the direct downloader**

The downloader receives an `http.Client`, target validator, retry policy, and progress callback. Write to a task-specific `.part` file with `io.CopyBuffer`, calculate progress from `Content-Length` when present, call `Sync`, close, and rename only after success.

**Step 4: Verify normal and cancellation paths**

Run: `cd helper && go test -race ./internal/download -v`

Expected: PASS and no leaked partial files.

**Step 5: Commit**

```bash
git add helper/internal/download
git commit -m "feat: stream direct video downloads"
```

### Task 6: FFmpeg HLS runner

**Files:**
- Create: `helper/internal/ffmpeg/runner.go`
- Create: `helper/internal/ffmpeg/runner_test.go`

**Step 1: Write failing command construction tests**

Verify the runner uses an argument slice and never a shell string. Expected core arguments:

```text
-nostdin -y -i <validated-url> -map 0 -c copy -movflags +faststart -progress pipe:1 -nostats <part-file>
```

The test must also verify that cancellation terminates the child process, FFmpeg-not-found maps to `未安装 FFmpeg`, and non-zero exit captures only a bounded tail of stderr.

**Step 2: Write failing progress parser tests**

Parse `out_time_ms`, `total_size`, `speed`, and `progress=end` from FFmpeg's key-value progress stream. Never log the full source URL when it may contain a token.

**Step 3: Implement the runner**

Use `exec.CommandContext`, explicit arguments, bounded stderr, and the same atomic output behavior as direct downloads. Detect an encrypted HLS manifest before starting FFmpeg; do not add decryption or DRM-related flags.

**Step 4: Run tests**

Run: `cd helper && go test -race ./internal/ffmpeg -v`

Expected: PASS.

**Step 5: Commit**

```bash
git add helper/internal/ffmpeg
git commit -m "feat: merge hls streams with ffmpeg"
```

### Task 7: Authenticated loopback API

**Files:**
- Create: `helper/internal/config/config.go`
- Create: `helper/internal/config/config_test.go`
- Create: `helper/internal/api/server.go`
- Create: `helper/internal/api/server_test.go`
- Modify: `helper/cmd/web-video-helper/main.go`

**Step 1: Write failing configuration tests**

Test first-run generation of a 256-bit token, restrictive file mode, configurable download directory, fixed loopback default `127.0.0.1:17432`, and refusal to bind a non-loopback host.

**Step 2: Implement config loading**

Default to `~/Library/Application Support/网页视频下载器/config.json` and `~/Downloads/网页视频下载器/`. Support `--config` for tests and portable development.

**Step 3: Write failing API tests**

With `httptest`, cover:

- `GET /health` without a token returns only non-sensitive readiness data.
- Every `/v1/` route requires `X-Video-Helper-Token`.
- CORS accepts `chrome-extension://...` and rejects normal web origins.
- Request bodies are capped and unknown JSON fields are rejected.
- Inspect returns HLS variants.
- Create/list/get/cancel/retry use the task manager.
- Reveal only accepts a completed task path owned by the configured download directory.

**Step 4: Implement handlers and middleware**

Use `http.ServeMux`, constant-time token comparison, strict JSON decoding, method checks, conservative timeouts, and uniform JSON errors. Bind only with `net.Listen("tcp", configuredLoopbackAddress)`.

**Step 5: Wire the executable**

Support `--version`, `--config`, and `--print-token`. On normal start, print the loopback address and download directory but redact the token unless explicitly requested.

**Step 6: Run all helper tests**

Run: `cd helper && go test -race ./...`

Expected: PASS.

**Step 7: Commit**

```bash
git add helper/cmd helper/internal/config helper/internal/api
git commit -m "feat: expose secure local helper api"
```

### Task 8: Extension media discovery

**Files:**
- Create: `extension/lib/media.js`
- Create: `extension/tests/media.test.js`
- Modify: `extension/manifest.json`
- Modify: `extension/content.js`
- Modify: `extension/background.js`

**Step 1: Write failing JavaScript tests**

Use `node:test` and `node:assert/strict` to cover URL normalization, media type inference, blob/data URL rejection, MP4/M3U8 query strings, candidate deduplication, title selection, and WeChat media CDN URLs that only reveal their type through content type.

**Step 2: Run and confirm failure**

Run: `node --test extension/tests/media.test.js`

Expected: FAIL because the media library does not exist.

**Step 3: Implement the shared media library**

Expose pure functions through both `globalThis.VideoGrabberMedia` and `module.exports` so the same logic runs in Chrome and Node tests.

**Step 4: Implement DOM collection**

Collect `video.currentSrc`, `video.src`, nested `source.src`, and matching `performance.getEntriesByType("resource")` entries. Send normalized candidates with page title, source kind, and tab URL to the background worker.

**Step 5: Implement request observation**

Use `chrome.webRequest.onBeforeRequest` and `onHeadersReceived` for `media` and `xmlhttprequest` requests. Record URL and safe response metadata only; do not capture request bodies, Cookie headers, authorization headers, or page content. Keep candidates per tab and clear them on top-level navigation or tab close.

**Step 6: Run tests and syntax checks**

Run:

```bash
node --test extension/tests/*.test.js
node --check extension/lib/media.js
node --check extension/content.js
node --check extension/background.js
```

Expected: PASS.

**Step 7: Commit**

```bash
git add extension
git commit -m "feat: detect webpage video resources"
```

### Task 9: Popup, pairing, and task controls

**Files:**
- Create: `extension/popup.html`
- Create: `extension/popup.css`
- Create: `extension/popup.js`
- Create: `extension/options.html`
- Create: `extension/options.js`
- Create: `extension/tests/popup-state.test.js`
- Modify: `extension/manifest.json`
- Modify: `extension/background.js`

**Step 1: Apply the frontend design guidance**

Use @frontend-design before writing UI code. Keep a compact 400 px popup, strong Chinese typography, clear connection status, accessible contrast, visible keyboard focus, reduced-motion support, and distinct video/task cards. Avoid framework dependencies and decorative clutter.

**Step 2: Write failing state tests**

Extract pure view-model functions and test empty, disconnected, scanning, candidate-list, downloading, merging, completed, failed, and canceled states. Test quality sorting and safe text rendering.

**Step 3: Implement pairing and helper client**

Store the token in `chrome.storage.local`, never sync storage. Fetch `http://127.0.0.1:17432`, attach the token only to `/v1/` calls, set timeouts with `AbortController`, and convert errors to short Chinese messages.

**Step 4: Implement popup behavior**

Add rescan, quality selection, download, cancel, retry, and reveal actions. Poll active tasks while the popup is open and stop polling when it closes. For no-result WeChat pages, show `请先在浏览器中播放视频几秒，再重新扫描`.

**Step 5: Implement options behavior**

Provide token entry, connection test, and a privacy explanation. Do not provide Cookie import or arbitrary helper-host fields.

**Step 6: Run tests and syntax checks**

Run:

```bash
node --test extension/tests/*.test.js
node --check extension/popup.js
node --check extension/options.js
```

Expected: PASS.

**Step 7: Commit**

```bash
git add extension
git commit -m "feat: add extension download interface"
```

### Task 10: Integration fixtures and end-to-end smoke test

**Files:**
- Create: `tests/fixtures/site/index.html`
- Create: `tests/fixtures/site/master.m3u8`
- Create: `tests/fixtures/site/720/index.m3u8`
- Create: `tests/fixtures/site/1080/index.m3u8`
- Create: `tests/integration/helper_test.go`
- Create: `scripts/run-smoke-test.zsh`

**Step 1: Create deterministic media fixtures**

Generate tiny color-and-tone MP4/HLS fixtures under `work/fixtures-generated/` with FFmpeg. Keep playlists and the HTML page in Git, but keep generated media segments out of Git.

**Step 2: Write the failing integration test**

Start a controlled local fixture server and a helper configured for test mode with an explicit allowlist for that exact fixture server. Do not weaken production private-network protection. Exercise health, inspect, direct download, HLS download, progress, completion, and cancellation.

**Step 3: Implement only the test seams needed**

Add dependency injection or a test-only exact-host allowlist to the helper. Production defaults must remain deny-private.

**Step 4: Run integration tests**

Run:

```bash
cd helper && go test -race ./...
zsh scripts/run-smoke-test.zsh
```

Expected: all tests pass and completed MP4 files exist only under `work/smoke-downloads/`.

**Step 5: Perform a Chrome extension smoke check**

Load the unpacked extension, start the fixture page, play each video briefly, and confirm the popup finds direct MP4 and HLS candidates. Download one of each and verify playback and duration with `ffprobe`.

**Step 6: WeChat Channels compatibility check**

Use only a user-provided share link for content they are authorized to download. Record whether Chrome exposes a direct MP4/HLS request. If it does, verify the normal path; if it does not or is protected, confirm the extension displays the documented unsupported message. Do not reverse engineer credentials or encryption.

**Step 7: Commit**

```bash
git add tests scripts/run-smoke-test.zsh
git commit -m "test: cover video download workflow"
```

### Task 11: macOS scripts and user documentation

**Files:**
- Create: `scripts/build-macos.zsh`
- Create: `scripts/start-helper.zsh`
- Create: `scripts/stop-helper.zsh`
- Create: `scripts/helper-status.zsh`
- Create: `README.md`
- Create: `docs/安装使用说明.md`

**Step 1: Write script syntax checks before behavior**

Run `zsh -n` against each initially empty script and then add behavior in small steps. Scripts must use explicit project-relative paths, quote every path, create state only under `~/Library/Application Support/网页视频下载器/`, and never delete a broad or unresolved path.

**Step 2: Implement the build script**

Build current architecture first. When modern Go supports both targets, build `darwin/arm64` and `darwin/amd64` into `work/dist/` and combine them with `lipo` into `work/dist/web-video-helper`. Run `file` to verify architectures.

**Step 3: Implement lifecycle scripts**

Start the helper in the background with a PID file and bounded log, refuse duplicate starts, verify the recorded PID belongs to the helper before stopping, and report health without printing the token.

**Step 4: Write user documentation**

Document FFmpeg installation, helper startup, `--print-token`, Chrome unpacked-extension installation, pairing, detection workflow, default download location, WeChat Channels limitations, privacy behavior, and troubleshooting.

**Step 5: Verify documentation commands**

Execute every documented command in a clean temporary config and confirm it matches the text.

**Step 6: Commit**

```bash
git add scripts README.md docs/安装使用说明.md
git commit -m "docs: add macos setup and usage guide"
```

### Task 12: Final verification, review, and packaged deliverable

**Files:**
- Modify as required by review findings.
- Create through centralized path: `outputs/网页视频下载器-macOS.zip`

**Step 1: Run complete automated verification**

Run:

```bash
cd helper && go test -race ./...
node --test extension/tests/*.test.js
zsh -n scripts/*.zsh
zsh scripts/run-smoke-test.zsh
```

Expected: every command passes.

**Step 2: Run security-focused checks**

Confirm the helper binds only to `127.0.0.1`, rejects missing/wrong tokens, rejects localhost/private-network download URLs in production mode, caps request and manifest sizes, revalidates redirects, avoids shell command construction, and redacts tokens from logs.

**Step 3: Use completion and review skills**

Use @superpowers:requesting-code-review for a focused code review and address verified findings. Then use @superpowers:verification-before-completion and rerun all relevant checks after the final change.

**Step 4: Prepare centralized outputs**

Run:

```bash
/Users/frank/.codex/scripts/ensure-central-outputs.zsh "/Users/frank/Documents/Codex/2026-07-23/neng"
```

If the helper reports a conflict, stop and report the exact paths without modifying either side.

**Step 5: Package the deliverable**

Create a staging directory under `work/package/` containing the extension, helper binary, lifecycle scripts, README, installation guide, and license notices. Exclude tests, tokens, local configuration, `.git`, logs, and downloaded media. Produce `outputs/网页视频下载器-macOS.zip` from that staging directory.

**Step 6: Inspect the archive**

List the ZIP contents, unpack it under a fresh `work/package-check/` directory, rerun helper `--version`, validate `manifest.json`, run script syntax checks, and compare checksums for packaged files.

**Step 7: Final commit**

```bash
git add -A
git commit -m "feat: deliver macos web video downloader"
```

Expected: Git contains source, tests, scripts, and docs; generated deliverables remain ignored and accessible through the task-local `outputs/` link.
