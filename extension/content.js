'use strict';

(function startMediaDiscovery() {
  const media = globalThis.VideoGrabberMedia;
  if (!media || typeof chrome === 'undefined' || !chrome.runtime) return;

  const MAX_PAGE_CANDIDATES = 100;
  const RESCAN_DELAY_MS = 300;
  let rescanTimer = null;
  let lastFingerprint = '';

  function pageTitle() {
    return typeof document.title === 'string' ? document.title : '';
  }

  function domCandidates() {
    const candidates = [];
    for (const video of document.querySelectorAll('video')) {
      const details = {
        baseUrl: location.href,
        pageUrl: location.href,
        title: pageTitle(),
        source: 'dom',
        width: video.videoWidth || undefined,
        height: video.videoHeight || undefined,
      };
      for (const url of [video.currentSrc, video.src]) {
        const candidate = media.normalizeCandidate({ ...details, url });
        if (candidate) {
          candidates.push(candidate);
          if (candidates.length >= MAX_PAGE_CANDIDATES) return candidates;
        }
      }
      for (const source of video.querySelectorAll('source')) {
        const candidate = media.normalizeCandidate({
          ...details,
          url: source.src,
          contentType: source.type,
        });
        if (candidate) {
          candidates.push(candidate);
          if (candidates.length >= MAX_PAGE_CANDIDATES) return candidates;
        }
      }
    }
    return candidates;
  }

  function performanceCandidates() {
    const candidates = [];
    if (!globalThis.performance || typeof performance.getEntriesByType !== 'function') return candidates;
    for (const entry of performance.getEntriesByType('resource')) {
      const candidate = media.normalizeCandidate({
        url: entry && entry.name,
        baseUrl: location.href,
        pageUrl: location.href,
        title: pageTitle(),
        source: 'performance',
      });
      if (candidate) candidates.push(candidate);
      if (candidates.length >= MAX_PAGE_CANDIDATES) break;
    }
    return candidates;
  }

  function collectCandidates() {
    return media.mergeCandidates(domCandidates(), performanceCandidates()).slice(0, MAX_PAGE_CANDIDATES);
  }

  function sendCandidates(candidates) {
    if (!candidates.length) return Promise.resolve({ ok: true, candidates: [] });
    return new Promise((resolve) => {
      try {
        chrome.runtime.sendMessage({
          type: 'ADD_CANDIDATES',
          pageUrl: location.href,
          candidates,
        }, function onCandidatesStored(response) {
          const runtimeError = chrome.runtime.lastError;
          if (runtimeError || !response || response.ok !== true) {
            resolve({ ok: false, error: '后台未能保存扫描结果' });
            return;
          }
          resolve({ ok: true, candidates: Array.isArray(response.candidates) ? response.candidates : candidates });
        });
      } catch (_error) {
        resolve({ ok: false, error: '后台未能保存扫描结果' });
      }
    });
  }

  async function scan(force) {
    const candidates = collectCandidates();
    const fingerprint = JSON.stringify(candidates);
    if (force || fingerprint !== lastFingerprint) {
      const stored = await sendCandidates(candidates);
      if (!stored.ok) return stored;
      lastFingerprint = fingerprint;
      return { ok: true, candidates: stored.candidates };
    }
    return { ok: true, candidates };
  }

  function scheduleScan() {
    if (rescanTimer !== null) clearTimeout(rescanTimer);
    rescanTimer = setTimeout(function runDebouncedScan() {
      rescanTimer = null;
      void scan(false);
    }, RESCAN_DELAY_MS);
  }

  function mutationMayContainMedia(mutation) {
    if (mutation.type === 'attributes') {
      return Boolean(mutation.target
        && typeof mutation.target.matches === 'function'
        && mutation.target.matches('video,source'));
    }
    for (const node of mutation.addedNodes) {
      if (!node || node.nodeType !== 1) continue;
      if ((typeof node.matches === 'function' && node.matches('video,source'))
        || (typeof node.querySelector === 'function' && node.querySelector('video,source'))) {
        return true;
      }
    }
    return false;
  }

  document.addEventListener('loadedmetadata', function onMetadata(event) {
    if (event.target && event.target.nodeName === 'VIDEO') scheduleScan();
  }, true);
  document.addEventListener('play', function onPlay(event) {
    if (event.target && event.target.nodeName === 'VIDEO') scheduleScan();
  }, true);

  chrome.runtime.onMessage.addListener(function onMessage(request, _sender, sendResponse) {
    if (!request || request.type !== 'RESCAN') return false;
    void scan(true).then(sendResponse, function onScanFailure() {
      sendResponse({ ok: false, error: '后台未能保存扫描结果' });
    });
    return true;
  });

  if (document.documentElement && typeof MutationObserver === 'function') {
    const observer = new MutationObserver(function onMutations(mutations) {
      if (mutations.some(mutationMayContainMedia)) scheduleScan();
    });
    observer.observe(document.documentElement, {
      childList: true,
      subtree: true,
      attributes: true,
      attributeFilter: ['src'],
    });
  }

  try {
    chrome.runtime.sendMessage({
      type: 'CLAIM_DOCUMENT',
      pageUrl: location.href,
    }, function ignoreClaimResponse() {
      void chrome.runtime.lastError;
    });
  } catch (_error) {
    // The extension may have been reloaded while this page was open.
  }
  void scan(false);
}());
