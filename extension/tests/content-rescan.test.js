'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const popupState = require('../lib/popup-state.js');
const { createPopupController } = require('../lib/popup-controller.js');

const extensionDir = path.resolve(__dirname, '..');
const mediaSource = fs.readFileSync(path.join(extensionDir, 'lib/media.js'), 'utf8');
const contentSource = fs.readFileSync(path.join(extensionDir, 'content.js'), 'utf8');

function createContentHarness() {
  let videoURL = '';
  let messageListener = null;
  let storedCandidates = [];
  const pendingAdds = [];
  const video = {
    get currentSrc() { return videoURL; },
    src: '',
    videoWidth: 1280,
    videoHeight: 720,
    querySelectorAll() { return []; },
  };
  const chrome = {
    runtime: {
      lastError: undefined,
      onMessage: {
        addListener(listener) { messageListener = listener; },
      },
      sendMessage(message, callback) {
        if (message.type === 'CLAIM_DOCUMENT') {
          callback({ ok: true });
          return;
        }
        if (message.type === 'ADD_CANDIDATES') {
          pendingAdds.push({ message, callback });
        }
      },
    },
  };
  const document = {
    title: '延迟写入视频',
    documentElement: null,
    querySelectorAll(selector) { return selector === 'video' ? [video] : []; },
    addEventListener() {},
  };
  const context = vm.createContext({
    chrome,
    console,
    document,
    location: { href: 'https://example.com/watch' },
    performance: { getEntriesByType() { return []; } },
    URL,
    clearTimeout,
    setTimeout,
  });
  context.globalThis = context;
  vm.runInContext(mediaSource, context, { filename: 'lib/media.js' });
  vm.runInContext(contentSource, context, { filename: 'content.js' });

  function dispatchRescan() {
    return new Promise((resolve) => {
      const keepAlive = messageListener({ type: 'RESCAN' }, {}, resolve);
      assert.equal(keepAlive, true);
    });
  }

  return {
    chrome,
    dispatchRescan,
    pendingAdds,
    setVideoURL(value) { videoURL = value; },
    storedCandidates() { return storedCandidates; },
    completeAdd(index = 0) {
      const pending = pendingAdds.splice(index, 1)[0];
      assert.ok(pending, 'expected delayed ADD_CANDIDATES');
      storedCandidates = pending.message.candidates;
      pending.callback({ ok: true, candidates: storedCandidates });
    },
    failAdd() {
      const pending = pendingAdds.shift();
      assert.ok(pending, 'expected delayed ADD_CANDIDATES');
      chrome.runtime.lastError = { message: 'private runtime detail' };
      pending.callback(undefined);
      chrome.runtime.lastError = undefined;
    },
  };
}

function flush() {
  return new Promise((resolve) => setImmediate(resolve));
}

test('RESCAN waits for delayed background candidate storage before controller reads media', async () => {
  const content = createContentHarness();
  let mediaReads = 0;
  const controller = createPopupController({
    helper: {},
    viewState: popupState,
    scheduler: { setTimeout() { return 1; }, clearTimeout() {} },
    renderer: {
      renderStatus() {}, renderCandidates() {}, renderTasks() {}, setNotice() {},
    },
    bridge: {
      async rescan() {
        const response = await content.dispatchRescan();
        if (!response.ok) throw new Error(response.error);
        return response;
      },
      async getTabMedia() {
        mediaReads += 1;
        return { pageUrl: 'https://example.com/watch', candidates: content.storedCandidates() };
      },
    },
  });
  content.setVideoURL('https://cdn.example/new-video.mp4');

  const rescan = controller.rescan();
  await flush();
  assert.equal(content.pendingAdds.length, 1);
  assert.equal(mediaReads, 0);
  content.completeAdd();
  await rescan;

  assert.equal(mediaReads, 1);
  assert.equal(controller.snapshot().candidates[0].url, 'https://cdn.example/new-video.mp4');
});

test('RESCAN reports a safe failure when background candidate storage fails', async () => {
  const content = createContentHarness();
  content.setVideoURL('https://cdn.example/video.mp4');

  const responsePromise = content.dispatchRescan();
  await flush();
  content.failAdd();
  const response = await responsePromise;

  assert.deepEqual(JSON.parse(JSON.stringify(response)), { ok: false, error: '后台未能保存扫描结果' });
  assert.doesNotMatch(JSON.stringify(response), /private|runtime|detail/i);
});
