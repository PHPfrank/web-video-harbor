'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const repoRoot = process.env.SMOKE_REPO_ROOT;
const fixtureURL = process.env.SMOKE_FIXTURE_URL;
const helperToken = process.env.SMOKE_HELPER_TOKEN;
const downloadDir = process.env.SMOKE_DOWNLOAD_DIR;
const resultsPath = process.env.SMOKE_EXTENSION_RESULTS_PATH;
if (!repoRoot || !fixtureURL || !helperToken || !downloadDir || !resultsPath) {
  throw new Error('missing extension/helper smoke environment');
}

const media = require(path.join(repoRoot, 'extension/lib/media.js'));
const helperApi = require(path.join(repoRoot, 'extension/lib/helper-client.js'));
const popupState = require(path.join(repoRoot, 'extension/lib/popup-state.js'));
const popupController = require(path.join(repoRoot, 'extension/lib/popup-controller.js'));

const storageLocal = {
  get(key, callback) { callback({ [key]: helperToken }); },
  set(_value, callback) { callback(); },
};
const helper = helperApi.createHelperClient({ storageLocal, timeoutMs: 15000 });
const pageUrl = `${fixtureURL}/wechat-like-page`;
const candidates = media.mergeCandidates([
  {
    url: `${fixtureURL}/direct.mp4`, contentType: 'video/mp4', title: '网页视频下载器集成测试-扩展直连',
    source: 'dom', pageUrl,
  },
  {
    url: `${fixtureURL}/master.m3u8`, contentType: 'application/vnd.apple.mpegurl', title: '网页视频下载器集成测试-扩展HLS',
    source: 'dom', pageUrl,
  },
  {
    url: `${fixtureURL}/wechat-stream?id=fixture`, contentType: 'video/mp4', title: '微信式无扩展名响应',
    source: 'webRequest', pageUrl,
  },
]);

assert.equal(candidates.length, 3);
assert.equal(candidates.find((candidate) => candidate.url.includes('/wechat-stream')).kind, 'mp4');

const bridge = {
  async getTabMedia() { return { pageUrl, candidates }; },
  async rescan() { return { ok: true }; },
};
const renderer = {
  renderStatus() {}, renderCandidates() {}, renderTasks() {}, setNotice() {},
};
const scheduler = {
  setTimeout() { return 1; }, clearTimeout() {},
};
const controller = popupController.createPopupController({
  helper, bridge, renderer, viewState: popupState, scheduler,
});

async function waitCompleted(task) {
  const deadline = Date.now() + 20000;
  while (Date.now() < deadline) {
    const current = await helper.getTask(task.id);
    if (current.status === 'completed') return current;
    if (current.status === 'failed' || current.status === 'canceled') {
      throw new Error(`task ${task.id} reached ${current.status}: ${current.errorCode || ''}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  throw new Error(`task ${task.id} did not complete`);
}

(async () => {
  try {
    await controller.start();
    const started = controller.snapshot();
    assert.equal(started.connection, 'connected');
    assert.equal(started.candidates.length, 3);

    const hlsURL = `${fixtureURL}/master.m3u8`;
    const inspected = await controller.inspectCandidate(hlsURL);
    assert.ok(inspected);
    assert.equal(inspected.variants.length, 2);
    assert.equal(inspected.variants[0].label, '1080p');

    const directTask = await controller.downloadCandidate(`${fixtureURL}/direct.mp4`);
    const hlsTask = await controller.downloadCandidate(hlsURL);
    assert.ok(directTask && hlsTask);
    const [direct, hls] = await Promise.all([waitCompleted(directTask), waitCompleted(hlsTask)]);

    for (const task of [direct, hls]) {
      const relative = path.relative(downloadDir, task.outputPath);
      assert.ok(relative && relative !== '..' && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative));
      assert.ok(fs.statSync(task.outputPath).size > 0);
    }
    fs.writeFileSync(resultsPath, `${JSON.stringify({ direct: direct.outputPath, hls: hls.outputPath }, null, 2)}\n`, { mode: 0o600 });
    process.stdout.write('content/background unit coverage + popup/helper fallback smoke passed\n');
  } finally {
    controller.stop();
  }
})().catch((error) => {
  process.stderr.write(`${error && error.stack ? error.stack : error}\n`);
  process.exitCode = 1;
});
