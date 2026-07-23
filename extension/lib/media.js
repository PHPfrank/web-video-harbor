(function initVideoGrabberMedia(root, factory) {
  'use strict';

  const api = factory();
  root.VideoGrabberMedia = api;
  if (typeof module === 'object' && module.exports) {
    module.exports = api;
  }
}(typeof globalThis !== 'undefined' ? globalThis : this, function createMediaApi() {
  'use strict';

  const DEFAULT_TITLE = '未命名视频';
  const MAX_TITLE_LENGTH = 120;
  const MAX_URL_LENGTH = 8192;
  const VALID_SOURCES = new Set(['dom', 'performance', 'webRequest']);
  const SOURCE_RANK = { unknown: 0, webRequest: 1, performance: 2, dom: 3 };
  const HLS_MIME_TYPES = new Set([
    'application/vnd.apple.mpegurl',
    'application/x-mpegurl',
    'audio/mpegurl',
  ]);

  function normalizeUrl(value, baseUrl) {
    if (typeof value !== 'string' || value.trim() === '') return null;
    if (value.trim().length > MAX_URL_LENGTH) return null;
    try {
      const url = baseUrl ? new URL(value.trim(), baseUrl) : new URL(value.trim());
      if (url.protocol !== 'http:' && url.protocol !== 'https:') return null;
      if (url.username || url.password) return null;
      url.hash = '';
      if (url.href.length > MAX_URL_LENGTH) return null;
      return url.href;
    } catch (_error) {
      return null;
    }
  }

  function normalizeContentType(value) {
    if (typeof value !== 'string') return '';
    return value.split(';', 1)[0].trim().toLowerCase();
  }

  function inferMediaKind(value, contentType) {
    const urlValue = normalizeUrl(value);
    if (!urlValue) return 'unknown';

    const mime = normalizeContentType(contentType);
    if (mime === 'video/mp4') return 'mp4';
    if (HLS_MIME_TYPES.has(mime)) return 'hls';
    if (mime && mime !== 'application/octet-stream') return 'unknown';

    const pathname = new URL(urlValue).pathname.toLowerCase();
    if (pathname.endsWith('.mp4')) return 'mp4';
    if (pathname.endsWith('.m3u8')) return 'hls';
    return 'unknown';
  }

  function normalizeTitle(value) {
    if (typeof value !== 'string') return DEFAULT_TITLE;
    const title = value.trim();
    return title ? title.slice(0, MAX_TITLE_LENGTH) : DEFAULT_TITLE;
  }

  function positiveDimension(value) {
    const number = Number(value);
    if (!Number.isFinite(number) || number <= 0) return undefined;
    return Math.min(Math.round(number), 32768);
  }

  function normalizeCandidate(input) {
    if (!input || typeof input !== 'object' || Array.isArray(input)) return null;
    const url = normalizeUrl(input.url, input.baseUrl);
    if (!url) return null;

    const contentType = normalizeContentType(input.contentType);
    const kind = inferMediaKind(url, contentType);
    if (kind === 'unknown') return null;

    const candidate = {
      url,
      kind,
      title: normalizeTitle(input.title),
      source: VALID_SOURCES.has(input.source) ? input.source : 'unknown',
    };
    const pageUrl = normalizeUrl(input.pageUrl);
    if (pageUrl) candidate.pageUrl = pageUrl;
    const width = positiveDimension(input.width);
    const height = positiveDimension(input.height);
    if (width !== undefined) candidate.width = width;
    if (height !== undefined) candidate.height = height;
    if (contentType) candidate.contentType = contentType;
    return candidate;
  }

  function isUsefulTitle(title) {
    return title && title !== DEFAULT_TITLE;
  }

  function mergeCandidateMetadata(current, incoming) {
    const merged = { ...current };
    if (!isUsefulTitle(current.title) && isUsefulTitle(incoming.title)) {
      merged.title = incoming.title;
    }
    if ((SOURCE_RANK[incoming.source] || 0) > (SOURCE_RANK[current.source] || 0)) {
      merged.source = incoming.source;
    }
    if (!merged.width && incoming.width) merged.width = incoming.width;
    if (!merged.height && incoming.height) merged.height = incoming.height;
    if (!merged.contentType && incoming.contentType) merged.contentType = incoming.contentType;
    if (!merged.pageUrl && incoming.pageUrl) merged.pageUrl = incoming.pageUrl;
    return merged;
  }

  function mergeCandidates() {
    const output = [];
    const indexes = new Map();
    for (const collection of arguments) {
      if (!Array.isArray(collection)) continue;
      for (const rawCandidate of collection) {
        const candidate = normalizeCandidate(rawCandidate);
        if (!candidate) continue;
        const key = `${candidate.kind}\n${candidate.url}`;
        const index = indexes.get(key);
        if (index === undefined) {
          indexes.set(key, output.length);
          output.push(candidate);
        } else {
          output[index] = mergeCandidateMetadata(output[index], candidate);
        }
      }
    }
    return output;
  }

  function cloneCandidates(items) {
    return items.map((item) => ({ ...item }));
  }

  function createCandidateStore(options) {
    const settings = options || {};
    const maxTabs = Number.isInteger(settings.maxTabs) && settings.maxTabs > 0 ? settings.maxTabs : 50;
    const perTab = Number.isInteger(settings.maxCandidatesPerTab) && settings.maxCandidatesPerTab > 0
      ? settings.maxCandidatesPerTab
      : 100;
    const tabs = new Map();

    function validTabId(tabId) {
      return Number.isInteger(tabId) && tabId >= 0;
    }

    function touch(tabId, candidates) {
      tabs.delete(tabId);
      tabs.set(tabId, candidates);
      while (tabs.size > maxTabs) {
        tabs.delete(tabs.keys().next().value);
      }
    }

    return {
      add(tabId, candidates) {
        if (!validTabId(tabId)) return [];
        const merged = mergeCandidates(tabs.get(tabId) || [], candidates).slice(0, perTab);
        touch(tabId, merged);
        return cloneCandidates(merged);
      },
      get(tabId) {
        if (!validTabId(tabId)) return [];
        return cloneCandidates(tabs.get(tabId) || []);
      },
      clear(tabId) {
        if (validTabId(tabId)) tabs.delete(tabId);
      },
      removeUrl(tabId, value) {
        if (!validTabId(tabId)) return [];
        const url = normalizeUrl(value);
        if (!url) return cloneCandidates(tabs.get(tabId) || []);
        const filtered = (tabs.get(tabId) || []).filter((candidate) => candidate.url !== url);
        if (filtered.length) touch(tabId, filtered);
        else tabs.delete(tabId);
        return cloneCandidates(filtered);
      },
      replaceUrl(tabId, rawCandidate) {
        if (!validTabId(tabId)) return [];
        let replacement = normalizeCandidate(rawCandidate);
        if (!replacement) return cloneCandidates(tabs.get(tabId) || []);
        const current = tabs.get(tabId) || [];
        const index = current.findIndex((candidate) => candidate.url === replacement.url);
        for (const candidate of current) {
          if (candidate.url === replacement.url) {
            replacement = mergeCandidateMetadata(replacement, candidate);
          }
        }
        const filtered = current.filter((candidate) => candidate.url !== replacement.url);
        filtered.splice(index < 0 ? filtered.length : index, 0, replacement);
        const limited = filtered.slice(0, perTab);
        touch(tabId, limited);
        return cloneCandidates(limited);
      },
      replace(tabId, candidates) {
        if (!validTabId(tabId)) return [];
        const normalized = mergeCandidates(candidates).slice(0, perTab);
        touch(tabId, normalized);
        return cloneCandidates(normalized);
      },
      entries() {
        return Array.from(tabs, ([tabId, candidates]) => [tabId, cloneCandidates(candidates)]);
      },
    };
  }

  return Object.freeze({
    DEFAULT_TITLE,
    normalizeUrl,
    inferMediaKind,
    normalizeCandidate,
    mergeCandidates,
    createCandidateStore,
  });
}));
