'use strict';

importScripts('lib/media.js');

const media = globalThis.VideoGrabberMedia;
const candidateStore = media.createCandidateStore({ maxTabs: 50, maxCandidatesPerTab: 100 });
const STORAGE_KEY = 'videoGrabberCandidatesV1';
const navigationStarts = new Map();
const documentStates = new Map();
const navigationInProgress = new Set();
const tabPageUrls = new Map();
const requestGenerations = new Map();
let persistenceQueue = Promise.resolve();

function validTabId(tabId) {
  return Number.isInteger(tabId) && tabId >= 0;
}

function validDocumentId(documentId) {
  return typeof documentId === 'string' && documentId.length > 0 && documentId.length <= 128;
}

function boundedDocumentSet(values) {
  const entries = Array.from(values);
  return new Set(entries.slice(Math.max(0, entries.length - 100)));
}

function resetDocumentState(tabId, staleDocumentId, generation) {
  const previous = documentStates.get(tabId);
  const blocked = new Set(previous ? [...previous.blocked, ...previous.current] : []);
  if (validDocumentId(staleDocumentId)) blocked.add(staleDocumentId);
  documentStates.set(tabId, {
    top: null,
    current: new Set(),
    blocked: boundedDocumentSet(blocked),
    generation,
    isolating: true,
  });
}

function documentState(tabId) {
  let state = documentStates.get(tabId);
  if (!state) {
    const navigation = navigationStarts.get(tabId);
    state = {
      top: null,
      current: new Set(),
      blocked: new Set(),
      generation: navigation ? navigation.generation : 0,
      isolating: false,
    };
    documentStates.set(tabId, state);
  }
  return state;
}

function rememberRequest(details, generation) {
  if (!details || typeof details.requestId !== 'string' || !details.requestId) return false;
  requestGenerations.delete(details.requestId);
  requestGenerations.set(details.requestId, { tabId: details.tabId, generation });
  while (requestGenerations.size > 1000) {
    requestGenerations.delete(requestGenerations.keys().next().value);
  }
  return true;
}

function belongsToCurrentRequest(details, phase) {
  if (!details || !validTabId(details.tabId)) return false;
  const state = documentState(details.tabId);
  if (phase === 'headers' && typeof details.requestId === 'string') {
    const request = requestGenerations.get(details.requestId);
    if (request && (request.tabId !== details.tabId || request.generation !== state.generation)) {
      return false;
    }
  }
  const documentId = details.documentId;
  if (!validDocumentId(documentId)) {
    if (phase === 'before') {
      const navigation = navigationStarts.get(details.tabId);
      if (state.isolating && (!Number.isFinite(details.timeStamp)
        || !navigation || details.timeStamp < navigation.startedAt)) {
        return false;
      }
      const remembered = rememberRequest(details, state.generation);
      return remembered || !state.isolating;
    }
    const request = typeof details.requestId === 'string'
      ? requestGenerations.get(details.requestId)
      : null;
    return request
      ? request.tabId === details.tabId && request.generation === state.generation
      : !state.isolating;
  }
  if (state.blocked.has(documentId)) return false;
  if (details.frameId === 0 && state.top && state.top !== documentId) return false;
  state.current.add(documentId);
  state.current = boundedDocumentSet(state.current);
  if (phase === 'before') rememberRequest(details, state.generation);
  return true;
}

function claimDocument(tabId, sender, pageUrlValue) {
  const pageUrl = media.normalizeUrl(pageUrlValue);
  if (!validTabId(tabId) || !pageUrl) return false;
  const expectedPageUrl = tabPageUrls.get(tabId);
  if (expectedPageUrl && expectedPageUrl !== pageUrl) return false;
  const state = documentState(tabId);
  const documentId = sender && sender.documentId;
  if (validDocumentId(documentId)) {
    if (state.blocked.has(documentId)) return false;
    if (state.top && state.top !== documentId) return false;
    state.top = documentId;
    state.current.add(documentId);
    state.current = boundedDocumentSet(state.current);
  } else if (state.isolating && expectedPageUrl !== pageUrl) {
    return false;
  }
  state.isolating = false;
  tabPageUrls.set(tabId, pageUrl);
  return true;
}

function storageSession() {
  return chrome.storage && chrome.storage.session ? chrome.storage.session : null;
}

function invokeChromeMethod(target, method, argument) {
  return new Promise((resolve) => {
    let settled = false;
    function finish(value) {
      if (settled) return;
      settled = true;
      resolve(value);
    }
    try {
      const returned = target[method](argument, function onResult(value) {
        void chrome.runtime.lastError;
        finish(value);
      });
      if (returned && typeof returned.then === 'function') {
        returned.then(finish, function ignoreStorageError() { finish(undefined); });
      }
    } catch (_error) {
      finish(undefined);
    }
  });
}

async function hydrateStore() {
  const session = storageSession();
  if (!session) return;
  const result = await invokeChromeMethod(session, 'get', STORAGE_KEY);
  const saved = result && result[STORAGE_KEY];
  if (!saved || typeof saved !== 'object' || Array.isArray(saved)) return;
  for (const [rawTabId, candidates] of Object.entries(saved)) {
    const tabId = Number(rawTabId);
    if (validTabId(tabId) && Array.isArray(candidates)) {
      candidateStore.replace(tabId, candidates);
    }
  }
}

const ready = hydrateStore();

function persistedSnapshot() {
  const tabs = {};
  for (const [tabId, candidates] of candidateStore.entries()) {
    if (candidates.length) tabs[String(tabId)] = candidates;
  }
  return { [STORAGE_KEY]: tabs };
}

function persistStore() {
  const session = storageSession();
  if (!session) return Promise.resolve();
  persistenceQueue = persistenceQueue.then(function writeLatestSnapshot() {
    return invokeChromeMethod(session, 'set', persistedSnapshot());
  });
  return persistenceQueue;
}

async function addCandidates(tabId, candidates) {
  if (!validTabId(tabId) || !Array.isArray(candidates)) return [];
  await ready;
  const stored = candidateStore.add(tabId, candidates.slice(0, 100));
  await persistStore();
  return stored;
}

async function clearTab(tabId) {
  if (!validTabId(tabId)) return;
  await ready;
  candidateStore.clear(tabId);
  await persistStore();
}

async function removeCandidateUrl(tabId, url) {
  if (!validTabId(tabId)) return;
  await ready;
  candidateStore.removeUrl(tabId, url);
  await persistStore();
}

function responseContentType(headers) {
  if (!Array.isArray(headers)) return '';
  const header = headers.find((item) => item && typeof item.name === 'string'
    && item.name.toLowerCase() === 'content-type');
  return header && typeof header.value === 'string' ? header.value : '';
}

function recordRequest(details, contentType, phase) {
  if (!belongsToCurrentRequest(details, phase)) return;
  const candidate = media.normalizeCandidate({
    url: details.url,
    pageUrl: tabPageUrls.get(details.tabId),
    contentType,
    source: 'webRequest',
  });
  if (candidate) {
    void addCandidates(details.tabId, [candidate]);
    return;
  }
  const mime = typeof contentType === 'string' ? contentType.split(';', 1)[0].trim().toLowerCase() : '';
  const hadSuffixCandidate = media.inferMediaKind(details.url, '') !== 'unknown';
  if (mime && mime !== 'application/octet-stream' && hadSuffixCandidate) {
    void removeCandidateUrl(details.tabId, details.url);
  }
}

function beginNavigation(details) {
  const tabId = details && details.tabId;
  if (!validTabId(tabId)) return;
  const eventTime = Number.isFinite(details.timeStamp) ? details.timeStamp : Date.now();
  const previous = navigationStarts.get(tabId);
  if (previous && details.requestId && previous.requestId === details.requestId) {
    const redirectedPageUrl = media.normalizeUrl(details.url);
    if (redirectedPageUrl) tabPageUrls.set(tabId, redirectedPageUrl);
    return;
  }
  const generation = previous ? previous.generation + 1 : 1;
  navigationStarts.set(tabId, {
    startedAt: eventTime,
    generation,
    requestId: typeof details.requestId === 'string' ? details.requestId : null,
  });
  navigationInProgress.add(tabId);
  const pageUrl = media.normalizeUrl(details.url);
  if (pageUrl) tabPageUrls.set(tabId, pageUrl);
  else tabPageUrls.delete(tabId);
  resetDocumentState(tabId, details.documentId, generation);
  void clearTab(tabId);
}

chrome.webRequest.onBeforeRequest.addListener(
  function onBeforeRequest(details) {
    if (details.type === 'main_frame') {
      beginNavigation(details);
      return;
    }
    recordRequest(details, '', 'before');
  },
  { urls: ['<all_urls>'], types: ['main_frame', 'media', 'xmlhttprequest'] },
);

chrome.webRequest.onHeadersReceived.addListener(
  function onHeadersReceived(details) {
    recordRequest(details, responseContentType(details.responseHeaders), 'headers');
  },
  { urls: ['<all_urls>'], types: ['media', 'xmlhttprequest'] },
  ['responseHeaders'],
);

chrome.tabs.onUpdated.addListener(function onTabUpdated(tabId, changeInfo, tab) {
  if (!changeInfo) return;
  if (changeInfo.status === 'complete') {
    navigationInProgress.delete(tabId);
    const pageUrl = media.normalizeUrl(tab && tab.url);
    if (pageUrl) tabPageUrls.set(tabId, pageUrl);
    return;
  }
  if (changeInfo.status !== 'loading') return;
  if (navigationInProgress.has(tabId)) return;
  beginNavigation({
    tabId,
    timeStamp: Date.now(),
    url: changeInfo.url || (tab && tab.url) || tabPageUrls.get(tabId),
  });
});

chrome.tabs.onRemoved.addListener(function onTabRemoved(tabId) {
  navigationStarts.delete(tabId);
  navigationInProgress.delete(tabId);
  documentStates.delete(tabId);
  tabPageUrls.delete(tabId);
  void clearTab(tabId);
});

function requestedTabId(request, sender) {
  if (request && validTabId(request.tabId)) return request.tabId;
  return sender && sender.tab && validTabId(sender.tab.id) ? sender.tab.id : null;
}

function sendMessageToTab(tabId, message) {
  return new Promise((resolve) => {
    try {
      chrome.tabs.sendMessage(tabId, message, function onResponse(response) {
        const error = chrome.runtime.lastError;
        if (error) resolve({ ok: false, error: '页面扫描器不可用' });
        else resolve(response || { ok: true });
      });
    } catch (_error) {
      resolve({ ok: false, error: '页面扫描器不可用' });
    }
  });
}

async function handleMessage(request, sender) {
  if (!request || typeof request !== 'object' || Array.isArray(request)) {
    return { ok: false, error: '无效请求' };
  }
  if (request.type === 'ADD_CANDIDATES') {
    const tabId = sender && sender.tab ? sender.tab.id : null;
    if (!validTabId(tabId) || !Array.isArray(request.candidates)) {
      return { ok: false, error: '无效媒体列表' };
    }
    if (!claimDocument(tabId, sender, request.pageUrl)) {
      return { ok: false, error: '页面已导航' };
    }
    const pageUrl = tabPageUrls.get(tabId);
    const candidates = await addCandidates(tabId, request.candidates.map((candidate) => ({
      ...(candidate && typeof candidate === 'object' && !Array.isArray(candidate) ? candidate : {}),
      pageUrl,
    })));
    return { ok: true, candidates };
  }
  if (request.type === 'CLAIM_DOCUMENT') {
    const tabId = sender && sender.tab ? sender.tab.id : null;
    if (!claimDocument(tabId, sender, request.pageUrl)) {
      return { ok: false, error: '页面已导航' };
    }
    return { ok: true };
  }
  if (request.type === 'GET_CANDIDATES') {
    const tabId = requestedTabId(request, sender);
    if (!validTabId(tabId)) return { ok: false, error: '无效标签页' };
    await ready;
    return { ok: true, candidates: candidateStore.get(tabId) };
  }
  if (request.type === 'CLEAR') {
    const tabId = requestedTabId(request, sender);
    if (!validTabId(tabId)) return { ok: false, error: '无效标签页' };
    await clearTab(tabId);
    return { ok: true, candidates: [] };
  }
  if (request.type === 'RESCAN') {
    const tabId = requestedTabId(request, sender);
    if (!validTabId(tabId)) return { ok: false, error: '无效标签页' };
    return sendMessageToTab(tabId, { type: 'RESCAN' });
  }
  return { ok: false, error: '未知请求' };
}

chrome.runtime.onMessage.addListener(function onRuntimeMessage(request, sender, sendResponse) {
  void handleMessage(request, sender).then(sendResponse, function onFailure() {
    sendResponse({ ok: false, error: '后台处理失败' });
  });
  return true;
});
