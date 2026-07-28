'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');

const state = require('../lib/popup-state.js');

test('disconnected view asks the user to pair before downloading', () => {
  const view = state.buildViewModel({ connection: 'disconnected' });

  assert.deepEqual(view.connection, {
    label: '未连接本地助手',
    tone: 'offline',
    detail: '请先在设置中输入配对密钥。',
  });
  assert.equal(view.canDownload, false);
});

test('empty and scanning views explain what the popup is doing', () => {
  const empty = state.buildViewModel({ connection: 'connected', candidates: [] });
  const scanning = state.buildViewModel({ connection: 'connected', scanning: true, candidates: [] });
  const wechat = state.buildViewModel({
    connection: 'connected',
    candidates: [],
    pageUrl: 'https://channels.weixin.qq.com/watch/abc',
  });
  const blocked = state.buildViewModel({
    connection: 'connected',
    scanning: true,
    candidates: [],
    experimentalPlatformBlocked: true,
    pageUrl: 'https://www.youtube.com/watch?v=_mVb1D8wHxg',
  });

  assert.equal(empty.emptyMessage, '尚未发现可下载的视频');
  assert.equal(scanning.emptyMessage, '正在重新扫描当前页面…');
  assert.equal(wechat.emptyMessage, '请先在浏览器中播放视频几秒，再重新扫描');
  assert.equal(blocked.emptyMessage, '实验性平台兼容尚未开启，可在设置中阅读说明后开启');
});

test('candidate view distinguishes MP4, WebM, and HLS and keeps useful metadata', () => {
  const view = state.buildViewModel({
    connection: 'connected',
    candidates: [
      { url: 'https://cdn.example/movie.mp4', kind: 'mp4', title: '一段视频', width: 1920, height: 1080 },
      { url: 'https://cdn.example/movie.webm', kind: 'webm', title: 'WebM' },
      { url: 'https://cdn.example/master.m3u8', kind: 'hls', title: '直播回放' },
    ],
  });

  assert.deepEqual(view.candidates.map((item) => ({ typeLabel: item.typeLabel, detail: item.detail })), [
    { typeLabel: 'MP4', detail: '1920 × 1080' },
    { typeLabel: 'WebM', detail: '可直接下载' },
    { typeLabel: 'M3U8', detail: '需要检查可用画质' },
  ]);
  assert.equal(view.canDownload, true);
});

test('platform candidate uses its provider label and fixed public-video guidance', () => {
  const view = state.buildViewModel({
    connection: 'connected',
    candidates: [
      {
        url: 'https://www.youtube.com/watch?v=_mVb1D8wHxg',
        kind: 'platform',
        provider: 'youtube',
        title: '<img src=x onerror=alert(1)>',
        qualityOptions: [
          { value: 'best', label: '最佳画质' },
          { value: '1080', label: '1080P' },
          { value: '720', label: '720P' },
        ],
        selectedQuality: 'best',
      },
      {
        url: 'https://www.bilibili.com/video/BV1K3Gz6pEoo',
        kind: 'platform',
        provider: 'bilibili',
        title: 'B站视频',
      },
    ],
  });

  assert.deepEqual(view.candidates.map((item) => item.typeLabel), ['YouTube', '哔哩哔哩']);
  assert.deepEqual(view.candidates.map((item) => item.detail), [
    '仅支持无需登录即可观看的公开视频',
    '仅支持无需登录即可观看的公开视频',
  ]);
  assert.equal(view.candidates[0].kind, 'platform');
  assert.equal(view.candidates[0].selectedQuality, 'best');
  assert.deepEqual(view.candidates[0].qualityOptions.map((item) => item.value), ['best', '1080', '720']);
  assert.equal(view.candidates[0].title, '<img src=x onerror=alert(1)>');
});

test('HLS variants sort by height then bandwidth without mutating input', () => {
  const variants = [
    { url: 'https://cdn.example/720-low.m3u8', label: '720p', height: 720, bandwidth: 1800000 },
    { url: 'https://cdn.example/audio.m3u8', label: '256 kbps', bandwidth: 256000 },
    { url: 'https://cdn.example/1080.m3u8', label: '1080p', height: 1080, bandwidth: 5200000 },
    { url: 'https://cdn.example/720-high.m3u8', label: '720p 高码率', height: 720, bandwidth: 2800000 },
  ];

  const sorted = state.sortHlsVariants(variants);

  assert.deepEqual(sorted.map((item) => item.label), ['1080p', '720p 高码率', '720p', '256 kbps']);
  assert.equal(variants[0].label, '720p');
});

test('HLS media-playlist inspection without variants becomes an original-quality download', () => {
  const candidate = {
    url: 'https://cdn.example/media/index.m3u8?signature=kept',
    kind: 'hls',
    title: '单清晰度回放',
  };

  const inspected = state.applyInspection(candidate, { mediaType: 'hls', variants: [] });
  const [view] = state.buildViewModel({ connection: 'connected', candidates: [inspected] }).candidates;

  assert.deepEqual(view.variants, [{
    url: candidate.url,
    label: '原始画质',
  }]);
  assert.equal(view.error, '');
  assert.equal(view.inspecting, false);
});

test('HLS master-playlist inspection keeps every variant sorted for selection', () => {
  const inspected = state.applyInspection({
    url: 'https://cdn.example/master.m3u8', kind: 'hls', title: '多码率回放',
  }, {
    mediaType: 'hls',
    variants: [
      { url: 'https://cdn.example/720.m3u8', label: '720p', height: 720, bandwidth: 2200000 },
      { url: 'https://cdn.example/1080.m3u8', label: '1080p', height: 1080, bandwidth: 5000000 },
    ],
  });

  assert.deepEqual(inspected.variants.map((variant) => variant.label), ['1080p', '720p']);
  assert.equal(inspected.url, 'https://cdn.example/master.m3u8');
});

test('task lifecycle maps to concise Chinese status and valid actions', () => {
  const cases = [
    ['queued', '等待中', true, false, false],
    ['downloading', '下载中', true, false, false],
    ['merging', '正在合并音视频', true, false, false],
    ['completed', '已完成', false, false, true],
    ['failed', '下载失败', false, true, false],
    ['canceled', '已取消', false, true, false],
  ];

  for (const [status, label, canCancel, canRetry, canReveal] of cases) {
    const [task] = state.buildViewModel({
      connection: 'connected',
      tasks: [{ id: `task-${status}`, status, title: '测试', progress: 43 }],
    }).tasks;
    assert.equal(task.statusLabel, label, status);
    assert.equal(task.canCancel, canCancel, status);
    assert.equal(task.canRetry, canRetry, status);
    assert.equal(task.canReveal, canReveal, status);
  }
});

test('task progress is bounded and failure details remain short', () => {
  const view = state.buildViewModel({
    connection: 'connected',
    tasks: [
      { id: 'over', status: 'downloading', title: 'A', progress: 900 },
      { id: 'failed', status: 'failed', title: 'B', progress: -2, error: 'A'.repeat(300) },
    ],
  });

  assert.equal(view.tasks[0].progress, 100);
  assert.equal(view.tasks[1].progress, 0);
  assert.ok(view.tasks[1].detail.length <= 83);
});

test('setText renders untrusted user text through textContent only', () => {
  const element = { textContent: '' };
  const payload = '<img src=x onerror="globalThis.pwned=true">';

  state.setText(element, payload);

  assert.equal(element.textContent, payload);
  assert.equal(Object.hasOwn(element, 'innerHTML'), false);
});
