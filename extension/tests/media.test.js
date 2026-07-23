'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');

const {
  normalizeUrl,
  inferMediaKind,
  normalizeCandidate,
  mergeCandidates,
  createCandidateStore,
} = require('../lib/media.js');

test('normalizeUrl resolves relative URLs, preserves queries, and removes fragments', () => {
  assert.equal(
    normalizeUrl('../media/movie.mp4?token=a%2Fb#player', 'https://example.com/articles/one/'),
    'https://example.com/articles/media/movie.mp4?token=a%2Fb',
  );
});

test('normalizeUrl accepts only credential-free HTTP(S) URLs', () => {
  for (const value of [
    '',
    '   ',
    'not a url',
    'blob:https://example.com/id',
    'data:video/mp4;base64,AAAA',
    'javascript:alert(1)',
    'file:///tmp/movie.mp4',
    'chrome-extension://abc/movie.mp4',
    'ftp://example.com/movie.mp4',
    'https://user:secret@example.com/movie.mp4',
  ]) {
    assert.equal(normalizeUrl(value), null, value);
  }
  assert.equal(normalizeUrl(' HTTPS://Example.COM:443/a.mp4 '), 'https://example.com/a.mp4');
});

test('normalizeUrl rejects oversized per-candidate URLs', () => {
  assert.equal(normalizeUrl(`https://example.com/movie.mp4?token=${'a'.repeat(9000)}`), null);
});

test('inferMediaKind recognizes exact path extensions despite query strings', () => {
  assert.equal(inferMediaKind('https://cdn.example/movie.MP4?x=.m3u8'), 'mp4');
  assert.equal(inferMediaKind('https://cdn.example/master.m3u8?file=.mp4'), 'hls');
  assert.equal(inferMediaKind('https://cdn.example/movie.mp4.js'), 'unknown');
  assert.equal(inferMediaKind('https://cdn.example/path/notmp4'), 'unknown');
});

test('inferMediaKind recognizes exact media MIME types including extensionless WeChat CDN URLs', () => {
  assert.equal(inferMediaKind('https://finder.video.qq.com/abc', 'video/mp4; charset=binary'), 'mp4');
  for (const mime of [
    'application/vnd.apple.mpegurl',
    'application/x-mpegurl',
    'audio/mpegurl',
  ]) {
    assert.equal(inferMediaKind('https://finder.video.qq.com/stream?id=1', mime), 'hls');
  }
});

test('inferMediaKind lets explicit non-media MIME override a misleading suffix', () => {
  assert.equal(inferMediaKind('https://example.com/error.mp4', 'text/html'), 'unknown');
  assert.equal(inferMediaKind('https://example.com/error.m3u8', 'application/json'), 'unknown');
  assert.equal(inferMediaKind('https://example.com/movie.mp4', 'application/octet-stream'), 'mp4');
  assert.equal(inferMediaKind('https://example.com/master.m3u8', ''), 'hls');
  assert.equal(inferMediaKind('https://example.com/file', 'video/mp4-extra'), 'unknown');
  assert.equal(inferMediaKind('https://example.com/file', 'application/vnd.apple.mpegurl.foo'), 'unknown');
});

test('normalizeCandidate sanitizes and bounds metadata without carrying secrets', () => {
  const normalized = normalizeCandidate({
    url: './movie.mp4#watch',
    baseUrl: 'https://example.com/page/',
    pageUrl: 'https://example.com/watch?id=1#player',
    title: `  ${'片'.repeat(220)}  `,
    source: 'dom',
    width: '1920',
    height: 1080,
    contentType: ' Video/MP4; charset=binary ',
    cookies: 'secret',
    headers: { Authorization: 'Bearer secret' },
    body: '<html>private</html>',
    pageBody: 'private',
  });

  assert.equal(normalized.url, 'https://example.com/page/movie.mp4');
  assert.equal(normalized.pageUrl, 'https://example.com/watch?id=1');
  assert.equal(normalized.title.length, 120);
  assert.equal(normalized.source, 'dom');
  assert.equal(normalized.kind, 'mp4');
  assert.equal(normalized.width, 1920);
  assert.equal(normalized.height, 1080);
  assert.equal(normalized.contentType, 'video/mp4');
  assert.deepEqual(Object.keys(normalized).sort(), [
    'contentType', 'height', 'kind', 'pageUrl', 'source', 'title', 'url', 'width',
  ]);
});

test('normalizeCandidate rejects an invalid pageUrl instead of retaining unsafe page context', () => {
  const normalized = normalizeCandidate({
    url: 'https://example.com/movie.mp4',
    pageUrl: 'https://user:secret@example.com/watch',
  });
  assert.equal(normalized.pageUrl, undefined);
});

test('normalizeCandidate uses safe fallbacks and rejects unknown media', () => {
  assert.equal(normalizeCandidate({ url: 'https://example.com/image.jpg' }), null);
  assert.deepEqual(
    normalizeCandidate({ url: 'https://example.com/movie.mp4', title: '  ', source: 'mystery' }),
    {
      url: 'https://example.com/movie.mp4',
      kind: 'mp4',
      title: '未命名视频',
      source: 'unknown',
    },
  );
});

test('mergeCandidates deduplicates by canonical URL and kind and keeps richer metadata', () => {
  const merged = mergeCandidates(
    [
      { url: 'https://cdn.example/movie.mp4#old', title: '未命名视频', source: 'performance' },
      { url: 'https://cdn.example/other.m3u8', title: 'Second', source: 'webRequest' },
    ],
    [
      {
        url: 'https://cdn.example/movie.mp4',
        title: 'A useful title',
        source: 'dom',
        width: 1920,
        height: 1080,
        contentType: 'video/mp4',
      },
      { url: 'https://cdn.example/movie.mp4', title: 'duplicate' },
    ],
  );

  assert.equal(merged.length, 2);
  assert.equal(merged[0].url, 'https://cdn.example/movie.mp4');
  assert.equal(merged[0].title, 'A useful title');
  assert.equal(merged[0].source, 'dom');
  assert.equal(merged[0].width, 1920);
  assert.equal(merged[0].height, 1080);
  assert.equal(merged[0].contentType, 'video/mp4');
  assert.equal(merged[1].url, 'https://cdn.example/other.m3u8');
});

test('candidate store preserves deterministic order and isolates tabs', () => {
  const store = createCandidateStore({ maxTabs: 3, maxCandidatesPerTab: 3 });
  store.add(7, [{ url: 'https://example.com/a.mp4', title: 'A' }]);
  store.add(8, [{ url: 'https://example.com/b.mp4', title: 'B' }]);
  store.add(7, [
    { url: 'https://example.com/c.m3u8', title: 'C' },
    { url: 'https://example.com/a.mp4', width: 1280 },
  ]);

  assert.deepEqual(store.get(7).map((item) => item.title), ['A', 'C']);
  assert.equal(store.get(7)[0].width, 1280);
  assert.deepEqual(store.get(8).map((item) => item.title), ['B']);
  assert.deepEqual(store.get(99), []);
  store.clear(7);
  assert.deepEqual(store.get(7), []);
  assert.deepEqual(store.get(8).map((item) => item.title), ['B']);
});

test('candidate store caps candidates and returns defensive copies', () => {
  const store = createCandidateStore({ maxTabs: 2, maxCandidatesPerTab: 2 });
  store.add(1, [
    { url: 'https://example.com/1.mp4' },
    { url: 'https://example.com/2.mp4' },
    { url: 'https://example.com/3.mp4' },
  ]);
  assert.deepEqual(store.get(1).map((item) => item.url), [
    'https://example.com/1.mp4',
    'https://example.com/2.mp4',
  ]);
  const result = store.get(1);
  result[0].title = 'mutated';
  assert.equal(store.get(1)[0].title, '未命名视频');
});
