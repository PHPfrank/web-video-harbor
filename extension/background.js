'use strict';

importScripts('lib/media.js');

const media = globalThis.VideoGrabberMedia;
const candidateStore = media.createCandidateStore({ maxTabs: 50, maxCandidatesPerTab: 100 });
const STORAGE_KEY = 'videoGrabberCandidatesV1';
const navigationStarts = new Map();
const documentStates = new Map();
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

function resetDocumentState(tabId, topDocumentId) {
  const previous = documentStates.get(tabId);
  const blocked = new Set(previous ? [...previous.blocked, ...previous.current] : []);
  const current = new Set();
  let top = null;
  if (validDocumentId(topDocumentId)) {
    blocked.delete(topDocumentId);
    current.add(topDocumentId);
    top = topDocumentId;
  }
  documentStates.set(tabId, { top, current, blocked: boundedDocumentSet(blocked) });
}

function belongsToCurrentDocument(details) {
  const documentId = details && details.documentId;
  if (!validDocumentId(documentId)) return true;
  let state = documentStates.get(details.tabId);
  if (!state) {
    state = { top: null, current: new Set(), blocked: new Set() };
    documentStates.set(details.tabId, state);
  }
  if (state.blocked.has(documentId)) return false;
  if (details.frameId === 0) {
    if (state.top && state.top !== documentId) return false;
    state.top = documentId;
  }
  state.current.add(documentId);
  state.current = boundedDocumentSet(state.current);
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

function recordRequest(details, contentType) {
  if (!details || !validTabId(details.tabId) || !belongsToCurrentDocument(details)) return;
  const candidate = media.normalizeCandidate({
    url: details.url,
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

function beginNavigation(tabId, timestamp, documentId) {
  if (!validTabId(tabId)) return;
  const eventTime = Number.isFinite(timestamp) ? timestamp : Date.now();
  const previous = navigationStarts.get(tabId);
  const hasNewDocument = validDocumentId(documentId)
    && (!previous || previous.documentId !== documentId);
  if (previous && eventTime <= previous.startedAt && !hasNewDocument) return;
  navigationStarts.set(tabId, {
    startedAt: Math.max(eventTime, previous ? previous.startedAt : eventTime),
    documentId: validDocumentId(documentId) ? documentId : null,
  });
  resetDocumentState(tabId, documentId);
  void clearTab(tabId);
}

chrome.webRequest.onBeforeRequest.addListener(
  function onBeforeRequest(details) {
    if (details.type === 'main_frame') {
      beginNavigation(details.tabId, details.timeStamp, details.documentId);
      return;
    }
    recordRequest(details, '');
  },
  { urls: ['<all_urls>'], types: ['main_frame', 'media', 'xmlhttprequest'] },
);

chrome.webRequest.onHeadersReceived.addListener(
  function onHeadersReceived(details) {
    recordRequest(details, responseContentType(details.responseHeaders));
  },
  { urls: ['<all_urls>'], types: ['media', 'xmlhttprequest'] },
  ['responseHeaders'],
);

chrome.tabs.onUpdated.addListener(function onTabUpdated(tabId, changeInfo) {
  if (!changeInfo) return;
  if (changeInfo.status === 'complete') {
    navigationStarts.delete(tabId);
    return;
  }
  if (changeInfo.status !== 'loading') return;
  const navigation = navigationStarts.get(tabId);
  if (navigation) return;
  beginNavigation(tabId, Date.now(), null);
});

chrome.tabs.onRemoved.addListener(function onTabRemoved(tabId) {
  navigationStarts.delete(tabId);
  documentStates.delete(tabId);
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
    if (!belongsToCurrentDocument({
      tabId,
      frameId: sender.frameId,
      documentId: sender.documentId,
    })) {
      return { ok: false, error: '页面已导航' };
    }
    const candidates = await addCandidates(tabId, request.candidates);
    return { ok: true, candidates };
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
