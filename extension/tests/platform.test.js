'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');

const platform = require('../lib/platform.js');

test('experimental page classifier matches only trusted HTTPS platform hosts', () => {
  const accepted = [
    'https://youtube.com/watch?v=x',
    'https://www.youtube.com/watch?v=x',
    'https://m.youtube.com/shorts/x',
    'https://youtu.be/x',
    'https://bilibili.com/video/x',
    'https://www.bilibili.com/video/x',
    'https://channels.weixin.qq.com/',
    'https://weixin.qq.com/',
    'https://www.wechat.com/',
    'https://WWW.YOUTUBE.COM/watch?v=x',
  ];
  const rejected = [
    '',
    'not a url',
    '//youtube.com/watch?v=x',
    'http://youtube.com/watch?v=x',
    'https://user:secret@youtube.com/watch?v=x',
    'https://youtube.com:443/watch?v=x',
    'https://youtube.com:/watch?v=x',
    'https://youtube.com.example/watch?v=x',
    'https://notyoutube.com/watch?v=x',
    'https://sub.youtu.be/x',
    'https://例子.youtube.com/watch?v=x',
    'https://yоutube.com/watch?v=x',
    'https://xn--youtube-9jg.com/watch?v=x',
    'https://youtube.com./watch?v=x',
    'https://example.com/watch?next=https://youtube.com/x',
  ];

  for (const value of accepted) assert.equal(platform.isExperimentalPlatformPage(value), true, value);
  for (const value of rejected) assert.equal(platform.isExperimentalPlatformPage(value), false, value);
});

test('candidate gating hides every experimental-page candidate only while disabled', () => {
  const captured = [
    { url: 'https://cdn.example/video.mp4', kind: 'mp4', title: 'MP4' },
    { url: 'https://cdn.example/master.m3u8', kind: 'hls', title: 'HLS' },
  ];
  const ordinary = platform.candidatesForPage({
    url: 'https://example.com/watch', title: '普通页面', candidates: captured, experimentalEnabled: false,
  });
  const youtubeDisabled = platform.candidatesForPage({
    url: 'https://www.youtube.com/watch?v=_mVb1D8wHxg', title: 'YouTube',
    candidates: captured, experimentalEnabled: false,
  });
  const bilibiliDisabled = platform.candidatesForPage({
    url: 'https://www.bilibili.com/video/BV1K3Gz6pEoo', title: 'Bilibili',
    candidates: captured, experimentalEnabled: false,
  });
  const wechatDisabled = platform.candidatesForPage({
    url: 'https://channels.weixin.qq.com/', title: '视频号', candidates: captured, experimentalEnabled: false,
  });

  assert.deepEqual(ordinary, { candidates: captured, experimentalPlatformBlocked: false });
  assert.deepEqual(youtubeDisabled, { candidates: [], experimentalPlatformBlocked: true });
  assert.deepEqual(bilibiliDisabled, { candidates: [], experimentalPlatformBlocked: true });
  assert.deepEqual(wechatDisabled, { candidates: [], experimentalPlatformBlocked: true });
});

test('enabled candidate gating keeps canonical page cards and captured WeChat media', () => {
  const captured = [{ url: 'https://cdn.example/video.mp4', kind: 'mp4', title: 'MP4' }];
  const youtube = platform.candidatesForPage({
    url: 'https://www.youtube.com/watch?v=_mVb1D8wHxg&list=ignored',
    title: 'YouTube', candidates: captured, experimentalEnabled: true,
  });
  const wechat = platform.candidatesForPage({
    url: 'https://channels.weixin.qq.com/', title: '视频号',
    candidates: captured, experimentalEnabled: true,
  });

  assert.equal(youtube.experimentalPlatformBlocked, false);
  assert.deepEqual(youtube.candidates.map((candidate) => candidate.kind), ['platform', 'mp4']);
  assert.equal(youtube.candidates[0].url, 'https://www.youtube.com/watch?v=_mVb1D8wHxg');
  assert.deepEqual(wechat, { candidates: captured, experimentalPlatformBlocked: false });
});

test('coordinated popup gating trusts the background page after a tab navigation race', () => {
  const staleTab = {
    url: 'https://example.com/old-page',
    title: '旧页面标题',
  };
  const result = platform.candidatesForCoordinatedPage({
    tab: staleTab,
    response: {
      pageUrl: 'https://www.youtube.com/watch?v=_mVb1D8wHxg',
      candidates: [{
        url: 'https://cdn.example/video.mp4', kind: 'mp4',
        pageUrl: 'https://www.youtube.com/watch?v=_mVb1D8wHxg',
      }],
    },
    experimentalEnabled: false,
  });

  assert.deepEqual(result, {
    pageUrl: 'https://www.youtube.com/watch?v=_mVb1D8wHxg',
    candidates: [],
    experimentalPlatformBlocked: true,
  });
});

test('coordinated popup gating fails closed without consistent background page context', () => {
  const candidate = {
    url: 'https://cdn.example/video.mp4', kind: 'mp4', pageUrl: 'https://example.com/watch',
  };
  for (const response of [
    null,
    { pageUrl: null, candidates: [candidate] },
    { pageUrl: 'https://user:secret@example.com/watch', candidates: [candidate] },
    { pageUrl: 'https://example.com/other', candidates: [candidate] },
  ]) {
    assert.deepEqual(platform.candidatesForCoordinatedPage({
      tab: { url: 'https://example.com/watch', title: '旧标题' },
      response,
      experimentalEnabled: true,
    }), {
      pageUrl: '', candidates: [], experimentalPlatformBlocked: false,
    });
  }
});

test('candidateForPage recognizes and canonicalizes supported single-video pages', () => {
  const cases = [
    {
      input: {
        url: 'https://www.youtube.com/watch?v=_mVb1D8wHxg&list=PLignored',
        title: 'Demo - YouTube',
      },
      expected: {
        url: 'https://www.youtube.com/watch?v=_mVb1D8wHxg',
        kind: 'platform',
        provider: 'youtube',
        title: 'Demo - YouTube',
      },
    },
    {
      input: {
        url: 'https://youtube.com/watch?v=_mVb1D8wHxg&utm_source=foo%20bar',
        title: 'YouTube without www',
      },
      expected: {
        url: 'https://www.youtube.com/watch?v=_mVb1D8wHxg',
        kind: 'platform',
        provider: 'youtube',
        title: 'YouTube without www',
      },
    },
    {
      input: { url: 'https://youtube.com/shorts/abc_123-XYZ', title: 'Short' },
      expected: {
        url: 'https://www.youtube.com/shorts/abc_123-XYZ',
        kind: 'platform',
        provider: 'youtube',
        title: 'Short',
      },
    },
    {
      input: { url: 'https://youtu.be/abc_123-XYZ?t=4', title: 'Short link' },
      expected: {
        url: 'https://youtu.be/abc_123-XYZ',
        kind: 'platform',
        provider: 'youtube',
        title: 'Short link',
      },
    },
    {
      input: {
        url: 'https://www.bilibili.com/video/BV1K3Gz6pEoo/?spm_id_from=x',
        title: 'Bilibili BV',
      },
      expected: {
        url: 'https://www.bilibili.com/video/BV1K3Gz6pEoo',
        kind: 'platform',
        provider: 'bilibili',
        title: 'Bilibili BV',
      },
    },
    {
      input: {
        url: 'https://www.bilibili.com/video/av170001?p=2&spm_id_from=foo%2Fbar',
        title: 'Bilibili av',
      },
      expected: {
        url: 'https://www.bilibili.com/video/av170001?p=2',
        kind: 'platform',
        provider: 'bilibili',
        title: 'Bilibili av',
      },
    },
  ];

  for (const { input, expected } of cases) {
    assert.deepEqual(platform.candidateForPage(input), expected, input.url);
  }
});

test('classifyPlatformUrl exposes only a provider and canonical URL', () => {
  assert.deepEqual(
    platform.classifyPlatformUrl('https://youtu.be/abc_123-XYZ?si=foo%20bar'),
    { provider: 'youtube', url: 'https://youtu.be/abc_123-XYZ' },
  );
  assert.deepEqual(
    platform.classifyPlatformUrl('https://www.bilibili.com/video/av170001?p=2'),
    { provider: 'bilibili', url: 'https://www.bilibili.com/video/av170001?p=2' },
  );
});

test('platform classifier rejects pages outside the single-video trust boundary', () => {
  const rejected = [
    '',
    '://www.youtube.com/watch?v=_mVb1D8wHxg',
    'http://www.youtube.com/watch?v=_mVb1D8wHxg',
    'https://user:secret@www.youtube.com/watch?v=_mVb1D8wHxg',
    'https://www.youtube.com:443/watch?v=_mVb1D8wHxg',
    'https://www.youtube.com:/watch?v=_mVb1D8wHxg',
    'https://www.yоutube.com/watch?v=_mVb1D8wHxg',
    'https://www.youtube.com.evil.example/watch?v=_mVb1D8wHxg',
    'https://www.youtube.com/watch?v=',
    'https://www.youtube.com/watch?v=abc',
    'https://www.youtube.com/watch?v=abc_123-中文',
    'https://www.youtube.com/watch?v=abc.123-XYZ',
    'https://www.youtube.com/watch?v=_mVb1D8wHxg&v=abc_123-XYZ',
    'https://www.youtube.com/watch?v=%5FmVb1D8wHxg',
    'https://www.youtube.com/watch?%76=_mVb1D8wHxg',
    'https://www.youtube.com/shorts/',
    'https://www.youtube.com/shorts/abc_123-XYZ/more',
    'https://youtu.be/abc',
    'https://youtu.be/abc_123-中文',
    'https://youtu.be/abc_123-XYZ/more',
    'https://www.youtube.com/playlist?list=PL123',
    'https://www.youtube.com/channel/UC123',
    'https://www.youtube.com/results?search_query=test',
    'https://www.youtube.com/live/abc_123-XYZ',
    'https://www.youtube.com/watch#v=_mVb1D8wHxg',
    'https://www.youtube.com/watch?v=_mVb1D8wHxg#v=abc_123-XYZ',
    'https://www.bilibili.com/video/',
    'https://www.bilibili.com/video/BV1K3Gz6pEo',
    'https://www.bilibili.com/video/avabc',
    'https://www.bilibili.com/video/av0',
    'https://www.bilibili.com/video/%42V1K3Gz6pEoo',
    'https://www.bilibili.com/video/BV1K3Gz6pEoo/more',
    'https://www.bilibili.com/video/av170001?p=two',
    'https://www.bilibili.com/video/av170001?p=%32',
    'https://www.bilibili.com/video/av170001?%70=2',
    'https://www.bilibili.com/video/av170001?p=1&p=2',
    'https://www.bilibili.com/medialist/play/123',
    'https://www.bilibili.com/bangumi/play/ep123',
    'https://live.bilibili.com/123',
    'https://www.bilibili.com/video/#BV1K3Gz6pEoo',
    `https://www.youtube.com/watch?v=_mVb1D8wHxg&x=${'a'.repeat(2048)}`,
  ];

  for (const url of rejected) {
    assert.equal(platform.classifyPlatformUrl(url), null, url);
    assert.equal(platform.candidateForPage({ url, title: 'Rejected' }), null, url);
  }
});

test('platform classifier rejects raw ASCII control characters before URL parsing', () => {
  const rejected = [
    'https://www.youtube.com/wa\ntch?v=_mVb1D8wHxg',
    'https://www.bilibili.com/vid\teo/BV1K3Gz6pEoo',
    'https://www.youtube.com/watch\u0000?v=_mVb1D8wHxg',
    'https://www.bilibili.com/video\u007f/BV1K3Gz6pEoo',
  ];

  for (const url of rejected) {
    assert.equal(platform.classifyPlatformUrl(url), null, JSON.stringify(url));
  }
});

test('platform classifier enforces the URL limit in UTF-8 bytes', () => {
  const prefix = 'https://www.youtube.com/watch?v=_mVb1D8wHxg&x=';
  const atLimit = `${prefix}${'中'.repeat(666)}🙂`;
  const overLimit = `${atLimit}中`;
  const encoder = new TextEncoder();

  assert.equal(encoder.encode(atLimit).byteLength, 2048);
  assert.equal(encoder.encode(overLimit).byteLength, 2051);
  assert.deepEqual(platform.classifyPlatformUrl(atLimit), {
    provider: 'youtube',
    url: 'https://www.youtube.com/watch?v=_mVb1D8wHxg',
  });
  assert.equal(platform.classifyPlatformUrl(overLimit), null);
});

test('candidateForPage accepts only page data and normalizes the title', () => {
  assert.equal(platform.candidateForPage(null), null);
  assert.equal(platform.candidateForPage([]), null);
  assert.deepEqual(
    platform.candidateForPage({
      url: 'https://youtu.be/abc_123-XYZ',
      title: '   ',
      cookies: 'secret',
      headers: { Authorization: 'secret' },
    }),
    {
      url: 'https://youtu.be/abc_123-XYZ',
      kind: 'platform',
      provider: 'youtube',
      title: '未命名视频',
    },
  );
});

test('QUALITY_OPTIONS is exact and deeply immutable', () => {
  assert.deepEqual(platform.QUALITY_OPTIONS, [
    { value: 'best', label: '最佳画质' },
    { value: '1080', label: '1080P' },
    { value: '720', label: '720P' },
  ]);
  assert.equal(Object.isFrozen(platform.QUALITY_OPTIONS), true);
  for (const option of platform.QUALITY_OPTIONS) {
    assert.equal(Object.isFrozen(option), true);
  }
  assert.throws(() => {
    platform.QUALITY_OPTIONS[0].label = 'mutated';
  }, TypeError);
  assert.equal(platform.QUALITY_OPTIONS[0].label, '最佳画质');
});
