import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

test('Makefile builds the renamed WebVideoHarbor helper command', () => {
  const command = execFileSync('/usr/bin/make', ['-n', 'build'], {
    cwd: repoRoot,
    encoding: 'utf8',
  });

  assert.match(command, /bin\/web-video-harbor-helper\b/);
  assert.match(command, /\.\/cmd\/web-video-harbor-helper\b/);
  assert.equal(fs.existsSync(path.join(repoRoot, 'helper', 'cmd', 'web-video-harbor-helper', 'main.go')), true);
  assert.doesNotMatch(command, /web-video-helper/);
});
