'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const extensionDir = path.resolve(__dirname, '..');

test('manifest loads the media library before the content scanner at document_idle', () => {
  const manifest = JSON.parse(fs.readFileSync(path.join(extensionDir, 'manifest.json'), 'utf8'));
  assert.equal(manifest.manifest_version, 3);
  assert.ok(manifest.permissions.includes('webRequest'));
  assert.ok(manifest.permissions.includes('storage'));
  assert.deepEqual(manifest.content_scripts, [{
    matches: ['http://*/*', 'https://*/*'],
    js: ['lib/media.js', 'content.js'],
    run_at: 'document_idle',
  }]);
  assert.equal(manifest.background.service_worker, 'background.js');

  for (const script of [...manifest.content_scripts[0].js, manifest.background.service_worker]) {
    assert.equal(fs.existsSync(path.join(extensionDir, script)), true, script);
  }
});

test('background imports the shared media library without dynamic code', () => {
  const source = fs.readFileSync(path.join(extensionDir, 'background.js'), 'utf8');
  assert.match(source, /importScripts\(['"]lib\/media\.js['"]\)/);
  assert.doesNotMatch(source, /\beval\s*\(|new\s+Function\s*\(/);
});

test('content scanner covers DOM, performance, rescan, playback, and dynamic pages', () => {
  const source = fs.readFileSync(path.join(extensionDir, 'content.js'), 'utf8');
  assert.match(source, /querySelectorAll\(['"]video['"]\)/);
  assert.match(source, /currentSrc/);
  assert.match(source, /querySelectorAll\(['"]source['"]\)/);
  assert.match(source, /getEntriesByType\(['"]resource['"]\)/);
  assert.match(source, /pageUrl:\s*location\.href/);
  assert.match(source, /CLAIM_DOCUMENT/);
  assert.match(source, /RESCAN/);
  assert.match(source, /loadedmetadata/);
  assert.match(source, /MutationObserver/);
  assert.match(source, /attributeFilter:\s*\[['"]src['"]\]/);
  const domScanner = source.slice(source.indexOf('function domCandidates'), source.indexOf('function performanceCandidates'));
  assert.match(domScanner, /MAX_PAGE_CANDIDATES/);
});
