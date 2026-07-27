'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');

const popupState = require('../lib/popup-state.js');
const platform = require('../lib/platform.js');
const { createPopupController } = require('../lib/popup-controller.js');

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((onResolve, onReject) => {
    resolve = onResolve;
    reject = onReject;
  });
  return { promise, resolve, reject };
}

function fakeRenderer() {
  return {
    status: [],
    candidates: [],
    tasks: [],
    notices: [],
    renderStatus(view) { this.status.push(structuredClone(view)); },
    renderCandidates(view) { this.candidates.push(structuredClone(view)); },
    renderTasks(view) { this.tasks.push(structuredClone(view)); },
    setNotice(message, tone) { this.notices.push({ message, tone }); },
  };
}

function fakeScheduler() {
  let nextID = 1;
  const timers = new Map();
  return {
    timers,
    setTimeout(callback, delay) {
      const id = nextID++;
      timers.set(id, { callback, delay });
      return id;
    },
    clearTimeout(id) { timers.delete(id); },
    runNext() {
      const entry = timers.entries().next().value;
      assert.ok(entry, 'expected a scheduled timer');
      const [id, timer] = entry;
      timers.delete(id);
      return timer.callback();
    },
    nextDelay() {
      const entry = timers.values().next().value;
      return entry && entry.delay;
    },
  };
}

function harness(overrides = {}) {
  const renderer = overrides.renderer || fakeRenderer();
  const scheduler = overrides.scheduler || fakeScheduler();
  const helper = {
    health: async () => ({
      ready: true,
      version: '0.2.0',
      ffmpeg: true,
      platformDownloader: { available: true, version: '2026.07.04' },
      javascriptRuntime: { available: true, version: '2.8.1' },
    }),
    listTasks: async () => [],
    inspect: async () => ({ mediaType: 'hls', variants: [] }),
    createTask: async (spec) => ({ id: 'created', title: spec.title, status: 'queued', progress: 0 }),
    cancelTask: async (id) => ({ id, title: 'task', status: 'canceled', progress: 0 }),
    retryTask: async () => ({ id: 'retry', title: 'task', status: 'queued', progress: 0 }),
    revealTask: async () => ({ revealed: true }),
    abortAll() {},
    ...overrides.helper,
  };
  const bridge = {
    getTabMedia: async () => ({ pageUrl: 'https://example.com/watch', candidates: [] }),
    rescan: async () => ({ ok: true }),
    ...overrides.bridge,
  };
  const controller = createPopupController({
    helper,
    bridge,
    renderer,
    scheduler,
    viewState: popupState,
    platformQualityOptions: platform.QUALITY_OPTIONS,
    connectedPollMs: 1200,
    disconnectedPollMs: 5000,
  });
  return { controller, helper, bridge, renderer, scheduler };
}

test('task refresh updates task/status regions without rebuilding candidate controls', async () => {
  const media = {
    pageUrl: 'https://example.com/watch',
    candidates: [{ url: 'https://cdn.example/master.m3u8', kind: 'hls', title: 'HLS' }],
  };
  const { controller, renderer } = harness({
    bridge: { getTabMedia: async () => media },
    helper: {
      listTasks: async () => [{ id: 'stable', title: 'task', status: 'downloading', progress: 20 }],
    },
  });
  await controller.refreshCandidates();
  await controller.refreshTasks();
  const candidateRenders = renderer.candidates.length;

  await controller.refreshTasks();

  assert.equal(renderer.candidates.length, candidateRenders);
  assert.equal(renderer.tasks.length, 1);
  assert.equal(renderer.status.length, 2);
});

test('changed task data rerenders once and preserves focused action by task ID', async () => {
  const taskResults = [
    [{ id: 'task-1', title: 'task', status: 'downloading', progress: 20 }],
    [{ id: 'task-1', title: 'task', status: 'downloading', progress: 20 }],
    [{ id: 'task-1', title: 'task', status: 'downloading', progress: 21 }],
  ];
  const { controller, renderer } = harness({
    helper: { listTasks: async () => taskResults.shift() },
  });
  await controller.refreshTasks();
  controller.focusTask('task-1', 'cancel');
  await controller.refreshTasks();
  const stableRenders = renderer.tasks.length;

  await controller.refreshTasks();

  assert.equal(renderer.tasks.length, stableRenders + 1);
  assert.deepEqual(renderer.tasks.at(-1).focusedTask, { id: 'task-1', action: 'cancel' });
});

test('capability rerender preserves selected HLS variant and focused control by candidate URL', async () => {
  const healthResults = [
    { ready: true, ffmpeg: true },
    { ready: true, ffmpeg: true },
    { ready: true, ffmpeg: false },
  ];
  const candidateURL = 'https://cdn.example/master.m3u8';
  const selectedURL = 'https://cdn.example/720.m3u8';
  const { controller, renderer } = harness({
    helper: { health: async () => healthResults.shift(), listTasks: async () => [] },
    bridge: {
      getTabMedia: async () => ({
        pageUrl: 'https://example.com',
        candidates: [{
          url: candidateURL,
          kind: 'hls',
          title: 'HLS',
          variants: [
            { url: 'https://cdn.example/1080.m3u8', label: '1080p' },
            { url: selectedURL, label: '720p' },
          ],
        }],
      }),
    },
  });
  await controller.refreshCandidates();
  await controller.refreshTasks();
  controller.selectVariant(candidateURL, selectedURL);
  controller.focusCandidate(candidateURL, 'quality');
  await controller.refreshTasks();
  const stableRenderCount = renderer.candidates.length;

  await controller.refreshTasks();

  assert.equal(renderer.candidates.length, stableRenderCount + 1);
  const latest = renderer.candidates.at(-1);
  assert.equal(latest.candidates[0].selectedVariant, selectedURL);
  assert.deepEqual(latest.focusedCandidate, { url: candidateURL, control: 'quality' });
});

test('polling is single-flight, self-schedules after completion, retries slowly, and recovers', async () => {
  const firstHealth = deferred();
  let healthCalls = 0;
  let taskCalls = 0;
  const { controller, scheduler, renderer } = harness({
    helper: {
      health() {
        healthCalls += 1;
        if (healthCalls === 1) return firstHealth.promise;
        if (healthCalls === 2) return Promise.reject(new Error('offline'));
        return Promise.resolve({ ready: true, ffmpeg: true });
      },
      async listTasks() { taskCalls += 1; return []; },
    },
  });

  const first = controller.startPolling();
  const duplicate = controller.startPolling();
  assert.strictEqual(duplicate, first);
  assert.equal(healthCalls, 1);
  assert.equal(scheduler.timers.size, 0);
  firstHealth.resolve({ ready: true, ffmpeg: true });
  await first;
  assert.equal(taskCalls, 1);
  assert.equal(scheduler.nextDelay(), 1200);

  await scheduler.runNext();
  assert.equal(healthCalls, 2);
  assert.equal(scheduler.nextDelay(), 5000);
  assert.equal(renderer.status.at(-1).connection.tone, 'offline');

  await scheduler.runNext();
  assert.equal(healthCalls, 3);
  assert.equal(taskCalls, 2);
  assert.equal(renderer.status.at(-1).connection.tone, 'online');
  assert.equal(scheduler.nextDelay(), 1200);
});

test('stop clears the next poll and aborts active helper requests', async () => {
  let aborted = 0;
  const { controller, scheduler } = harness({ helper: { abortAll() { aborted += 1; } } });
  await controller.startPolling();
  assert.equal(scheduler.timers.size, 1);

  controller.stop();

  assert.equal(scheduler.timers.size, 0);
  assert.equal(aborted, 1);
});

test('candidate pending lock prevents concurrent duplicate downloads and exposes busy state', async () => {
  const creation = deferred();
  let createCalls = 0;
  const candidateURL = 'https://cdn.example/video.mp4';
  const { controller, renderer } = harness({
    bridge: {
      getTabMedia: async () => ({
        pageUrl: 'https://example.com',
        candidates: [{ url: candidateURL, kind: 'mp4', title: 'MP4' }],
      }),
    },
    helper: {
      createTask() { createCalls += 1; return creation.promise; },
    },
  });
  await controller.refreshCandidates();
  await controller.refreshTasks();

  const first = controller.downloadCandidate(candidateURL);
  const duplicate = controller.downloadCandidate(candidateURL);
  assert.strictEqual(duplicate, first);
  assert.equal(createCalls, 1);
  assert.equal(renderer.candidates.at(-1).candidates[0].pending, true);
  creation.resolve({ id: 'created', title: 'MP4', status: 'queued', progress: 0 });
  await first;
  assert.equal(renderer.candidates.at(-1).candidates[0].pending, false);
});

test('rejected download restores the button and shows a safe visible error', async () => {
  const candidateURL = 'https://cdn.example/video.mp4';
  const secret = 'https://private.example/video.mp4?token=secret';
  const { controller, renderer } = harness({
    bridge: {
      getTabMedia: async () => ({
        pageUrl: 'https://example.com',
        candidates: [{ url: candidateURL, kind: 'mp4', title: 'MP4' }],
      }),
    },
    helper: {
      createTask: async () => { throw new Error(secret); },
    },
  });
  await controller.refreshCandidates();
  await controller.refreshTasks();

  const result = await controller.downloadCandidate(candidateURL);

  assert.equal(result, null);
  assert.equal(renderer.candidates.at(-1).candidates[0].pending, false);
  assert.equal(renderer.candidates.at(-1).candidates[0].error, '无法创建下载任务');
  assert.equal(renderer.notices.at(-1).message, '无法创建下载任务');
  assert.doesNotMatch(JSON.stringify(renderer.notices), /private|token|https?:/i);
});

test('task pending lock prevents duplicate actions and restores controls in finally', async () => {
  const cancellation = deferred();
  let cancelCalls = 0;
  const { controller, renderer } = harness({
    helper: {
      listTasks: async () => [{ id: 'task-1', title: 'task', status: 'downloading', progress: 20 }],
      cancelTask() { cancelCalls += 1; return cancellation.promise; },
    },
  });
  await controller.refreshTasks();

  const first = controller.taskAction('task-1', 'cancel');
  const duplicate = controller.taskAction('task-1', 'cancel');
  assert.strictEqual(duplicate, first);
  assert.equal(cancelCalls, 1);
  assert.equal(renderer.tasks.at(-1).tasks[0].pending, true);
  cancellation.reject(new Error('offline'));
  assert.equal(await first, null);
  assert.equal(renderer.tasks.at(-1).tasks[0].pending, false);
  assert.equal(renderer.notices.at(-1).message, '无法操作该任务');
});

test('health ffmpeg=false leaves MP4 usable but blocks HLS inspect and download', async () => {
  let inspectCalls = 0;
  let createCalls = 0;
  const mp4URL = 'https://cdn.example/video.mp4';
  const hlsURL = 'https://cdn.example/master.m3u8';
  const { controller, renderer } = harness({
    helper: {
      health: async () => ({ ready: true, version: '0.1.0', ffmpeg: false }),
      listTasks: async () => [],
      inspect: async () => { inspectCalls += 1; return { mediaType: 'hls', variants: [] }; },
      createTask: async (spec) => { createCalls += 1; return { id: spec.mediaType, title: 'task', status: 'queued' }; },
    },
    bridge: {
      getTabMedia: async () => ({
        pageUrl: 'https://example.com',
        candidates: [
          { url: mp4URL, kind: 'mp4', title: 'MP4' },
          { url: hlsURL, kind: 'hls', title: 'HLS' },
        ],
      }),
    },
  });
  await controller.refreshCandidates();
  await controller.refreshTasks();

  const view = renderer.candidates.at(-1);
  assert.equal(view.candidates.find((item) => item.url === mp4URL).canUse, true);
  assert.equal(view.candidates.find((item) => item.url === hlsURL).canUse, false);
  assert.match(view.candidates.find((item) => item.url === hlsURL).detail, /未安装 FFmpeg/);
  await controller.inspectCandidate(hlsURL);
  await controller.downloadCandidate(hlsURL);
  await controller.downloadCandidate(mp4URL);
  assert.equal(inspectCalls, 0);
  assert.equal(createCalls, 1);
});

test('platform candidate defaults to best, skips inspection, and sends the selected fixed quality', async () => {
  const candidateURL = 'https://www.youtube.com/watch?v=_mVb1D8wHxg';
  const createdSpecs = [];
  let inspectCalls = 0;
  const { controller, renderer } = harness({
    bridge: {
      getTabMedia: async () => ({
        pageUrl: candidateURL,
        candidates: [{
          url: candidateURL,
          kind: 'platform',
          provider: 'youtube',
          title: '页面标题',
        }],
      }),
    },
    helper: {
      inspect: async () => { inspectCalls += 1; return {}; },
      createTask: async (spec) => {
        createdSpecs.push(spec);
        return { id: 'platform-task', title: spec.title, status: 'queued', progress: 0 };
      },
    },
  });
  await controller.refreshCandidates();
  await controller.refreshTasks();

  const candidate = renderer.candidates.at(-1).candidates[0];
  assert.equal(candidate.selectedQuality, 'best');
  assert.deepEqual(candidate.qualityOptions, platform.QUALITY_OPTIONS);
  assert.equal(await controller.inspectCandidate(candidateURL), null);
  assert.equal(inspectCalls, 0);
  assert.equal(controller.selectQuality(candidateURL, '1080'), true);
  assert.equal(controller.selectQuality(candidateURL, '2160'), false);

  await controller.downloadCandidate(candidateURL);

  assert.deepEqual(createdSpecs, [{
    url: candidateURL,
    title: '页面标题',
    mediaType: 'platform',
    quality: '1080',
  }]);
});

test('platform availability requires parser, JavaScript runtime, and FFmpeg without blocking MP4', async () => {
  const platformURL = 'https://www.bilibili.com/video/BV1K3Gz6pEoo';
  const mp4URL = 'https://cdn.example/video.mp4';
  let createCalls = 0;
  const healthResults = [
    {
      ready: true,
      ffmpeg: true,
      platformDownloader: { available: false, version: '' },
      javascriptRuntime: { available: true, version: '2.8.1' },
    },
    {
      ready: true,
      ffmpeg: true,
      platformDownloader: { available: true, version: '2026.07.04' },
      javascriptRuntime: { available: false, version: '' },
    },
    {
      ready: true,
      ffmpeg: false,
      platformDownloader: { available: true, version: '2026.07.04' },
      javascriptRuntime: { available: true, version: '2.8.1' },
    },
  ];
  const { controller, renderer } = harness({
    bridge: {
      getTabMedia: async () => ({
        pageUrl: platformURL,
        candidates: [
          { url: platformURL, kind: 'platform', provider: 'bilibili', title: 'B站视频' },
          { url: mp4URL, kind: 'mp4', title: 'MP4' },
        ],
      }),
    },
    helper: {
      health: async () => healthResults.shift(),
      listTasks: async () => [],
      createTask: async () => { createCalls += 1; return { id: 'unexpected' }; },
    },
  });
  await controller.refreshCandidates();

  await controller.refreshTasks();
  let candidates = renderer.candidates.at(-1).candidates;
  assert.equal(candidates.find((item) => item.url === platformURL).canUse, false);
  assert.match(candidates.find((item) => item.url === platformURL).blockedReason, /安装包不完整.*解析器/);
  assert.equal(candidates.find((item) => item.url === mp4URL).canUse, true);

  await controller.refreshTasks();
  candidates = renderer.candidates.at(-1).candidates;
  assert.equal(candidates.find((item) => item.url === platformURL).canUse, false);
  assert.match(candidates.find((item) => item.url === platformURL).blockedReason, /安装包不完整.*JavaScript/);
  assert.equal(candidates.find((item) => item.url === mp4URL).canUse, true);
  await controller.downloadCandidate(platformURL);
  assert.equal(createCalls, 0);
  assert.match(renderer.notices.at(-1).message, /安装包不完整.*JavaScript/);

  await controller.refreshTasks();
  candidates = renderer.candidates.at(-1).candidates;
  assert.equal(candidates.find((item) => item.url === platformURL).canUse, false);
  assert.match(candidates.find((item) => item.url === platformURL).blockedReason, /FFmpeg/);
  assert.equal(candidates.find((item) => item.url === mp4URL).canUse, true);
});

test('HLS selection remains a variant URL and never becomes a platform quality value', async () => {
  const candidateURL = 'https://cdn.example/master.m3u8';
  const variantURL = 'https://cdn.example/720.m3u8';
  const createdSpecs = [];
  const { controller } = harness({
    bridge: {
      getTabMedia: async () => ({
        pageUrl: 'https://example.com/watch',
        candidates: [{
          url: candidateURL,
          kind: 'hls',
          title: 'HLS',
          variants: [{ url: variantURL, label: '720p', height: 720 }],
        }],
      }),
    },
    helper: {
      createTask: async (spec) => {
        createdSpecs.push(spec);
        return { id: 'hls-task', title: spec.title, status: 'queued', progress: 0 };
      },
    },
  });
  await controller.refreshCandidates();
  await controller.refreshTasks();

  assert.equal(controller.selectQuality(candidateURL, '1080'), false);
  assert.equal(controller.selectVariant(candidateURL, variantURL), true);
  await controller.downloadCandidate(candidateURL);

  assert.deepEqual(createdSpecs, [{ url: variantURL, title: 'HLS', mediaType: 'hls' }]);
});

test('rescan preserves platform quality and focused control when the canonical page URL remains', async () => {
  const candidateURL = 'https://www.youtube.com/watch?v=_mVb1D8wHxg';
  let scans = 0;
  const { controller, renderer } = harness({
    bridge: {
      getTabMedia: async () => ({
        pageUrl: candidateURL,
        candidates: [{ url: candidateURL, kind: 'platform', provider: 'youtube', title: '视频' }],
      }),
      rescan: async () => { scans += 1; return { ok: true }; },
    },
  });
  await controller.refreshCandidates();
  controller.selectQuality(candidateURL, '720');
  controller.focusCandidate(candidateURL, 'quality');

  await controller.rescan();

  assert.equal(scans, 1);
  const latest = renderer.candidates.at(-1);
  assert.equal(latest.candidates[0].selectedQuality, '720');
  assert.deepEqual(latest.focusedCandidate, { url: candidateURL, control: 'quality' });
});
