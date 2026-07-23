# FFmpeg Safe HLS Proxy Fix Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Ensure FFmpeg only reads caller-preflighted HLS through a loopback proxy whose Go HTTP client validates every upstream target, while fixing MP4 muxer selection, output permissions, and cancellation classification.

**Architecture:** A new `internal/hlsproxy` server owns the caller-supplied root playlist, rewrites every nested HLS URI to random opaque loopback routes, and fetches child playlists and binary resources with the existing safe transport. `ffmpeg.Runner` starts that proxy per run and passes only its loopback root URL to FFmpeg, then shuts it down before returning.

**Tech Stack:** Go standard library (`net/http`, `net`, `crypto/rand`, `context`), existing `internal/hls` parser, existing `internal/safety` transport, FFmpeg.

---

### Task 1: Explicit MP4 muxer and Start cancellation

**Files:**
- Modify: `helper/internal/ffmpeg/runner_test.go`
- Modify: `helper/internal/ffmpeg/runner.go`

1. Change the argv expectation to require `-f mp4` immediately before the private part path and run it to observe RED.
2. Add a real FFmpeg test that generates a tiny HLS fixture under `t.TempDir`, invokes the runner path, and asserts the `.part` suffix no longer causes “Unable to choose an output format”; skip only when FFmpeg is genuinely absent.
3. Add `-f mp4` with no shell or additional protocol permissions and verify GREEN.
4. Add a fake-command test where `Start` returns an error after canceling its context; expect `CodeCanceled` and observe RED.
5. Check `ctx.Err()` before classifying any `Start` error and verify GREEN.

### Task 2: Private output permissions

**Files:**
- Modify: `helper/internal/ffmpeg/runner_test.go`
- Modify: `helper/internal/ffmpeg/runner.go`

1. Under a permissive temporary umask, make the fake command create its part with broad permissions and assert the final hard-linked file is exactly `0600`; observe RED.
2. In `syncAndClosePart`, after `Lstat`, open, and `SameFile`, call `file.Chmod(0600)` before `Sync` and `Close`; join chmod/sync/close errors deterministically.
3. Verify the permission test and all existing atomic-publication tests GREEN.

### Task 3: Loopback HLS proxy core and URI rewriting

**Files:**
- Create: `helper/internal/hlsproxy/proxy.go`
- Create: `helper/internal/hlsproxy/proxy_test.go`

1. Write RED tests for binding only `127.0.0.1:0`, random unguessable token plus opaque IDs, GET/HEAD-only methods, invalid token rejection, and clean shutdown.
2. Define production `Config{SourceURL, Manifest, Resolver}` and `Start(ctx, Config) (*Proxy, error)`. Keep the unsafe test client/listener/validation bypass only in package-private `internalConfig` and `start`.
3. Write RED table tests that rewrite relative and query-only URI lines plus quoted `URI="..."` attributes on `MEDIA`, `I-FRAME-STREAM-INF`, `MAP`, `KEY`, and `SESSION-KEY`; assert no upstream signed URL appears in served playlists.
4. Implement a line-preserving rewriter that resolves against each playlist URL, registers `{absoluteURL, playlist|binary}` under a random ID, rewrites every URI line, and rewrites syntactically valid quoted `URI` attributes. Master-child references are playlist resources; media segments/maps/parts are binary resources.
5. Run the rewrite tests and keep malformed/encrypted root playlists rejected through `hls.ParseBytes`.

### Task 4: Safe recursive upstream fetching

**Files:**
- Modify: `helper/internal/hlsproxy/proxy.go`
- Modify: `helper/internal/hlsproxy/proxy_test.go`

1. Write RED tests proving the supplied root is never re-fetched even when its URL later serves changed/encrypted bytes.
2. Write RED tests for private variant, segment, and MAP URLs; expect typed safe proxy errors and verify no raw signed URL in error text.
3. Write RED tests for encrypted/malformed child playlists and the 2 MiB body cap.
4. Write RED tests that only Range is copied upstream, Cookie/Authorization are absent, and only safe Content-Type/Length/Range/Accept-Ranges/status metadata is returned.
5. Build the production client exclusively with `safety.NewSafeTransport` and `safety.SafeRedirectPolicy`; validate every registered upstream URL before requesting it. Never copy inbound headers except Range.
6. Buffer only child playlists up to 2 MiB, inspect with `hls.ParseBytes`, rewrite recursively, and stream binary bodies. Store the first typed safe error for `Proxy.Err()` without retaining a URL.
7. Bind request contexts to the proxy parent context; `Close` cancels upstream work, shuts down the server, closes the listener if needed, and waits for the Serve goroutine.

### Task 5: Runner proxy integration

**Files:**
- Modify: `helper/internal/ffmpeg/runner.go`
- Modify: `helper/internal/ffmpeg/runner_test.go`

1. Add RED runner tests using a package-private proxy-factory seam: FFmpeg argv must contain only an injected loopback root URL, never `SourceURL`; whitelist must be exactly `http,tcp`; proxy closes on success, failure, cancellation, pipe/start errors, and output errors.
2. Add RED tests mapping proxy unsafe/encrypted/manifest failures to existing typed runner errors without signed URLs.
3. Add `proxyFactory` to `internalConfig`/`Runner`; production calls `hlsproxy.Start`, tests use a fake. Start the proxy only after root parse and source-base validation, defer bounded close/wait, and remove all direct-remote fallback.
4. After FFmpeg/parser completion, consult `Proxy.Err()` before generic process failure. Keep parser-error and context-cancellation precedence.
5. Update all command tests to assert the opaque loopback URL and exact whitelist. Run runner and proxy race tests.

### Task 6: Final verification and commit

1. Run `cd helper && /usr/local/bin/go test -race ./internal/hlsproxy ./internal/ffmpeg -v`.
2. Run `cd helper && /usr/local/bin/go test -race ./...`.
3. Run `cd helper && /usr/local/bin/go vet ./...` and `git diff --check`.
4. Confirm only the plan, `runner.go/test`, and `hlsproxy` files changed; self-review URL/error secrecy, listener lifecycle, header filtering, and file permissions.
5. Commit with `fix: proxy hls streams before ffmpeg`.
