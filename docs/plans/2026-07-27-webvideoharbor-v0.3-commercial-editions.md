# WebVideoHarbor v0.3 Commercial Editions Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Release a public Free macOS edition that downloads only direct MP4 files and a privately distributed Pro macOS edition that unlocks M3U8 and supported public platform downloads with offline, device-bound activation.

**Architecture:** Keep one shared extension/API contract, enforce capabilities in the Go helper as well as the UI, and maintain the Pro implementation in a separate private repository seeded from v0.2.1. The Pro helper verifies Ed25519-signed licenses offline; the signing private key and production signer ledger never enter the public repository or release packages.

**Tech Stack:** Chrome Manifest V3, vanilla JavaScript with Node's test runner, Go 1.x standard library (`crypto/ed25519`, `crypto/sha256`, `encoding/base64`), zsh packaging tests, FFmpeg, pinned yt-dlp and Deno.

---

## Before implementation

- Use `@superpowers:using-git-worktrees` and create an isolated public worktree named `codex/v030-free`.
- Do not implement Pro-only code on a branch that can accidentally be pushed to `PHPfrank/web-video-harbor`.
- Before creating `PHPfrank/web-video-harbor-pro` or pushing any private branch, obtain explicit user approval for that external GitHub action.
- Create the Pro repository as **private**, seed it from the reviewed v0.2.1 code, and verify its remote does not point to the public repository.
- Never generate the production signing private key during ordinary development or tests. Tests must use ephemeral fixture keys.
- Treat the already-public v0.2.1 source as historical fact. Do not rewrite or delete public history; the goal is to protect future Pro maintenance and official builds, not claim that old code was never published.

### Task 1: Add the shared edition and capability contract

**Repository:** Public Free repository first; cherry-pick the commit into the private Pro repository.

**Files:**
- Create: `helper/internal/entitlement/policy.go`
- Create: `helper/internal/entitlement/policy_test.go`
- Modify: `helper/internal/api/server.go`
- Modify: `helper/internal/api/server_test.go`

**Step 1: Write the failing policy tests**

Cover exact, closed capability values and safe status output:

```go
func TestFreePolicyAllowsOnlyMP4(t *testing.T) {
	policy := entitlement.NewFreePolicy()
	if err := policy.Require("mp4"); err != nil {
		t.Fatalf("MP4 rejected: %v", err)
	}
	for _, mediaType := range []string{"hls", "platform", "", "MP4", "unknown"} {
		var required *entitlement.ProRequiredError
		if err := policy.Require(mediaType); !errors.As(err, &required) {
			t.Fatalf("%q error = %v", mediaType, err)
		}
	}
}

func TestFreeStatusIsMinimal(t *testing.T) {
	got := entitlement.NewFreePolicy().Status()
	if got.Edition != "free" || got.Activated || got.ActivationSupported {
		t.Fatalf("status = %#v", got)
	}
	if !got.Capabilities.MP4 || got.Capabilities.HLS || got.Capabilities.Platform {
		t.Fatalf("capabilities = %#v", got.Capabilities)
	}
}
```

**Step 2: Run the tests and verify they fail**

Run:

```bash
cd helper && go test ./internal/entitlement -v
```

Expected: FAIL because `internal/entitlement` does not exist.

**Step 3: Implement the minimal public contract**

Use a closed interface; do not expose a generic feature string that silently accepts typos:

```go
package entitlement

import "fmt"

type Capabilities struct {
	MP4      bool `json:"mp4"`
	HLS      bool `json:"hls"`
	Platform bool `json:"platform"`
}

type Status struct {
	Edition             string       `json:"edition"`
	Activated           bool         `json:"activated"`
	ActivationSupported bool         `json:"activationSupported"`
	DeviceCode          string       `json:"deviceCode,omitempty"`
	Capabilities        Capabilities `json:"capabilities"`
}

type Policy interface {
	Status() Status
	Require(mediaType string) error
}

type ProRequiredError struct{ MediaType string }

func (e *ProRequiredError) Error() string { return fmt.Sprintf("pro required for %s", e.MediaType) }
func (e *ProRequiredError) SafeMessage() string { return "当前视频需要 Pro 版本" }

type freePolicy struct{}

func NewFreePolicy() Policy { return freePolicy{} }

func (freePolicy) Status() Status {
	return Status{
		Edition: "free",
		Capabilities: Capabilities{MP4: true},
	}
}

func (freePolicy) Require(mediaType string) error {
	if mediaType == "mp4" {
		return nil
	}
	return &ProRequiredError{MediaType: mediaType}
}
```

**Step 4: Add the authenticated status route tests**

Add tests asserting:

- `GET /v1/license` requires the exact loopback token.
- Free response is exactly `edition`, `activated`, `activationSupported`, and `capabilities`; it contains no token, paths, device identifiers, or parser details.
- CORS preflight permits `GET /v1/license` only from valid Chrome extension origins.
- `POST /v1/license` is rejected in the public Free helper.

**Step 5: Implement `GET /v1/license`**

Add `Entitlement entitlement.Policy` to `api.Options` and `Server`. Reject a nil policy in `api.New`. Add the exact route to `allowedMethods` and `routeV1` after authentication. Keep `/health` unchanged and unauthenticated; license state must not be added there.

**Step 6: Run focused tests**

Run:

```bash
cd helper && go test ./internal/entitlement ./internal/api -v
```

Expected: PASS.

**Step 7: Commit**

```bash
git add helper/internal/entitlement helper/internal/api/server.go helper/internal/api/server_test.go
git commit -m "feat: expose free edition capabilities"
```

### Task 2: Enforce Free restrictions in both API and engine

**Repository:** Public Free repository first; cherry-pick shared enforcement into Pro.

**Files:**
- Modify: `helper/internal/api/engine.go`
- Modify: `helper/internal/api/engine_test.go`
- Modify: `helper/internal/api/server.go`
- Modify: `helper/internal/api/server_test.go`
- Modify: `helper/cmd/web-video-harbor-helper/main.go`
- Modify: `helper/cmd/web-video-harbor-helper/main_test.go`

**Step 1: Write failing server tests**

With a Free policy, assert:

```go
for _, body := range []string{
	`{"url":"https://media.example/video.m3u8","title":"HLS","mediaType":"hls"}`,
	`{"url":"https://www.youtube.com/watch?v=abc_123-XYZ","title":"YT","mediaType":"platform","quality":"720"}`,
} {
	rr := perform(t, srv.Handler(), http.MethodPost, "/v1/tasks", []byte(body), testToken, "")
	if rr.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if got := decodeObject(t, rr); got["code"] != "pro_required" {
		t.Fatalf("response = %#v", got)
	}
}
```

Also verify MP4 still returns `201 Created` and that `/v1/inspect` returns `pro_required` before making any remote request.

**Step 2: Write failing engine tests**

Inject Free policy directly into the engine and verify `Start` and `Retry` reject HLS/platform even if a caller bypasses HTTP. A failed or canceled Pro task created with a test policy must become non-retryable after the policy changes to Free.

**Step 3: Run focused tests and verify failure**

```bash
cd helper && go test ./internal/api -run 'Free|ProRequired|Retry' -v
```

Expected: FAIL because no policy is enforced.

**Step 4: Implement defense in depth**

- Add `entitlement entitlement.Policy` to `engineDeps` and `Engine`.
- Change `NewEngine` to require the policy.
- Call `Require(spec.MediaType)` before creating a task in `Start`.
- Call `Require(spec.MediaType)` before creating a retry in `Retry`.
- In `handleInspect`, require `hls` before invoking the inspector.
- In `handleCreate`, require the normalized media type before invoking `TaskService.Start`.
- Map `ProRequiredError` to HTTP `402` with code `pro_required` and the fixed safe message.
- Construct `entitlement.NewFreePolicy()` in the public helper `main.go` and pass the same policy instance to the engine and server.

Do not delete HLS/platform safety checks. The policy is an authorization layer, not a replacement for URL, DRM, parser, or FFmpeg validation.

**Step 5: Run focused tests**

```bash
cd helper && go test ./internal/api ./cmd/web-video-harbor-helper -v
```

Expected: PASS.

**Step 6: Run the Go race suite**

```bash
cd helper && go test -race ./...
```

Expected: PASS with no race reports.

**Step 7: Commit**

```bash
git add helper/internal/api helper/cmd/web-video-harbor-helper
git commit -m "feat: enforce free MP4-only policy"
```

### Task 3: Show Free and Pro state clearly in the extension

**Repository:** Public Free repository; cherry-pick into Pro.

**Files:**
- Modify: `extension/lib/helper-client.js`
- Modify: `extension/lib/popup-controller.js`
- Modify: `extension/lib/popup-state.js`
- Modify: `extension/popup.js`
- Modify: `extension/popup.css`
- Modify: `extension/options.html`
- Modify: `extension/options.js`
- Modify: `extension/tests/helper-client.test.js`
- Modify: `extension/tests/popup-controller.test.js`
- Modify: `extension/tests/popup-state.test.js`
- Modify: `extension/tests/popup-ui.test.js`
- Modify: `extension/tests/wiring.test.js`

**Step 1: Write failing client normalization tests**

Add `getLicenseStatus()` tests for these exact states:

```js
{
  edition: 'free',
  activated: false,
  activationSupported: false,
  deviceCode: '',
  capabilities: { mp4: true, hls: false, platform: false },
}
```

Malformed, oversized, unknown or array-shaped fields must normalize to Free/locked. Never trust an unrecognized edition as Pro.

Add fixed Chinese client errors:

```js
pro_required: '当前视频需要 Pro 版本',
license_invalid: '激活码无效，请核对后重试',
license_device_mismatch: '激活码不属于这台 Mac',
license_unsupported: '激活码版本不受支持，请升级网页视频港',
```

**Step 2: Run the failing extension tests**

```bash
node --test extension/tests/helper-client.test.js extension/tests/popup-controller.test.js extension/tests/popup-ui.test.js
```

Expected: FAIL because the client and controller do not know license state.

**Step 3: Add the helper client method**

Implement strict normalization and:

```js
getLicenseStatus() {
  return request('/v1/license', { authenticated: true }).then(normalizeLicenseStatus);
}
```

Do not store license data in `chrome.storage`; the helper owns it.

**Step 4: Gate candidates in the controller**

Fetch license status during the existing refresh cycle. Derive capability by candidate kind:

```js
function capabilityForCandidate(candidate) {
  if (candidate.kind === 'hls') return 'hls';
  if (candidate.kind === 'platform') return 'platform';
  return 'mp4';
}
```

For a locked candidate return `requiresPro: true`, `canUse: false`, and `blockedReason: '此类视频需要 Pro 版本'`. The Pro check must take precedence over FFmpeg/parser checks so Free users do not see irrelevant installation errors.

**Step 5: Render an upgrade action**

For `requiresPro`, render an enabled secondary button labeled `了解 Pro` rather than a disabled Download button. The click opens `options.html#pro` through `chrome.runtime.openOptionsPage()`. Use text nodes only; do not inject contact HTML from a remote source.

**Step 6: Add the version card to Options**

Show:

- Current edition: Free / Pro.
- Activation state.
- Free description: ordinary direct MP4 only.
- Pro description: M3U8 and supported public platform single videos.
- Contact: `t3056339@163.com`.
- A clear disclaimer that not all sites are supported and no access controls are bypassed.

The shared page may reserve a hidden activation form for the private Pro helper, but the Free helper must show “安装 Pro 版本后激活” and must not accept a token locally.

**Step 7: Run all extension tests**

```bash
node --test extension/tests/*.test.js
```

Expected: PASS.

**Step 8: Commit**

```bash
git add extension
git commit -m "feat: present free and pro capabilities"
```

### Task 4: Create the private offline-license library

**Repository:** Private Pro repository only.

**Files:**
- Create: `helper/internal/license/token.go`
- Create: `helper/internal/license/token_test.go`
- Create: `helper/internal/license/device.go`
- Create: `helper/internal/license/device_darwin.go`
- Create: `helper/internal/license/device_test.go`
- Create: `helper/internal/license/store.go`
- Create: `helper/internal/license/store_test.go`
- Create: `helper/internal/entitlement/pro.go`
- Create: `helper/internal/entitlement/pro_test.go`

**Step 1: Write failing token tests**

Generate an ephemeral key in the test with `ed25519.GenerateKey(rand.Reader)`. Cover:

- A valid `WVH1.<payload>.<signature>` token.
- One-byte payload and signature changes.
- Wrong device code, product, schema version and public key.
- Duplicate/unknown JSON fields, invalid base64, excessive length and trailing data.
- No expiry field: a valid purchased v1 Pro license remains permanent.

The payload is fixed:

```go
type Payload struct {
	Version   int    `json:"v"`
	Product   string `json:"product"`
	Device    string `json:"device"`
	LicenseID string `json:"license_id"`
	IssuedAt  string `json:"issued_at"`
}
```

**Step 2: Run tests and verify failure**

```bash
cd helper && go test ./internal/license -v
```

Expected: FAIL because the package does not exist.

**Step 3: Implement strict Ed25519 verification**

- Prefix must be exactly `WVH1`.
- Use unpadded URL-safe base64.
- Limit the complete token to 4096 bytes and payload to 2048 bytes.
- Use `json.Decoder.DisallowUnknownFields()` and require EOF.
- Verify signature before trusting payload fields.
- Require `v == 1`, `product == "web-video-harbor-pro"`, a valid uppercase device code, a bounded ASCII license ID, and RFC3339 `issued_at`.
- Return typed errors; do not include raw token contents in errors.

**Step 4: Write and implement device-code tests**

Use dependency injection for the raw macOS identifier. Convert it to a non-reversible code:

```go
func DeviceCode(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 256 {
		return "", ErrDeviceUnavailable
	}
	sum := sha256.Sum256([]byte("WebVideoHarbor/device/v1\x00" + raw))
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:10])
	return "WVH-" + encoded[:5] + "-" + encoded[5:10] + "-" + encoded[10:], nil
}
```

The Darwin provider invokes `/usr/sbin/ioreg` directly with fixed arguments, never through a shell, and parses only `IOPlatformUUID`. It must never log or persist the raw UUID. Tests use a fake command runner.

**Step 5: Write and implement secure local storage**

Store only the signed token in `license.json` beside the existing config:

- Parent must be a real directory, not a symlink.
- Existing file must be regular, non-symlink, mode `0600`, bounded to 8 KiB.
- Activation writes a new `0600` file through a temporary file, `fsync`, atomic rename and parent-directory sync.
- Failed verification never overwrites an existing valid license.
- Errors never include the token.

**Step 6: Implement the Pro policy adapter**

`Status()` exposes the device code and activates HLS/platform only after a valid license for this device. `Require("mp4")` always succeeds; `Require("hls")` and `Require("platform")` return `ProRequiredError` until activated.

**Step 7: Run race tests**

```bash
cd helper && go test -race ./internal/license ./internal/entitlement -v
```

Expected: PASS.

**Step 8: Commit in the private repository**

```bash
git add helper/internal/license helper/internal/entitlement
git commit -m "feat: verify device-bound pro licenses"
```

### Task 5: Build the private signer and two-device ledger

**Repository:** Private Pro repository only.

**Files:**
- Create: `helper/cmd/web-video-harbor-license-signer/main.go`
- Create: `helper/cmd/web-video-harbor-license-signer/main_test.go`
- Create: `helper/cmd/web-video-harbor-license-signer/README.md`
- Modify: `.gitignore`

**Step 1: Write failing command tests**

Test with temporary directories and ephemeral keys:

- `keygen` creates an Ed25519 private key with mode `0600` and prints only the public key.
- `issue --order WVH-0001 --device WVH-...` emits one valid token.
- The same order can issue for two distinct devices.
- A third distinct device is rejected.
- Reissuing the same order/device returns the same recorded license instead of consuming another seat.
- Symlinked key/ledger paths, loose key permissions, malformed device codes and unknown flags are rejected.
- stdout never contains the private key or ledger contents.

**Step 2: Run and verify failure**

```bash
cd helper && go test ./cmd/web-video-harbor-license-signer -v
```

Expected: FAIL because the command does not exist.

**Step 3: Implement the minimal CLI**

Use explicit subcommands:

```text
license-signer keygen --private-key <path>
license-signer issue --private-key <path> --ledger <path> --order <id> --device <code>
license-signer status --ledger <path> --order <id>
```

The ledger stores order ID, device code, license ID and token. It contains no customer name, email or payment details. Write it atomically with mode `0600`. Lock concurrent issuance so two processes cannot exceed two devices.

**Step 4: Protect production material**

Add exact ignores:

```gitignore
/work/license-production/
*.private.key
license-ledger.json
```

The README must state that production key generation is a manual release ceremony. Never add a sample production key; tests generate fixtures in temporary directories.

**Step 5: Run tests and secret scan**

```bash
cd helper && go test -race ./cmd/web-video-harbor-license-signer -v
rg -n --hidden -g '!work/**' -g '!.git/**' 'BEGIN (OPENSSH|PRIVATE) KEY|private\.key|license-ledger\.json' .
```

Expected: tests PASS; the scan finds only documentation/ignore references, not key material.

**Step 6: Commit in the private repository**

```bash
git add .gitignore helper/cmd/web-video-harbor-license-signer
git commit -m "feat: issue two-device pro licenses"
```

### Task 6: Integrate Pro activation into helper and Options

**Repository:** Private Pro repository.

**Files:**
- Modify: `helper/internal/api/server.go`
- Modify: `helper/internal/api/server_test.go`
- Modify: `helper/internal/api/engine.go`
- Modify: `helper/cmd/web-video-harbor-helper/main.go`
- Modify: `helper/cmd/web-video-harbor-helper/main_test.go`
- Modify: `extension/lib/helper-client.js`
- Modify: `extension/options.html`
- Modify: `extension/options.js`
- Modify: `extension/tests/helper-client.test.js`
- Modify: `extension/tests/wiring.test.js`

**Step 1: Write failing activation API tests**

Add authenticated `POST /v1/license/activate` with body `{ "license": "..." }`. Cover:

- Valid activation returns Pro/activated and never echoes the token.
- Invalid signature returns `license_invalid`.
- Wrong device returns `license_device_mismatch`.
- Unsupported schema/product returns `license_unsupported`.
- Oversized body, unknown JSON fields, wrong content type and unauthenticated calls are rejected.
- Logs and responses never contain full tokens or raw device UUIDs.

**Step 2: Implement activation route**

Extend the private policy with a narrow activation interface rather than type-asserting a concrete manager throughout the server:

```go
type Activator interface {
	Policy
	Activate(token string) (Status, error)
}
```

Permit only `POST /v1/license/activate`; keep `GET /v1/license` for status. Apply the existing strict JSON/body/CORS limits.

**Step 3: Wire Pro policy at startup**

- Resolve `license.json` relative to the existing verified config path.
- Obtain the device code without logging the raw identifier.
- Load the embedded public key and fail closed if it is invalid.
- Pass the same Pro policy instance to API and engine.
- `--version` may print `web-video-harbor-helper 0.3.0-pro`; it must not print activation or device data.
- Add `--print-device-code` for support, but never add a flag that prints the stored license.

**Step 4: Write failing Options tests**

Test that the Pro package:

- Displays a copyable device application code while unactivated.
- Accepts a pasted activation code only on explicit form submission.
- Clears the input after success.
- Displays only fixed safe errors.
- Never stores the activation code in Chrome storage or query strings.

**Step 5: Implement the activation form**

Add `activateLicense(token)` to the helper client and wire the options form. Do not use remote scripts, analytics, or automatic clipboard reads.

**Step 6: Run focused tests**

```bash
cd helper && go test -race ./internal/api ./cmd/web-video-harbor-helper -v
cd .. && node --test extension/tests/helper-client.test.js extension/tests/wiring.test.js
```

Expected: PASS.

**Step 7: Commit in the private repository**

```bash
git add helper extension
git commit -m "feat: activate pro from extension settings"
```

### Task 7: Produce clearly different Free and Pro packages

**Repositories:** Public Free repository and private Pro repository.

**Files:**
- Modify: `extension/manifest.json`
- Modify: `scripts/build-macos.zsh`
- Modify: `scripts/package-macos.zsh`
- Modify: `scripts/helper-status.zsh`
- Modify: `scripts/verify-doc-commands.zsh`
- Modify: `tests/scripts/macos_scripts_test.zsh`
- Modify: `tests/scripts/package_macos_test.zsh`
- Modify: `tests/repository_branding.test.mjs`
- Modify: `README.md`
- Modify: `docs/安装使用说明.md`
- Modify: `THIRD_PARTY_NOTICES.md`

**Step 1: Write failing Free package tests**

The public archive must be exactly `WebVideoHarbor-Free-macOS-v0.3.0.zip` and must:

- Report helper version `0.3.0-free`.
- Use manifest version `0.3.0` and visible name `网页视频港 Free`.
- Exclude `yt-dlp_macos`, Deno binaries and their package-only notices.
- Include the shared extension, MP4 helper, Free documentation and Go notices.
- Return Free capabilities and reject HLS/platform when smoke tested.

**Step 2: Update the public build/package scripts**

Remove production calls to `fetch-yt-dlp.zsh` and `fetch-deno.zsh` from the Free packager. Keep test-only fixture injection impossible in production. Do not delete the historical fetch scripts until the private repository is safely established and their third-party notices are retained there.

**Step 3: Run public package tests**

```bash
zsh tests/scripts/macos_scripts_test.zsh
zsh tests/scripts/package_macos_test.zsh
```

Expected: PASS and a fixture Free ZIP under `work/`, not `outputs/`.

**Step 4: Commit the public release split**

```bash
git add extension/manifest.json scripts tests README.md docs/安装使用说明.md THIRD_PARTY_NOTICES.md
git commit -m "build: package the v0.3 free edition"
```

**Step 5: Write failing Pro package tests in the private repository**

The private archive must be exactly `WebVideoHarbor-Pro-macOS-v0.3.0.zip` and must:

- Report helper version `0.3.0-pro`.
- Include pinned yt-dlp, Deno, their notices and the Pro activation UI.
- Contain the public verification key but no private key, signer binary, signer source, ledger, test token, device fixture, logs or customer data.
- Start unactivated, allow MP4, reject HLS/platform, accept a fixture license in tests, then allow HLS/platform.

**Step 6: Implement and test the private Pro packager**

Run:

```bash
zsh tests/scripts/macos_scripts_test.zsh
zsh tests/scripts/package_macos_test.zsh
```

Expected: PASS with only fixture keys/tokens created inside `work/` and cleaned afterward.

**Step 7: Commit in the private repository**

```bash
git add extension/manifest.json scripts tests README.md docs/安装使用说明.md THIRD_PARTY_NOTICES.md
git commit -m "build: package the v0.3 pro edition"
```

### Task 8: Documentation, licensing checkpoint and full verification

**Repositories:** Public Free and private Pro.

**Files:**
- Create: `docs/版本与购买.md`
- Create: `docs/隐私说明.md`
- Modify: `README.md`
- Modify: `docs/安装使用说明.md`
- Modify: `tests/repository_branding.test.mjs`
- Create in private repository: `docs/发码操作手册.md`

**Step 1: Write documentation assertions first**

Add repository tests requiring exact statements:

- Free supports direct HTTP/HTTPS MP4 only.
- Pro supports non-encrypted M3U8 and listed public single-video pages on a best-effort basis.
- No version promises every website, DRM, login, paid or regional bypass.
- Prices are ¥79 Pro, ¥39 installation, ¥99 bundle.
- Contact is `t3056339@163.com`.
- One purchase is for the purchaser's two Macs, with manual reset for replacement devices.
- Existing v0.2.1 history remains available but is not the maintained commercial Free channel.

**Step 2: Run the failing documentation test**

```bash
node --test tests/repository_branding.test.mjs
```

Expected: FAIL until the documents are updated.

**Step 3: Write customer-facing documents**

Keep marketing factual. Do not use “万能下载器”, “支持所有网站”, “破解”, “绕过限制” or guaranteed-success language.

The private issuing manual must cover: order numbering, device-count check, code issuance, safe copy/paste, manual reset records, key backup, and what to do if the private key is lost. It must not contain the production private key or real customer records.

**Step 4: Stop for the repository license decision**

The project currently has no first-party `LICENSE`. Before public commercial release, ask the user to choose a reviewed source-available/open-core license strategy. Do not silently add MIT, GPL, a custom commercial license, or claim “open source” without that decision. Third-party obligations remain independent and must be preserved.

**Step 5: Run complete automated verification in both repositories**

```bash
make test
cd helper && go test -race ./...
cd .. && node --test extension/tests/*.test.js tests/repository_branding.test.mjs
zsh tests/scripts/macos_scripts_test.zsh
zsh tests/scripts/package_macos_test.zsh
zsh scripts/verify-doc-commands.zsh
```

Expected: every command exits 0. Run the two repository suites separately and retain their outputs under `work/`.

**Step 6: Run edition-specific smoke tests**

Free:

- Download the local fixture MP4.
- Confirm M3U8 and platform cards say Pro is required.
- Confirm direct API attempts and retries cannot bypass the gate.

Pro:

- Before activation, download MP4 and reject M3U8/platform.
- Activate with an ephemeral fixture license.
- Download and merge the local non-encrypted M3U8 fixture.
- Run fake-yt-dlp integration; use no real account, Cookie or protected content.
- Confirm malformed licenses and a third-device signer request fail safely.

**Step 7: Inspect final archives**

For each archive verify SHA-256, universal helper architectures, expected edition/version, absence of symlinks and special files, and absence of secrets/customer data. Production packaging may write only through the task's `outputs/` link after running `ensure-central-outputs.zsh`.

Do not generate the production key, publish a GitHub Release, push the private repository, charge a customer, or send an external message without separate user authorization.

**Step 8: Commit documentation**

Public:

```bash
git add README.md docs tests/repository_branding.test.mjs
git commit -m "docs: explain free and pro editions"
```

Private:

```bash
git add README.md docs tests/repository_branding.test.mjs
git commit -m "docs: document pro activation operations"
```

## Release acceptance checklist

- Free official build can perform ordinary MP4 download without FFmpeg, yt-dlp or Deno.
- HLS/platform restrictions are enforced by the helper and engine, not only by disabled UI controls.
- Pro activation works offline and is bound to the displayed device code.
- One order cannot be issued for more than two distinct devices without an explicit reset operation.
- Public repository and Free ZIP contain no Pro signing private key, signer, ledger or customer data.
- Pro ZIP contains no private key, signer, ledger or customer data.
- Existing v0.2.1 history is preserved.
- All tests and smoke checks pass for both editions.
- No public push, private remote creation, production key ceremony or release publication occurs without user approval.
