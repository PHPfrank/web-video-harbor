import assert from 'node:assert/strict';
import { access, readFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const repositoryRoot = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  '..',
);

const docsPath = (...parts) => path.join(repositoryRoot, 'docs', ...parts);

async function readDocument(filename) {
  return readFile(docsPath(filename), 'utf8');
}

test('GitHub Pages site provides its homepage and recommendation entry point', async () => {
  const requiredFiles = [
    docsPath('index.html'),
    docsPath('recommendations.html'),
    docsPath('site.css'),
    docsPath('.nojekyll'),
  ];

  await Promise.all(requiredFiles.map((filename) => access(filename)));

  const homepage = await readDocument('index.html');
  const recommendations = await readDocument('recommendations.html');

  assert.match(homepage, /WebVideoHarbor/);
  assert.match(homepage, /网页视频港/);
  assert.match(homepage, /href=["']recommendations\.html["']/);
  for (const document of [homepage, recommendations]) {
    assert.match(
      document,
      /href=["']https:\/\/github\.com\/PHPfrank\/web-video-harbor\/blob\/main\/PRIVACY\.md["']/,
    );
    assert.doesNotMatch(document, /href=["']\.\.\/PRIVACY\.md["']/);
  }
  assert.match(recommendations, /本页包含推广链接/);
  assert.match(recommendations, /不会增加你的购买价格/);
  assert.equal(
    [...recommendations.matchAll(/class=["'][^"']*\brecommendation-card\b[^"']*["']/g)].length,
    2,
  );
});

test('recommendation links are disclosed, current, and free of tracking claims', async () => {
  const recommendations = await readDocument('recommendations.html');
  const affiliateUrls = [
    'https://s.click.taobao.com/KXMdz3k',
    'https://www.aliyun.com/minisite/goods?userCode=c5z9bjlt',
  ];

  for (const url of affiliateUrls) {
    const escapedUrl = url.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    assert.match(
      recommendations,
      new RegExp(
        `<a\\s+[^>]*href=["']${escapedUrl}["'][^>]*rel=["']sponsored noopener noreferrer["'][^>]*>`,
      ),
    );
  }

  assert.equal(
    [...recommendations.matchAll(/<span class=["']affiliate-badge["']>推广<\/span>/g)].length,
    2,
  );
  assert.match(
    recommendations,
    /具体品牌、型号、容量、价格和售后以商家页面实时信息为准/,
  );

  const forbiddenContent = [
    /1082\.05/i,
    /到手价/i,
    /券面额/i,
    /<script\b/i,
    /gtag/i,
    /Google Analytics/i,
    /Plausible/i,
    /Umami/i,
    /Matomo/i,
    /tracking pixel/i,
  ];

  for (const pattern of forbiddenContent) {
    assert.doesNotMatch(recommendations, pattern);
  }
});
