'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const extensionDir = path.resolve(__dirname, '..');
const mediaSource = fs.readFileSync(path.join(extensionDir, 'lib/media.js'), 'utf8');
const backgroundSource = fs.readFileSync(path.join(extensionDir, 'background.js'), 'utf8');

function eventHook() {
  const listeners = [];
  return {
    listeners,
    addListener(listener) { listeners.push(listener); },
  };
}

function createHarness() {
  const hooks = {
    before: eventHook(),
    headers: eventHook(),
    updated: eventHook(),
    removed: eventHook(),
    message: eventHook(),
  };
  const storageState = {};
  let now = 10_000;
  class FakeDate extends Date {
    static now() { return now; }
  }
  const chrome = {
    runtime: { lastError: undefined, onMessage: hooks.message },
    storage: {
      session: {
        get(key, callback) { callback({ [key]: storageState[key] }); },
        set(values, callback) { Object.assign(storageState, values); callback(); },
      },
    },
    webRequest: {
      onBeforeRequest: hooks.before,
      onHeadersReceived: hooks.headers,
    },
    tabs: {
      onUpdated: hooks.updated,
      onRemoved: hooks.removed,
      sendMessage(_tabId, _message, callback) { callback({ ok: true }); },
    },
  };
  const context = vm.createContext({ chrome, URL, Date: FakeDate, console });
  context.globalThis = context;
  context.importScripts = function importScripts(name) {
    assert.equal(name, 'lib/media.js');
    vm.runInContext(mediaSource, context, { filename: name });
  };
  vm.runInContext(backgroundSource, context, { filename: 'background.js' });

  async function dispatch(request, sender) {
    return new Promise((resolve) => hooks.message.listeners[0](request, sender || {}, resolve));
  }
  async function flush() {
    await new Promise((resolve) => setImmediate(resolve));
    await new Promise((resolve) => setImmediate(resolve));
  }

  return {
    hooks,
    dispatch,
    flush,
    advance(milliseconds) { now += milliseconds; },
  };
}

test('background detects an extensionless WeChat CDN response from Content-Type per tab', async () => {
  const harness = createHarness();
  harness.hooks.before.listeners[0]({
    tabId: 12,
    type: 'main_frame',
    url: 'https://channels.weixin.qq.com/watch?id=abc',
    timeStamp: 10_000,
  });
  await harness.dispatch({
    type: 'CLAIM_DOCUMENT',
    pageUrl: 'https://channels.weixin.qq.com/watch?id=abc',
  }, { tab: { id: 12 }, frameId: 0, documentId: 'wechat-document' });
  harness.hooks.headers.listeners[0]({
    tabId: 12,
    frameId: 0,
    documentId: 'wechat-document',
    type: 'xmlhttprequest',
    url: 'https://finder.video.qq.com/stream?id=abc',
    responseHeaders: [{ name: 'Content-Type', value: 'video/mp4; charset=binary' }],
  });
  await harness.flush();

  const found = await harness.dispatch({ type: 'GET_CANDIDATES', tabId: 12 });
  const otherTab = await harness.dispatch({ type: 'GET_CANDIDATES', tabId: 13 });
  assert.equal(found.ok, true);
  assert.equal(found.candidates.length, 1);
  assert.equal(found.candidates[0].kind, 'mp4');
  assert.equal(found.candidates[0].pageUrl, 'https://channels.weixin.qq.com/watch?id=abc');
  assert.equal(otherTab.candidates.length, 0);
});

test('explicit non-media response MIME removes an early suffix-based candidate', async () => {
  const harness = createHarness();
  const request = {
    tabId: 4,
    type: 'xmlhttprequest',
    url: 'https://example.com/error.mp4',
  };
  harness.hooks.before.listeners[0](request);
  await harness.flush();
  assert.equal((await harness.dispatch({ type: 'GET_CANDIDATES', tabId: 4 })).candidates.length, 1);

  harness.hooks.headers.listeners[0]({
    ...request,
    responseHeaders: [{ name: 'content-type', value: 'text/html' }],
  });
  await harness.flush();
  assert.equal((await harness.dispatch({ type: 'GET_CANDIDATES', tabId: 4 })).candidates.length, 0);
});

test('late loading notification for the same navigation does not erase new candidates', async () => {
  const harness = createHarness();
  harness.hooks.before.listeners[0]({
    tabId: 9,
    type: 'main_frame',
    url: 'https://example.com/watch',
    timeStamp: 10_000,
  });
  await harness.dispatch({
    type: 'CLAIM_DOCUMENT',
    pageUrl: 'https://example.com/watch',
  }, { tab: { id: 9 }, frameId: 0, documentId: 'current-document' });
  harness.hooks.headers.listeners[0]({
    tabId: 9,
    frameId: 0,
    documentId: 'current-document',
    type: 'media',
    url: 'https://cdn.example.com/movie.mp4',
    responseHeaders: [{ name: 'content-type', value: 'video/mp4' }],
  });
  await harness.flush();
  harness.advance(6_000);
  harness.hooks.updated.listeners[0](9, { status: 'loading' });
  await harness.flush();

  const result = await harness.dispatch({ type: 'GET_CANDIDATES', tabId: 9 });
  assert.equal(result.candidates.length, 1);
});

test('old document responses and messages cannot repopulate a newly navigated tab', async () => {
  const harness = createHarness();
  const oldSender = { tab: { id: 6 }, frameId: 0, documentId: 'old-document' };
  assert.equal((await harness.dispatch({
    type: 'CLAIM_DOCUMENT',
    pageUrl: 'https://old.example/watch',
  }, oldSender)).ok, true);
  await harness.dispatch({
    type: 'ADD_CANDIDATES',
    pageUrl: 'https://old.example/watch',
    candidates: [{ url: 'https://cdn.example/old.mp4', pageUrl: 'https://old.example/watch' }],
  }, oldSender);
  await harness.flush();

  harness.hooks.before.listeners[0]({
    tabId: 6,
    frameId: 0,
    documentId: 'old-document',
    type: 'main_frame',
    url: 'https://new.example/watch',
    timeStamp: 11_000,
  });
  await harness.flush();
  harness.hooks.headers.listeners[0]({
    tabId: 6,
    frameId: 0,
    documentId: 'old-document',
    type: 'media',
    url: 'https://cdn.example/old.mp4',
    responseHeaders: [{ name: 'content-type', value: 'video/mp4' }],
  });
  const staleMessage = await harness.dispatch(
    {
      type: 'ADD_CANDIDATES',
      pageUrl: 'https://new.example/watch',
      candidates: [{ url: 'https://cdn.example/stale.m3u8', pageUrl: 'https://new.example/watch' }],
    },
    { tab: { id: 6 }, frameId: 0, documentId: 'old-document' },
  );
  await harness.flush();

  assert.equal(staleMessage.ok, false);
  assert.equal((await harness.dispatch({ type: 'GET_CANDIDATES', tabId: 6 })).candidates.length, 0);

  const newClaim = await harness.dispatch({
    type: 'CLAIM_DOCUMENT',
    pageUrl: 'https://new.example/watch',
  }, { tab: { id: 6 }, frameId: 0, documentId: 'actual-new-document' });
  const newCandidate = await harness.dispatch({
    type: 'ADD_CANDIDATES',
    pageUrl: 'https://new.example/watch',
    candidates: [{ url: 'https://cdn.example/new.mp4', pageUrl: 'https://new.example/watch' }],
  }, { tab: { id: 6 }, frameId: 0, documentId: 'actual-new-document' });
  assert.equal(newClaim.ok, true);
  assert.equal(newCandidate.ok, true);
  assert.equal(newCandidate.candidates[0].pageUrl, 'https://new.example/watch');
});

test('navigation isolation rejects undocumented stale work but accepts a matching new-page claim', async () => {
  const harness = createHarness();
  await harness.dispatch({
    type: 'CLAIM_DOCUMENT',
    pageUrl: 'https://old.example/watch',
  }, { tab: { id: 3 }, frameId: 0 });
  harness.hooks.before.listeners[0]({
    tabId: 3,
    type: 'xmlhttprequest',
    requestId: 'old-request',
    url: 'https://cdn.example/late.mp4',
    timeStamp: 10_100,
  });
  harness.hooks.before.listeners[0]({
    tabId: 3,
    type: 'main_frame',
    url: 'https://new.example/watch',
    timeStamp: 11_000,
  });
  await harness.flush();

  harness.hooks.headers.listeners[0]({
    tabId: 3,
    type: 'xmlhttprequest',
    requestId: 'old-request',
    url: 'https://cdn.example/late.mp4',
    responseHeaders: [{ name: 'content-type', value: 'video/mp4' }],
  });
  harness.hooks.before.listeners[0]({
    tabId: 3,
    type: 'xmlhttprequest',
    requestId: 'new-request',
    url: 'https://cdn.example/current.m3u8',
    timeStamp: 11_100,
  });
  harness.hooks.headers.listeners[0]({
    tabId: 3,
    type: 'xmlhttprequest',
    requestId: 'new-request',
    url: 'https://cdn.example/current.m3u8',
    responseHeaders: [{ name: 'content-type', value: 'application/vnd.apple.mpegurl' }],
  });
  const staleMessage = await harness.dispatch({
    type: 'ADD_CANDIDATES',
    pageUrl: 'https://old.example/watch',
    candidates: [{ url: 'https://cdn.example/stale.mp4', pageUrl: 'https://old.example/watch' }],
  }, { tab: { id: 3 }, frameId: 0 });
  const newMessage = await harness.dispatch({
    type: 'ADD_CANDIDATES',
    pageUrl: 'https://new.example/watch',
    candidates: [{ url: 'https://cdn.example/new.mp4', pageUrl: 'https://old.example/untrusted' }],
  }, { tab: { id: 3 }, frameId: 0 });
  await harness.flush();

  assert.equal(staleMessage.ok, false);
  assert.equal(newMessage.ok, true);
  assert.equal(newMessage.candidates[1].pageUrl, 'https://new.example/watch');
  assert.deepEqual(
    Array.from((await harness.dispatch({ type: 'GET_CANDIDATES', tabId: 3 })).candidates, (item) => item.url),
    ['https://cdn.example/current.m3u8', 'https://cdn.example/new.mp4'],
  );
});
