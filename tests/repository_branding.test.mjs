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

test('v0.2.0 branding and platform documentation stay aligned', () => {
  const manifest = JSON.parse(fs.readFileSync(path.join(repoRoot, 'extension', 'manifest.json'), 'utf8'));
  const readme = fs.readFileSync(path.join(repoRoot, 'README.md'), 'utf8');
  const guide = fs.readFileSync(path.join(repoRoot, 'docs', '安装使用说明.md'), 'utf8');
  const docs = `${readme}\n${guide}`;

  assert.equal(manifest.version, '0.2.0');
  assert.match(manifest.description, /YouTube|哔哩哔哩/);
  for (const topic of [
    'YouTube', '哔哩哔哩', '单个视频', '最佳画质', '1080P', '720P', 'MP4', 'MKV',
    'Cookie', '登录', '会员', 'DRM', '播放列表', 'yt-dlp', '不会静默更新',
    '停止助手', '替换安装包', '重新加载扩展', '启动助手', '保留现有配对状态',
    '平台解析器缺失', '平台解析规则已变化', 'PO Token',
  ]) {
    assert.match(docs, new RegExp(topic), `documentation is missing: ${topic}`);
  }
});
