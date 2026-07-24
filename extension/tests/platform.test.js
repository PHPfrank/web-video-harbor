'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');

const platform = require('../lib/platform.js');

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
