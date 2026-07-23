'use strict';

importScripts('lib/media.js');

const media = globalThis.VideoGrabberMedia;
const candidateStore = media.createCandidateStore({ maxTabs: 50, maxCandidatesPerTab: 100 });
const STORAGE_KEY = 'videoGrabberCandidatesV1';
const navigationStarts = new Map();
const documentStates = new Map();
const navigationInProgress = new Set();
const tabPageUrls = new Map();
const authoritativeKinds = new Map();
const requestContexts = new Map();
let persistenceQueue = Promise.resolve();
let sessionPersistenceEnabled = true;
let requestEpoch = 0;

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

function rememberAuthoritativeKind(tabId, url, kind) {
  let kinds = authoritativeKinds.get(tabId);
  if (!kinds) {
    kinds = new Map();
    authoritativeKinds.set(tabId, kinds);
  }
  kinds.delete(url);
  kinds.set(url, kind);
  while (kinds.size > 100) kinds.delete(kinds.keys().next().value);
}

function explicitMediaMime(contentType) {
  const mime = typeof contentType === 'string' ? contentType.split(';', 1)[0].trim().toLowerCase() : '';
  return mime === 'video/mp4'
    || mime === 'application/vnd.apple.mpegurl'
    || mime === 'application/x-mpegurl'
    || mime === 'audio/mpegurl';
}

function rememberRequestContext(details) {
  if (!details || !validTabId(details.tabId) || !validDocumentId(details.documentId)
    || typeof details.requestId !== 'string' || !details.requestId) return null;
  const state = documentState(details.tabId);
  if (state.blocked.has(details.documentId)
    || (details.frameId === 0 && state.top && state.top !== details.documentId)) return null;
  const epoch = ++requestEpoch;
  requestContexts.delete(details.requestId);
  requestContexts.set(details.requestId, {
    tabId: details.tabId,
    generation: state.generation,
    documentId: details.documentId,
    phase: 'before',
    epoch,
  });
  while (requestContexts.size > 1000) {
    requestContexts.delete(requestContexts.keys().next().value);
  }
  return { requestId: details.requestId, epoch, phase: 'before' };
}

function beginHeadersContext(details) {
  if (!details || typeof details.requestId !== 'string' || !details.requestId) {
    return { accepted: true, token: null };
  }
  const state = documentState(details.tabId);
  let context = requestContexts.get(details.requestId);
  const matches = !context || (context.tabId === details.tabId
    && context.generation === state.generation
    && context.documentId === details.documentId);
  const epoch = ++requestEpoch;
  if (!context) {
    context = {
      tabId: details.tabId,
      generation: state.generation,
      documentId: details.documentId,
    };
    requestContexts.set(details.requestId, context);
  }
  context.phase = 'headers';
  context.epoch = epoch;
  return {
    accepted: matches,
    token: { requestId: details.requestId, epoch, phase: 'headers' },
  };
}

function requestTokenMatches(token) {
  if (!token) return true;
  const context = requestContexts.get(token.requestId);
  if (!context) return false;
  return context.epoch === token.epoch
    && context.phase === token.phase
    && documentState(context.tabId).generation === context.generation;
}

function forgetRequestToken(token) {
  if (token && requestTokenMatches(token)) requestContexts.delete(token.requestId);
}

function forgetRequestContext(details) {
  if (details && typeof details.requestId === 'string') requestContexts.delete(details.requestId);
}

function forgetTabRequestContexts(tabId) {
  for (const [requestId, context] of requestContexts) {
    if (context.tabId === tabId) requestContexts.delete(requestId);
  }
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

function belongsToCurrentRequest(details) {
  if (!details || !validTabId(details.tabId)) return false;
  const state = documentState(details.tabId);
  const documentId = details.documentId;
  if (!validDocumentId(documentId)) return false;
  if (state.blocked.has(documentId)) return false;
  if (details.frameId === 0 && state.top && state.top !== documentId) return false;
  state.current.add(documentId);
  state.current = boundedDocumentSet(state.current);
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
      const restored = candidateStore.replace(tabId, candidates);
      for (const candidate of restored) {
        if (explicitMediaMime(candidate.contentType)) {
          rememberAuthoritativeKind(tabId, candidate.url, candidate.kind);
        }
      }
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

function writeSessionSnapshot(session, snapshot) {
  return new Promise((resolve) => {
    let settled = false;
    function finish(succeeded) {
      if (settled) return;
      settled = true;
      resolve(succeeded);
    }
    try {
      const returned = session.set(snapshot, function onStored() {
        finish(!chrome.runtime.lastError);
      });
      if (returned && typeof returned.then === 'function') {
        returned.then(function stored() { finish(true); }, function storageFailed() { finish(false); });
      }
    } catch (_error) {
      finish(false);
    }
  });
}

function persistStore() {
  const session = storageSession();
  if (!session || !sessionPersistenceEnabled) return Promise.resolve();
  persistenceQueue = persistenceQueue.then(async function writeLatestSnapshot() {
    if (!sessionPersistenceEnabled) return;
    const succeeded = await writeSessionSnapshot(session, persistedSnapshot());
    if (!succeeded) {
      sessionPersistenceEnabled = false;
      await invokeChromeMethod(session, 'remove', STORAGE_KEY);
    }
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

async function commitNetworkMutation(tabId, generation, details, token, mutation) {
  await ready;
  if (documentState(tabId).generation !== generation || !belongsToCurrentRequest(details)
    || !requestTokenMatches(token)) return false;
  mutation();
  await persistStore();
  return true;
}

function responseContentType(headers) {
  if (!Array.isArray(headers)) return '';
  const header = headers.find((item) => item && typeof item.name === 'string'
    && item.name.toLowerCase() === 'content-type');
  return header && typeof header.value === 'string' ? header.value : '';
}

async function trustedPageUrl(tabId) {
  const cached = tabPageUrls.get(tabId);
  if (cached) return cached;
  if (!chrome.tabs || typeof chrome.tabs.get !== 'function') return null;
  const tab = await invokeChromeMethod(chrome.tabs, 'get', tabId);
  const pageUrl = media.normalizeUrl(tab && tab.url);
  return pageUrl || null;
}

async function recordRequest(details, contentType, token) {
  if (!belongsToCurrentRequest(details) || !requestTokenMatches(token)) return;
  const generation = documentState(details.tabId).generation;
  const pageUrl = await trustedPageUrl(details.tabId);
  if (!pageUrl || documentState(details.tabId).generation !== generation
    || !belongsToCurrentRequest(details) || !requestTokenMatches(token)) return;
  tabPageUrls.set(details.tabId, pageUrl);
  const candidate = media.normalizeCandidate({
    url: details.url,
    pageUrl,
    contentType,
    source: 'webRequest',
  });
  if (candidate) {
    const authoritative = explicitMediaMime(contentType);
    const suffixKind = media.inferMediaKind(details.url, '');
    const replace = contentType && suffixKind !== 'unknown' && suffixKind !== candidate.kind;
    await commitNetworkMutation(details.tabId, generation, details, token, function commitCandidate() {
      if (authoritative) rememberAuthoritativeKind(details.tabId, candidate.url, candidate.kind);
      if (replace) candidateStore.replaceUrl(details.tabId, candidate);
      else candidateStore.add(details.tabId, [candidate]);
    });
    return;
  }
  const mime = typeof contentType === 'string' ? contentType.split(';', 1)[0].trim().toLowerCase() : '';
  const hadSuffixCandidate = media.inferMediaKind(details.url, '') !== 'unknown';
  if (mime && mime !== 'application/octet-stream' && hadSuffixCandidate) {
    await commitNetworkMutation(details.tabId, generation, details, token, function rejectCandidate() {
      candidateStore.removeUrl(details.tabId, details.url);
    });
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
  authoritativeKinds.delete(tabId);
  resetDocumentState(tabId, details.documentId, generation);
  void clearTab(tabId);
}

chrome.webRequest.onBeforeRequest.addListener(
  function onBeforeRequest(details) {
    if (details.type === 'main_frame') {
      beginNavigation(details);
      return;
    }
    const token = rememberRequestContext(details);
    void recordRequest(details, '', token);
  },
  { urls: ['<all_urls>'], types: ['main_frame', 'media', 'xmlhttprequest'] },
);

chrome.webRequest.onHeadersReceived.addListener(
  function onHeadersReceived(details) {
    const headers = beginHeadersContext(details);
    if (!headers.accepted) {
      forgetRequestToken(headers.token);
      return;
    }
    void recordRequest(details, responseContentType(details.responseHeaders), headers.token)
      .finally(function headersFinished() { forgetRequestToken(headers.token); });
  },
  { urls: ['<all_urls>'], types: ['media', 'xmlhttprequest'] },
  ['responseHeaders'],
);

chrome.webRequest.onCompleted.addListener(
  forgetRequestContext,
  { urls: ['<all_urls>'], types: ['media', 'xmlhttprequest'] },
);

chrome.webRequest.onErrorOccurred.addListener(
  forgetRequestContext,
  { urls: ['<all_urls>'], types: ['media', 'xmlhttprequest'] },
);

chrome.tabs.onUpdated.addListener(function onTabUpdated(tabId, changeInfo, tab) {
  if (!changeInfo) return;
  if (!changeInfo.status && typeof changeInfo.url === 'string') {
    const pageUrl = media.normalizeUrl(changeInfo.url);
    if (pageUrl && tabPageUrls.get(tabId) !== pageUrl) {
      tabPageUrls.set(tabId, pageUrl);
      authoritativeKinds.delete(tabId);
      const state = documentState(tabId);
      state.generation += 1;
      navigationStarts.set(tabId, {
        startedAt: Date.now(),
        generation: state.generation,
        requestId: null,
      });
      void clearTab(tabId);
    }
    return;
  }
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
  authoritativeKinds.delete(tabId);
  forgetTabRequestContexts(tabId);
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
    await ready;
    if (!claimDocument(tabId, sender, request.pageUrl)) {
      return { ok: false, error: '页面已导航' };
    }
    const pageUrl = tabPageUrls.get(tabId);
    const knownKinds = authoritativeKinds.get(tabId);
    const submitted = request.candidates.map((candidate) => ({
      ...(candidate && typeof candidate === 'object' && !Array.isArray(candidate) ? candidate : {}),
      pageUrl,
    })).filter((candidate) => {
      if (!knownKinds) return true;
      const normalized = media.normalizeCandidate(candidate);
      return !normalized || !knownKinds.has(normalized.url)
        || knownKinds.get(normalized.url) === normalized.kind;
    });
    const candidates = await addCandidates(tabId, submitted);
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
