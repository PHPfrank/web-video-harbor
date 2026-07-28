'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');

const helper = require('../lib/helper-client.js');

function localStorage(initialToken = '') {
  const values = initialToken ? { videoHelperToken: initialToken } : {};
  return {
    values,
    get(key, callback) { callback({ [key]: values[key] }); },
    set(next, callback) { Object.assign(values, next); callback(); },
  };
}

function jsonResponse(status, body) {
  return {
    ok: status >= 200 && status < 300,
    status,
    async json() { return body; },
  };
}

test('health is unauthenticated while every v1 request uses the locally stored token', async () => {
  const calls = [];
  const client = helper.createHelperClient({
    storageLocal: localStorage('pairing-token'),
    async fetchImpl(url, options) {
      calls.push({ url, options });
      return url.endsWith('/health')
        ? jsonResponse(200, {
          ready: true,
          version: '0.2.0',
          ffmpeg: true,
          pid: 4321,
          platformDownloader: { available: true, version: '2026.07.04' },
          javascriptRuntime: { available: true, version: '2.8.1' },
        })
        : jsonResponse(200, []);
    },
  });

  const health = await client.health();
  await client.listTasks();

  assert.equal(health.pid, 4321);
  assert.deepEqual(health.platformDownloader, { available: true, version: '2026.07.04' });
  assert.deepEqual(health.javascriptRuntime, { available: true, version: '2.8.1' });
  assert.equal(calls[0].url, 'http://127.0.0.1:17432/health');
  assert.equal(calls[0].options.headers['X-Video-Helper-Token'], undefined);
  assert.equal(calls[1].url, 'http://127.0.0.1:17432/v1/tasks');
  assert.equal(calls[1].options.headers['X-Video-Helper-Token'], 'pairing-token');
});

test('health normalizes missing or malformed platform downloader status safely', async () => {
  const unsafeCases = [
    undefined,
    {},
    null,
    'available',
    { available: 'true', version: '/Users/person/private-parser' },
    { available: true, version: '2026.07.04\n/private-parser', path: '/Users/person/private-parser' },
  ];

  for (const platformDownloader of unsafeCases) {
    const client = helper.createHelperClient({
      storageLocal: localStorage(),
      async fetchImpl() {
        return jsonResponse(200, {
          ready: true,
          version: '0.2.0',
          ffmpeg: true,
          pid: 4321,
          path: '/Users/person/helper',
          ...(platformDownloader === undefined ? {} : { platformDownloader }),
        });
      },
    });

    const health = await client.health();
    assert.deepEqual(health.platformDownloader, { available: false, version: '' });
    assert.equal(Object.hasOwn(health, 'path'), false);
    assert.doesNotMatch(JSON.stringify(health), /Users|private-parser/);
  }
});

test('health rejects unbounded platform downloader versions without throwing', async () => {
  const client = helper.createHelperClient({
    storageLocal: localStorage(),
    async fetchImpl() {
      return jsonResponse(200, {
        ready: true,
        version: '0.2.0',
        ffmpeg: true,
        pid: 4321,
        platformDownloader: { available: true, version: '1'.repeat(65) },
      });
    },
  });

  const health = await client.health();
  assert.deepEqual(health.platformDownloader, { available: false, version: '' });
});

test('health normalizes missing or malformed JavaScript runtime status safely', async () => {
  const unsafeCases = [
    undefined,
    {},
    null,
    'available',
    { available: 'true', version: '/Users/person/private-runtime' },
    { available: true, version: '2.8.1\n/private-runtime', path: '/Users/person/private-runtime' },
    { available: true, version: '1'.repeat(65) },
  ];

  for (const javascriptRuntime of unsafeCases) {
    const client = helper.createHelperClient({
      storageLocal: localStorage(),
      async fetchImpl() {
        return jsonResponse(200, {
          ready: true,
          version: '0.2.1',
          ffmpeg: true,
          pid: 4321,
          path: '/Users/person/helper',
          ...(javascriptRuntime === undefined ? {} : { javascriptRuntime }),
        });
      },
    });

    const health = await client.health();
    assert.deepEqual(health.javascriptRuntime, { available: false, version: '' });
    assert.equal(Object.hasOwn(health, 'path'), false);
    assert.doesNotMatch(JSON.stringify(health), /Users|private-runtime/);
  }
});

test('helper address stays fixed even if a caller supplies a different base URL', async () => {
  let requestedURL = '';
  const client = helper.createHelperClient({
    baseUrl: 'https://attacker.example',
    storageLocal: localStorage(),
    async fetchImpl(url) {
      requestedURL = url;
      return jsonResponse(200, { ready: true });
    },
  });

  await client.health();

  assert.equal(requestedURL, 'http://127.0.0.1:17432/health');
});

test('inspect and task actions match the Go API request shape', async () => {
  const calls = [];
  const client = helper.createHelperClient({
    storageLocal: localStorage('secret'),
    async fetchImpl(url, options) {
      calls.push({ url, options });
      if (url.endsWith('/inspect')) {
        return jsonResponse(200, { mediaType: 'hls', variants: [{ url: 'https://cdn.example/1080.m3u8', label: '1080p' }] });
      }
      return jsonResponse(url.endsWith('/retry') ? 201 : 200, { id: 'task-1', status: 'queued' });
    },
  });

  const inspection = await client.inspect('https://cdn.example/master.m3u8');
  await client.createTask({ url: inspection.variants[0].url, title: '视频', mediaType: 'hls' });
  await client.cancelTask('task-1');
  await client.retryTask('task-1');
  await client.revealTask('task-1');

  assert.deepEqual(JSON.parse(calls[0].options.body), { url: 'https://cdn.example/master.m3u8' });
  assert.deepEqual(JSON.parse(calls[1].options.body), {
    url: 'https://cdn.example/1080.m3u8', title: '视频', mediaType: 'hls',
  });
  assert.deepEqual(calls.slice(2).map((call) => call.url), [
    'http://127.0.0.1:17432/v1/tasks/task-1/cancel',
    'http://127.0.0.1:17432/v1/tasks/task-1/retry',
    'http://127.0.0.1:17432/v1/tasks/task-1/reveal',
  ]);
});

test('settings GET is authenticated and normalizes only a complete current acknowledgment', async () => {
  const calls = [];
  const responses = [
    {
      experimentalPlatformCompatibilityEnabled: true,
      platformNoticeVersion: '2026-07-28-v1',
      currentPlatformNoticeVersion: '2026-07-28-v1',
      path: '/Users/person/settings.json',
    },
    {
      experimentalPlatformCompatibilityEnabled: true,
      platformNoticeVersion: 'stale',
      currentPlatformNoticeVersion: '2026-07-28-v1',
    },
    {
      experimentalPlatformCompatibilityEnabled: 'true',
      platformNoticeVersion: '2026-07-28-v1',
      currentPlatformNoticeVersion: '/Users/person/private',
    },
    null,
  ];
  const client = helper.createHelperClient({
    storageLocal: localStorage('pairing-token'),
    async fetchImpl(url, options) {
      calls.push({ url, options });
      return jsonResponse(200, responses.shift());
    },
  });

  assert.deepEqual(await client.getSettings(), {
    experimentalPlatformCompatibilityEnabled: true,
    platformNoticeVersion: '2026-07-28-v1',
    currentPlatformNoticeVersion: '2026-07-28-v1',
  });
  for (let index = 0; index < 3; index += 1) {
    const settings = await client.getSettings();
    assert.equal(settings.experimentalPlatformCompatibilityEnabled, false);
    assert.equal(settings.platformNoticeVersion, '');
    if (index >= 1) assert.equal(settings.currentPlatformNoticeVersion, '');
    assert.doesNotMatch(JSON.stringify(settings), /Users|private|path/);
  }
  assert.equal(calls.every((call) => call.url === 'http://127.0.0.1:17432/v1/settings'), true);
  assert.equal(calls.every((call) => call.options.headers['X-Video-Helper-Token'] === 'pairing-token'), true);
});

test('settings normalizer accepts bounded notice versions and fails closed otherwise', () => {
  assert.deepEqual(helper.normalizeSettings({
    experimentalPlatformCompatibilityEnabled: false,
    platformNoticeVersion: '',
    currentPlatformNoticeVersion: '2026-07-28-v1',
  }), {
    experimentalPlatformCompatibilityEnabled: false,
    platformNoticeVersion: '',
    currentPlatformNoticeVersion: '2026-07-28-v1',
  });
  for (const unsafe of ['', ' old', '2026-07-28-v1\n/private', 'x'.repeat(33), '/Users/person']) {
    assert.deepEqual(helper.normalizeSettings({
      experimentalPlatformCompatibilityEnabled: true,
      platformNoticeVersion: unsafe,
      currentPlatformNoticeVersion: unsafe,
    }), {
      experimentalPlatformCompatibilityEnabled: false,
      platformNoticeVersion: '',
      currentPlatformNoticeVersion: '',
    });
  }
});

test('settings PUT constructs the exact authenticated enable and disable bodies', async () => {
  const calls = [];
  const client = helper.createHelperClient({
    storageLocal: localStorage('pairing-token'),
    async fetchImpl(url, options) {
      calls.push({ url, options });
      const body = JSON.parse(options.body);
      return jsonResponse(200, {
        experimentalPlatformCompatibilityEnabled: body.enabled,
        platformNoticeVersion: body.enabled ? body.noticeVersion : '',
        currentPlatformNoticeVersion: '2026-07-28-v1',
      });
    },
  });

  await client.setPlatformCompatibility({
    enabled: true,
    acknowledged: true,
    noticeVersion: '2026-07-28-v1',
    ignored: '/Users/person/private',
  });
  await client.setPlatformCompatibility({ enabled: false, acknowledged: true, noticeVersion: 'stale' });

  assert.deepEqual(calls.map((call) => JSON.parse(call.options.body)), [
    { enabled: true, acknowledged: true, noticeVersion: '2026-07-28-v1' },
    { enabled: false },
  ]);
  assert.equal(calls.every((call) => call.options.method === 'PUT'), true);
  assert.equal(calls.every((call) => call.options.headers['X-Video-Helper-Token'] === 'pairing-token'), true);
  assert.equal(calls.every((call) => call.url === 'http://127.0.0.1:17432/v1/settings/platform-compatibility'), true);
});

test('settings PUT refuses unsafe local enable requests before fetching', async () => {
  let fetchCalls = 0;
  const client = helper.createHelperClient({
    storageLocal: localStorage('pairing-token'),
    async fetchImpl() { fetchCalls += 1; return jsonResponse(200, {}); },
  });
  await assert.rejects(client.setPlatformCompatibility({ enabled: true, acknowledged: false, noticeVersion: '2026-07-28-v1' }), {
    code: 'invalid_acknowledgment',
    message: '请先阅读并确认实验性平台兼容使用边界',
  });
  for (const noticeVersion of ['', 'stale', '/Users/person/private', 'x'.repeat(33)]) {
    await assert.rejects(client.setPlatformCompatibility({ enabled: true, acknowledged: true, noticeVersion }), {
      code: 'notice_outdated',
      message: '使用提示已更新，请重新阅读后确认',
    });
  }
  assert.equal(fetchCalls, 0);
});

test('v1 calls fail locally when no pairing token is stored', async () => {
  let fetched = false;
  const client = helper.createHelperClient({
    storageLocal: localStorage(),
    async fetchImpl() { fetched = true; return jsonResponse(200, []); },
  });

  await assert.rejects(client.listTasks(), { message: '请先完成本地助手配对' });
  assert.equal(fetched, false);
});

test('request timeout aborts work and returns a short Chinese message', async () => {
  const client = helper.createHelperClient({
    storageLocal: localStorage(),
    timeoutMs: 5,
    fetchImpl(_url, options) {
      return new Promise((_resolve, reject) => {
        options.signal.addEventListener('abort', () => reject(new Error('network details should stay private')));
      });
    },
  });

  await assert.rejects(client.health(), { message: '本地助手响应超时' });
});

test('timeout also covers a stalled JSON response body', async () => {
  const client = helper.createHelperClient({
    storageLocal: localStorage(),
    timeoutMs: 5,
    async fetchImpl(_url, options) {
      return {
        ok: true,
        status: 200,
        json() {
          return new Promise((_resolve, reject) => {
            options.signal.addEventListener('abort', () => reject(new Error('body aborted')));
          });
        },
      };
    },
  });

  await assert.rejects(client.health(), { message: '本地助手响应超时', code: 'timeout' });
});

test('remote and network errors never expose response text, URL, or token', async () => {
  const secret = 'very-private-token';
  const videoURL = 'https://private.example/video.m3u8?signature=sensitive';
  const client = helper.createHelperClient({
    storageLocal: localStorage(secret),
    async fetchImpl() {
      return jsonResponse(500, { code: 'internal_error', message: `${secret} ${videoURL}` });
    },
  });

  await assert.rejects(async () => {
    await client.inspect(videoURL);
  }, (error) => {
    assert.equal(error.message, '本地助手暂时无法完成此操作');
    assert.doesNotMatch(error.message, /private|sensitive|token|https?:/i);
    return true;
  });
});

test('platform compatibility errors use fixed safe Chinese messages', async () => {
  const cases = [
    ['verification_required', 'YouTube 要求浏览器验证；为保护账号隐私，网页视频港不会读取登录信息'],
    ['network_filtered', '当前网络阻止了本地下载连接，请联系网络管理员或更换网络'],
    ['javascript_runtime', '视频解析组件不完整，请重新安装网页视频港'],
    ['platform_compatibility_disabled', '实验性平台兼容尚未开启'],
    ['invalid_acknowledgment', '请先阅读并确认实验性平台兼容使用边界'],
    ['notice_outdated', '使用提示已更新，请重新阅读后确认'],
    ['settings_unavailable', '无法保存本地设置'],
  ];

  for (const [code, message] of cases) {
    const client = helper.createHelperClient({
      storageLocal: localStorage('secret'),
      async fetchImpl() {
        return jsonResponse(422, { code, message: 'https://private.example/?token=secret' });
      },
    });

    await assert.rejects(client.createTask({ mediaType: 'platform' }), { code, message });
  }
});

test('pairing token is trimmed and stored only through the supplied local storage area', async () => {
  const storageLocal = localStorage();

  await helper.saveToken(storageLocal, '  abc-123  ');

  assert.equal(await helper.readToken(storageLocal), 'abc-123');
  assert.deepEqual(storageLocal.values, { videoHelperToken: 'abc-123' });
});

test('local storage failures are converted to a short safe message', async () => {
  const storageLocal = {
    get() { return Promise.reject(new Error('private extension profile path')); },
  };

  await assert.rejects(helper.readToken(storageLocal), (error) => {
    assert.equal(error.message, '无法读取本地配对信息');
    assert.doesNotMatch(error.message, /private|profile|path/i);
    return true;
  });
});

test('known safe API error codes map to specific short Chinese messages', async () => {
  const cases = [
    ['http_status', '视频服务器拒绝了请求'],
    ['response_too_large', '视频清单过大'],
    ['invalid_request', '下载请求参数无效'],
    ['task_error', '本地助手无法执行该任务'],
  ];
  for (const [code, message] of cases) {
    const client = helper.createHelperClient({
      storageLocal: localStorage('secret'),
      async fetchImpl() { return jsonResponse(400, { code, message: 'untrusted detail' }); },
    });
    await assert.rejects(client.listTasks(), { message });
  }
});

test('health summary distinguishes a connected helper without FFmpeg', () => {
  assert.deepEqual(helper.describeHealth({ ready: true, version: '0.1.0', ffmpeg: false }), {
    message: '助手已连接，但未安装 FFmpeg',
    tone: 'error',
  });
  assert.deepEqual(helper.describeHealth({ ready: true, version: '0.1.0', ffmpeg: true }), {
    message: '连接成功，本地助手可以使用。',
    tone: 'success',
  });
  assert.deepEqual(helper.describeHealth({ ready: true }), {
    message: '连接成功，本地助手可以使用。',
    tone: 'success',
  });
});

test('abortAll cancels every active helper request', async () => {
  let aborts = 0;
  const client = helper.createHelperClient({
    storageLocal: localStorage(),
    fetchImpl(_url, options) {
      return new Promise((_resolve, reject) => {
        options.signal.addEventListener('abort', () => {
          aborts += 1;
          reject(new Error('aborted'));
        });
      });
    },
  });
  const pending = client.health();

  client.abortAll();

  await assert.rejects(pending, { code: 'aborted' });
  assert.equal(aborts, 1);
});
