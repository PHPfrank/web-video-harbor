(function initVideoGrabberPlatform(root, factory) {
  'use strict';

  const api = factory();
  root.VideoGrabberPlatform = api;
  if (typeof module === 'object' && module.exports) {
    module.exports = api;
  }
}(typeof globalThis !== 'undefined' ? globalThis : this, function createPlatformApi() {
  'use strict';

  const MAX_URL_LENGTH = 2048;
  const MAX_TITLE_LENGTH = 120;
  const DEFAULT_TITLE = '未命名视频';
  const YOUTUBE_ID_PATTERN = /^[A-Za-z0-9_-]{11}$/;
  const BILIBILI_ID_PATTERN = /^(?:BV[A-Za-z0-9]{10}|av[1-9][0-9]*)$/;
  const PART_PATTERN = /^[0-9]+$/;
  const CONTROL_CHARACTER_PATTERN = /[\u0000-\u001F\u007F]/;
  const ASCII_HOST_PATTERN = /^[A-Za-z0-9.-]+$/;
  const EXPERIMENTAL_ROOTS = Object.freeze([
    'youtube.com',
    'bilibili.com',
    'weixin.qq.com',
    'wechat.com',
  ]);
  const SUPPORTED_AUTHORITIES = new Set([
    'www.youtube.com',
    'youtube.com',
    'youtu.be',
    'www.bilibili.com',
  ]);
  const QUALITY_OPTIONS = Object.freeze([
    Object.freeze({ value: 'best', label: '最佳画质' }),
    Object.freeze({ value: '1080', label: '1080P' }),
    Object.freeze({ value: '720', label: '720P' }),
  ]);

  function rawQueryFrom(value) {
    const queryIndex = value.indexOf('?');
    return queryIndex < 0 ? '' : value.slice(queryIndex + 1);
  }

  function hasValidQueryEncoding(rawQuery) {
    return !/%(?![0-9A-Fa-f]{2})/.test(rawQuery) && !rawQuery.includes(';');
  }

  function literalQueryValue(rawQuery, key, pattern) {
    const prefix = `${key}=`;
    let value = '';
    let count = 0;
    for (const field of rawQuery.split('&')) {
      if (!field.startsWith(prefix)) continue;
      count += 1;
      value = field.slice(prefix.length);
      if (!pattern.test(value)) return null;
    }
    return count === 1 ? value : null;
  }

  function pathId(pathname, prefix) {
    let path = pathname;
    if (prefix) {
      if (!path.startsWith(`${prefix}/`)) return null;
      path = path.slice(prefix.length);
    }
    if (path.endsWith('/')) path = path.slice(0, -1);
    if (!path.startsWith('/')) return null;
    const id = path.slice(1);
    return id && !id.includes('/') ? id : null;
  }

  function classifyYouTube(url, rawQuery) {
    if (url.hostname === 'youtu.be') {
      const id = pathId(url.pathname, '');
      if (!id || !YOUTUBE_ID_PATTERN.test(id)) return null;
      return { provider: 'youtube', url: `https://youtu.be/${id}` };
    }

    if (url.pathname === '/watch') {
      if (url.searchParams.getAll('v').length !== 1) return null;
      const id = literalQueryValue(rawQuery, 'v', YOUTUBE_ID_PATTERN);
      if (!id) return null;
      return { provider: 'youtube', url: `https://www.youtube.com/watch?v=${id}` };
    }

    const id = pathId(url.pathname, '/shorts');
    if (!id || !YOUTUBE_ID_PATTERN.test(id)) return null;
    return { provider: 'youtube', url: `https://www.youtube.com/shorts/${id}` };
  }

  function classifyBilibili(url, rawQuery) {
    const id = pathId(url.pathname, '/video');
    if (!id || !BILIBILI_ID_PATTERN.test(id)) return null;

    const parts = url.searchParams.getAll('p');
    if (parts.length > 1) return null;
    let canonicalUrl = `https://www.bilibili.com/video/${id}`;
    if (parts.length === 1) {
      const part = literalQueryValue(rawQuery, 'p', PART_PATTERN);
      if (part === null) return null;
      canonicalUrl += `?p=${part}`;
    }
    return { provider: 'bilibili', url: canonicalUrl };
  }

  function classifyPlatformUrl(value) {
    if (typeof value !== 'string' || value === '') return null;
    if (new TextEncoder().encode(value).byteLength > MAX_URL_LENGTH) return null;
    if (CONTROL_CHARACTER_PATTERN.test(value)) return null;
    if (value.includes('#')) return null;

    const authorityMatch = /^https:\/\/([^/?#]+)(?:[/?]|$)/.exec(value);
    if (!authorityMatch || !SUPPORTED_AUTHORITIES.has(authorityMatch[1])) return null;

    const rawQuery = rawQueryFrom(value);
    if (!hasValidQueryEncoding(rawQuery)) return null;

    let url;
    try {
      url = new URL(value);
    } catch (_error) {
      return null;
    }
    if (url.protocol !== 'https:' || url.username || url.password || url.port || url.hash) return null;
    if (!SUPPORTED_AUTHORITIES.has(url.hostname) || url.host !== authorityMatch[1]) return null;
    if (url.pathname.includes('%')) return null;

    if (url.hostname === 'www.bilibili.com') return classifyBilibili(url, rawQuery);
    return classifyYouTube(url, rawQuery);
  }

  function isExperimentalPlatformPage(value) {
    if (typeof value !== 'string' || value === '' || CONTROL_CHARACTER_PATTERN.test(value)) return false;
    const authorityMatch = /^https:\/\/([^/?#]+)(?:[/?#]|$)/i.exec(value);
    if (!authorityMatch) return false;
    const rawHost = authorityMatch[1];
    if (!ASCII_HOST_PATTERN.test(rawHost) || rawHost.includes(':') || rawHost.endsWith('.')) return false;

    let url;
    try {
      url = new URL(value);
    } catch (_error) {
      return false;
    }
    if (url.protocol !== 'https:' || url.username || url.password || url.port) return false;
    const host = rawHost.toLowerCase();
    if (url.hostname.toLowerCase() !== host || url.host.toLowerCase() !== host) return false;
    if (host === 'youtu.be') return true;
    return EXPERIMENTAL_ROOTS.some((root) => host === root || host.endsWith(`.${root}`));
  }

  function normalizeTitle(value) {
    if (typeof value !== 'string') return DEFAULT_TITLE;
    const title = value.trim();
    return title ? title.slice(0, MAX_TITLE_LENGTH) : DEFAULT_TITLE;
  }

  function candidateForPage(page) {
    if (!page || typeof page !== 'object' || Array.isArray(page)) return null;
    const classified = classifyPlatformUrl(page.url);
    if (!classified) return null;
    return {
      url: classified.url,
      kind: 'platform',
      provider: classified.provider,
      title: normalizeTitle(page.title),
    };
  }

  function candidatesForPage(input) {
    const page = input && typeof input === 'object' && !Array.isArray(input) ? input : {};
    const candidates = Array.isArray(page.candidates) ? page.candidates.slice() : [];
    const experimentalPage = isExperimentalPlatformPage(page.url);
    if (experimentalPage && page.experimentalEnabled !== true) {
      return { candidates: [], experimentalPlatformBlocked: true };
    }
    if (page.experimentalEnabled !== true) {
      return { candidates, experimentalPlatformBlocked: false };
    }
    const platformCandidate = candidateForPage({ url: page.url, title: page.title });
    const combined = platformCandidate
      && !candidates.some((candidate) => candidate && candidate.url === platformCandidate.url)
      ? [platformCandidate, ...candidates] : candidates;
    return { candidates: combined, experimentalPlatformBlocked: false };
  }

  return Object.freeze({
    classifyPlatformUrl,
    isExperimentalPlatformPage,
    candidateForPage,
    candidatesForPage,
    QUALITY_OPTIONS,
  });
}));
