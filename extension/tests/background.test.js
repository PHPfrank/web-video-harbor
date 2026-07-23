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

function createHarness(options = {}) {
  const hooks = {
    before: eventHook(),
    headers: eventHook(),
    completed: eventHook(),
    requestError: eventHook(),
    updated: eventHook(),
    removed: eventHook(),
    message: eventHook(),
  };
  const storageState = {};
  const pendingTabGets = [];
  const pendingStorageSets = [];
  let storageSetCalls = 0;
  let storageRemoveCalls = 0;
  const localAccessLevels = [];
  if (options.initialStorageCandidates) {
    storageState.videoGrabberCandidatesV1 = options.initialStorageCandidates;
  }
  let now = 10_000;
  class FakeDate extends Date {
    static now() { return now; }
  }
  const chrome = {
    runtime: { lastError: undefined, onMessage: hooks.message },
    storage: {
      local: options.localAccessLevelUnavailable ? {} : {
        setAccessLevel(options, callback) {
          localAccessLevels.push(options);
          if (callback) callback();
        },
      },
      session: {
        get(key, callback) { callback({ [key]: storageState[key] }); },
        set(values, callback) {
          storageSetCalls += 1;
          if (options.deferStorageSet) {
            pendingStorageSets.push({ values, callback });
            return;
          }
          if (options.storageSetFails) {
            chrome.runtime.lastError = { message: 'QUOTA_BYTES quota exceeded' };
            callback();
            chrome.runtime.lastError = undefined;
            return;
          }
          Object.assign(storageState, values);
          callback();
        },
        remove(key, callback) {
          storageRemoveCalls += 1;
          delete storageState[key];
          callback();
        },
      },
    },
    webRequest: {
      onBeforeRequest: hooks.before,
      onHeadersReceived: hooks.headers,
      onCompleted: hooks.completed,
      onErrorOccurred: hooks.requestError,
    },
    tabs: {
      onUpdated: hooks.updated,
      onRemoved: hooks.removed,
      get(tabId, callback) {
        if (options.deferTabGet) {
          pendingTabGets.push({ tabId, callback });
          return;
        }
        const url = options.tabUrls && options.tabUrls[tabId];
        callback(url ? { id: tabId, url } : undefined);
      },
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
    resolveTabGet(tabId, url) {
      const index = pendingTabGets.findIndex((pending) => pending.tabId === tabId);
      assert.notEqual(index, -1, `missing pending tabs.get for tab ${tabId}`);
      const [pending] = pendingTabGets.splice(index, 1);
      pending.callback(url ? { id: tabId, url } : undefined);
    },
    resolveTabGetAt(index, url) {
      assert.ok(index >= 0 && index < pendingTabGets.length, `missing pending tabs.get at ${index}`);
      const [pending] = pendingTabGets.splice(index, 1);
      pending.callback(url ? { id: pending.tabId, url } : undefined);
    },
    pendingTabGetCount() { return pendingTabGets.length; },
    storageSetCalls() { return storageSetCalls; },
    storageRemoveCalls() { return storageRemoveCalls; },
    localAccessLevels() { return localAccessLevels.slice(); },
    storageCandidates() { return storageState.videoGrabberCandidatesV1 || {}; },
    pendingStorageSetCount() { return pendingStorageSets.length; },
    resolveStorageSet() {
      assert.ok(pendingStorageSets.length, 'missing pending storage.session.set');
      const pending = pendingStorageSets.shift();
      Object.assign(storageState, pending.values);
      pending.callback();
    },
  };
}

test('cold-start headers cannot claim an old document but claimed current requests still work', async () => {
  const harness = createHarness({ tabUrls: { 90: 'https://new.example/watch' } });
  harness.hooks.headers.listeners[0]({
    tabId: 90,
    frameId: 0,
    documentId: 'old-document',
    requestId: 'late-old-request',
    type: 'xmlhttprequest',
    url: 'https://cdn.example/old.mp4',
    responseHeaders: [{ name: 'content-type', value: 'video/mp4' }],
  });
  await harness.flush();
  assert.equal((await harness.dispatch({ type: 'GET_CANDIDATES', tabId: 90 })).candidates.length, 0);

  const sender = { tab: { id: 90 }, frameId: 0, documentId: 'new-document' };
  assert.equal((await harness.dispatch({
    type: 'CLAIM_DOCUMENT',
    pageUrl: 'https://new.example/watch',
  }, sender)).ok, true);
  const currentRequest = {
    tabId: 90,
    frameId: 0,
    documentId: 'new-document',
    requestId: 'current-request',
    type: 'xmlhttprequest',
    url: 'https://finder.video.qq.com/extensionless?id=current',
  };
  harness.hooks.before.listeners[0](currentRequest);
  harness.hooks.headers.listeners[0]({
    ...currentRequest,
    responseHeaders: [{ name: 'content-type', value: 'video/mp4' }],
  });
  await harness.flush();

  const result = await harness.dispatch({ type: 'GET_CANDIDATES', tabId: 90 });
  assert.deepEqual(Array.from(result.candidates, (candidate) => candidate.url), [currentRequest.url]);
  assert.equal(result.candidates[0].pageUrl, 'https://new.example/watch');
});

test('GET_TAB_MEDIA reconciles restored candidates with the current tab URL', async () => {
  const oldCandidate = {
    url: 'https://cdn.example/old.mp4',
    pageUrl: 'https://old.example/watch',
    contentType: 'video/mp4',
    source: 'webRequest',
  };
  const changed = createHarness({
    initialStorageCandidates: { 92: [oldCandidate] },
    tabUrls: { 92: 'https://new.example/watch' },
  });
  const changedResult = await changed.dispatch({ type: 'GET_TAB_MEDIA', tabId: 92 });
  await changed.flush();
  assert.equal(changedResult.pageUrl, 'https://new.example/watch');
  assert.equal(changedResult.candidates.length, 0);
  assert.deepEqual(Object.keys(changed.storageCandidates()), []);

  const unchanged = createHarness({
    initialStorageCandidates: { 93: [{ ...oldCandidate, pageUrl: 'https://same.example/watch' }] },
    tabUrls: { 93: 'https://same.example/watch#player' },
  });
  const unchangedResult = await unchanged.dispatch({ type: 'GET_TAB_MEDIA', tabId: 93 });
  assert.equal(unchangedResult.pageUrl, 'https://same.example/watch');
  assert.equal(unchangedResult.candidates.length, 1);
  assert.equal(unchangedResult.candidates[0].pageUrl, unchangedResult.pageUrl);
});

test('background restricts local pairing storage to trusted extension contexts', async () => {
  const harness = createHarness();
  await harness.flush();
  assert.equal(harness.localAccessLevels().length, 1);
  assert.equal(harness.localAccessLevels()[0].accessLevel, 'TRUSTED_CONTEXTS');
});

test('background still starts when storage.local access-level control is unavailable', async () => {
  const harness = createHarness({ localAccessLevelUnavailable: true });
  await harness.flush();
  assert.equal(harness.localAccessLevels().length, 0);
  assert.equal(harness.hooks.before.listeners.length, 1);
  assert.equal(harness.hooks.message.listeners.length, 1);
});

test('popup bridge returns current page URL and candidates together', async () => {
  const harness = createHarness();
  const sender = { tab: { id: 91 }, frameId: 0, documentId: 'popup-bridge-document' };
  await harness.dispatch({
    type: 'CLAIM_DOCUMENT',
    pageUrl: 'https://channels.weixin.qq.com/watch/bridge',
  }, sender);
  await harness.dispatch({
    type: 'ADD_CANDIDATES',
    pageUrl: 'https://channels.weixin.qq.com/watch/bridge',
    candidates: [{ url: 'https://finder.video.qq.com/media.mp4', title: '视频号视频' }],
  }, sender);

  const result = await harness.dispatch({ type: 'GET_TAB_MEDIA', tabId: 91 });

  assert.equal(result.ok, true);
  assert.equal(result.pageUrl, 'https://channels.weixin.qq.com/watch/bridge');
  assert.equal(result.candidates.length, 1);
  assert.equal(result.candidates[0].pageUrl, result.pageUrl);
});

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
  const harness = createHarness({ tabUrls: { 4: 'https://example.com/watch' } });
  const request = {
    tabId: 4,
    frameId: 0,
    documentId: 'current-document',
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

test('navigation isolation rejects all undocumented network work but accepts a matching new-page claim', async () => {
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
  assert.equal(newMessage.candidates[0].pageUrl, 'https://new.example/watch');
  assert.deepEqual(
    Array.from((await harness.dispatch({ type: 'GET_CANDIDATES', tabId: 3 })).candidates, (item) => item.url),
    ['https://cdn.example/new.mp4'],
  );
});

test('background accepts headers-only media after the current document claims its page', async () => {
  const harness = createHarness({ tabUrls: { 21: 'https://example.com/current?page=1#video' } });
  await harness.dispatch({
    type: 'CLAIM_DOCUMENT',
    pageUrl: 'https://example.com/current?page=1#video',
  }, { tab: { id: 21 }, frameId: 0, documentId: 'current-document' });
  harness.hooks.headers.listeners[0]({
    tabId: 21,
    frameId: 0,
    documentId: 'current-document',
    type: 'xmlhttprequest',
    url: 'https://cdn.example/stream?id=1',
    responseHeaders: [{ name: 'content-type', value: 'video/mp4' }],
  });
  await harness.flush();

  const result = await harness.dispatch({ type: 'GET_CANDIDATES', tabId: 21 });
  assert.equal(result.candidates.length, 1);
  assert.equal(result.candidates[0].pageUrl, 'https://example.com/current?page=1');
});

test('background drops network candidates when trusted tab pageUrl is unavailable', async () => {
  const harness = createHarness();
  harness.hooks.headers.listeners[0]({
    tabId: 22,
    frameId: 0,
    documentId: 'current-document',
    type: 'xmlhttprequest',
    url: 'https://cdn.example/stream?id=2',
    responseHeaders: [{ name: 'content-type', value: 'video/mp4' }],
  });
  await harness.flush();

  const result = await harness.dispatch({ type: 'GET_CANDIDATES', tabId: 22 });
  assert.equal(result.candidates.length, 0);
});

test('late tabs.get result cannot overwrite pageUrl after navigation generation changes', async () => {
  const harness = createHarness({ deferTabGet: true });
  harness.hooks.before.listeners[0]({
    tabId: 31,
    frameId: 0,
    documentId: 'old-document',
    requestId: 'old-media-31',
    type: 'media',
    url: 'https://cdn.example/old.mp4',
  });
  await harness.flush();
  harness.hooks.before.listeners[0]({
    tabId: 31,
    frameId: 0,
    documentId: 'old-document',
    requestId: 'navigation-31',
    type: 'main_frame',
    url: 'https://example.com/new',
    timeStamp: 12_000,
  });
  harness.resolveTabGet(31, 'https://example.com/old');
  await harness.flush();

  const claim = await harness.dispatch({
    type: 'CLAIM_DOCUMENT',
    pageUrl: 'https://example.com/new',
  }, { tab: { id: 31 }, frameId: 0, documentId: 'new-document' });
  assert.equal(claim.ok, true);
  assert.equal((await harness.dispatch({ type: 'GET_CANDIDATES', tabId: 31 })).candidates.length, 0);
});

test('authoritative response MIME replaces conflicting suffix inference in both directions', async () => {
  const harness = createHarness({
    tabUrls: {
      41: 'https://example.com/one',
      42: 'https://example.com/two',
    },
  });
  const cases = [
    { tabId: 41, url: 'https://cdn.example/stream.mp4', mime: 'application/vnd.apple.mpegurl', kind: 'hls' },
    { tabId: 42, url: 'https://cdn.example/stream.m3u8', mime: 'video/mp4', kind: 'mp4' },
  ];
  for (const item of cases) {
    const pageUrl = `https://example.com/${item.tabId === 41 ? 'one' : 'two'}`;
    const details = {
      tabId: item.tabId,
      frameId: 0,
      documentId: `document-${item.tabId}`,
      type: 'xmlhttprequest',
      url: item.url,
    };
    harness.hooks.before.listeners[0](details);
    await harness.flush();
    harness.hooks.headers.listeners[0]({
      ...details,
      responseHeaders: [{ name: 'content-type', value: item.mime }],
    });
    await harness.flush();
    await harness.dispatch({
      type: 'ADD_CANDIDATES',
      pageUrl,
      candidates: [{
        url: item.url,
        pageUrl,
        source: 'performance',
      }],
    }, { tab: { id: item.tabId }, frameId: 0, documentId: `document-${item.tabId}` });
    const result = await harness.dispatch({ type: 'GET_CANDIDATES', tabId: item.tabId });
    assert.equal(result.candidates.length, 1);
    assert.equal(result.candidates[0].kind, item.kind);
  }
});

test('SPA URL update clears candidates and keeps the current document eligible', async () => {
  const harness = createHarness();
  const sender = { tab: { id: 51 }, frameId: 0, documentId: 'spa-document' };
  await harness.dispatch({
    type: 'ADD_CANDIDATES',
    pageUrl: 'https://example.com/old-route',
    candidates: [{ url: 'https://cdn.example/old.mp4', pageUrl: 'https://example.com/old-route' }],
  }, sender);
  harness.hooks.updated.listeners[0](
    51,
    { url: 'https://example.com/new-route' },
    { id: 51, url: 'https://example.com/new-route' },
  );
  await harness.flush();
  assert.equal((await harness.dispatch({ type: 'GET_CANDIDATES', tabId: 51 })).candidates.length, 0);

  const result = await harness.dispatch({
    type: 'ADD_CANDIDATES',
    pageUrl: 'https://example.com/new-route',
    candidates: [{ url: 'https://cdn.example/new.mp4', pageUrl: 'https://example.com/new-route' }],
  }, sender);
  assert.equal(result.ok, true);
  assert.equal(result.candidates[0].pageUrl, 'https://example.com/new-route');
});

test('storage.session quota failure disables repeated persistence attempts but keeps memory candidates', async () => {
  const harness = createHarness({
    storageSetFails: true,
    tabUrls: { 61: 'https://example.com/watch' },
  });
  await harness.dispatch({
    type: 'CLAIM_DOCUMENT',
    pageUrl: 'https://example.com/watch',
  }, { tab: { id: 61 }, frameId: 0, documentId: 'quota-document' });
  for (const name of ['one', 'two']) {
    harness.hooks.headers.listeners[0]({
      tabId: 61,
      frameId: 0,
      documentId: 'quota-document',
      type: 'media',
      url: `https://cdn.example/${name}.mp4`,
      responseHeaders: [{ name: 'content-type', value: 'video/mp4' }],
    });
    await harness.flush();
  }

  assert.equal(harness.storageSetCalls(), 1);
  assert.equal(harness.storageRemoveCalls(), 1);
  assert.equal((await harness.dispatch({ type: 'GET_CANDIDATES', tabId: 61 })).candidates.length, 2);
});

test('MIME replacement cannot re-add an old response when navigation occurs during persistence', async () => {
  const harness = createHarness({
    deferStorageSet: true,
    tabUrls: { 71: 'https://example.com/old' },
  });
  const request = {
    tabId: 71,
    frameId: 0,
    documentId: 'old-document',
    requestId: 'media-71',
    type: 'xmlhttprequest',
    url: 'https://cdn.example/stream.mp4',
  };
  harness.hooks.before.listeners[0](request);
  await harness.flush();
  harness.resolveStorageSet();
  await harness.flush();

  harness.hooks.headers.listeners[0]({
    ...request,
    responseHeaders: [{ name: 'content-type', value: 'application/vnd.apple.mpegurl' }],
  });
  await harness.flush();
  assert.equal(harness.pendingStorageSetCount(), 1);
  harness.hooks.before.listeners[0]({
    tabId: 71,
    frameId: 0,
    documentId: 'old-document',
    requestId: 'navigation-71',
    type: 'main_frame',
    url: 'https://example.com/new',
    timeStamp: 20_000,
  });
  harness.resolveStorageSet();
  await harness.flush();
  while (harness.pendingStorageSetCount()) {
    harness.resolveStorageSet();
    await harness.flush();
  }

  const claim = await harness.dispatch({
    type: 'CLAIM_DOCUMENT',
    pageUrl: 'https://example.com/new',
  }, { tab: { id: 71 }, frameId: 0, documentId: 'new-document' });
  assert.equal(claim.ok, true);
  assert.equal((await harness.dispatch({ type: 'GET_CANDIDATES', tabId: 71 })).candidates.length, 0);
});

test('extensionless response started before SPA navigation cannot attach to the new route', async () => {
  const harness = createHarness({ tabUrls: { 72: 'https://example.com/old-route' } });
  const request = {
    tabId: 72,
    frameId: 0,
    documentId: 'spa-document',
    requestId: 'media-72',
    type: 'xmlhttprequest',
    url: 'https://cdn.example/extensionless?id=72',
  };
  harness.hooks.before.listeners[0](request);
  await harness.flush();
  harness.hooks.updated.listeners[0](
    72,
    { url: 'https://example.com/new-route' },
    { id: 72, url: 'https://example.com/new-route' },
  );
  await harness.flush();
  harness.hooks.headers.listeners[0]({
    ...request,
    responseHeaders: [{ name: 'content-type', value: 'video/mp4' }],
  });
  await harness.flush();

  assert.equal((await harness.dispatch({ type: 'GET_CANDIDATES', tabId: 72 })).candidates.length, 0);
});

test('hydrate rebuilds authoritative MIME state before accepting content candidates', async () => {
  const pageUrl = 'https://example.com/restored';
  const harness = createHarness({
    initialStorageCandidates: {
      73: [{
        url: 'https://cdn.example/stream.mp4',
        pageUrl,
        contentType: 'application/vnd.apple.mpegurl',
        source: 'webRequest',
      }],
    },
  });
  const result = await harness.dispatch({
    type: 'ADD_CANDIDATES',
    pageUrl,
    candidates: [{
      url: 'https://cdn.example/stream.mp4',
      pageUrl,
      source: 'performance',
    }],
  }, { tab: { id: 73 }, frameId: 0, documentId: 'restored-document' });

  assert.equal(result.candidates.length, 1);
  assert.equal(result.candidates[0].kind, 'hls');
});

test('headers HLS result invalidates a slower before suffix inference for the same request', async () => {
  const harness = createHarness({ deferTabGet: true });
  const request = {
    tabId: 81,
    frameId: 0,
    documentId: 'document-81',
    requestId: 'request-81',
    type: 'xmlhttprequest',
    url: 'https://cdn.example/stream.mp4',
  };
  harness.hooks.before.listeners[0](request);
  await harness.flush();
  harness.hooks.headers.listeners[0]({
    ...request,
    responseHeaders: [{ name: 'content-type', value: 'application/vnd.apple.mpegurl' }],
  });
  await harness.flush();
  assert.equal(harness.pendingTabGetCount(), 2);

  harness.resolveTabGetAt(1, 'https://example.com/watch');
  await harness.flush();
  harness.resolveTabGetAt(0, 'https://example.com/watch');
  await harness.flush();

  const result = await harness.dispatch({ type: 'GET_CANDIDATES', tabId: 81 });
  assert.equal(result.candidates.length, 1);
  assert.equal(result.candidates[0].kind, 'hls');
});

test('headers non-media result invalidates a slower before suffix inference for the same request', async () => {
  const harness = createHarness({ deferTabGet: true });
  const request = {
    tabId: 82,
    frameId: 0,
    documentId: 'document-82',
    requestId: 'request-82',
    type: 'xmlhttprequest',
    url: 'https://cdn.example/error.mp4',
  };
  harness.hooks.before.listeners[0](request);
  await harness.flush();
  harness.hooks.headers.listeners[0]({
    ...request,
    responseHeaders: [{ name: 'content-type', value: 'text/html' }],
  });
  await harness.flush();
  assert.equal(harness.pendingTabGetCount(), 2);

  harness.resolveTabGetAt(1, 'https://example.com/watch');
  await harness.flush();
  harness.resolveTabGetAt(0, 'https://example.com/watch');
  await harness.flush();

  assert.equal((await harness.dispatch({ type: 'GET_CANDIDATES', tabId: 82 })).candidates.length, 0);
});
