import assert from 'node:assert/strict';
import test from 'node:test';

import { selectChromeLaunch } from './chrome_launch.mjs';

test('uses native arm64 Chrome when an x64 Node process runs under Rosetta', () => {
  const chromePath = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const chromeArguments = ['--headless=new', '--remote-debugging-port=0'];

  assert.deepEqual(selectChromeLaunch({
    chromePath,
    chromeArguments,
    platform: 'darwin',
    runtimeArch: 'x64',
    nativeArm64Available: true,
  }), {
    command: '/usr/bin/arch',
    arguments: ['-arm64', chromePath, ...chromeArguments],
  });
});

test('keeps direct Chrome launch unless Rosetta can switch to native arm64', () => {
  const chromePath = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const chromeArguments = ['--headless=new'];
  for (const runtime of [
    { platform: 'darwin', runtimeArch: 'arm64', nativeArm64Available: true },
    { platform: 'darwin', runtimeArch: 'x64', nativeArm64Available: false },
    { platform: 'linux', runtimeArch: 'x64', nativeArm64Available: true },
  ]) {
    assert.deepEqual(selectChromeLaunch({ chromePath, chromeArguments, ...runtime }), {
      command: chromePath,
      arguments: chromeArguments,
    });
  }
});
