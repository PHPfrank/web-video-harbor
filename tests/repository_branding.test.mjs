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

test('community edition licensing, privacy, and usage boundaries stay aligned', () => {
  const version = fs.readFileSync(path.join(repoRoot, 'VERSION'), 'utf8');
  const manifest = JSON.parse(fs.readFileSync(path.join(repoRoot, 'extension', 'manifest.json'), 'utf8'));
  const readme = fs.readFileSync(path.join(repoRoot, 'README.md'), 'utf8');
  const guide = fs.readFileSync(path.join(repoRoot, 'docs', '安装使用说明.md'), 'utf8');
  const privacy = fs.readFileSync(path.join(repoRoot, 'PRIVACY.md'), 'utf8');
  const boundary = fs.readFileSync(path.join(repoRoot, 'docs', '使用边界.md'), 'utf8');
  const license = fs.readFileSync(path.join(repoRoot, 'LICENSE'), 'utf8');
  const notices = fs.readFileSync(path.join(repoRoot, 'THIRD_PARTY_NOTICES.md'), 'utf8');

  assert.equal(version, '1.0.0\n');
  assert.equal(manifest.version, '1.0.0');
  assert.match(manifest.description, /Community Edition/);
  assert.doesNotMatch(manifest.description, /YouTube|哔哩哔哩|Bilibili|微信视频号/);

  assert.match(license, /^MIT License\n/);
  assert.match(license, /Copyright \(c\) 2026 PHPfrank/);
  assert.match(license, /Permission is hereby granted, free of charge, to any person obtaining a copy/);
  assert.match(license, /THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND/);

  assert.match(readme, /^# WebVideoHarbor（网页视频港）\n/);
  assert.match(readme, /Community Edition/);
  assert.match(readme, /v1\.0\.0/);
  assert.match(readme, /完全免费/);
  assert.match(readme, /开源/);
  assert.match(readme, /本地运行/);
  assert.match(readme, /技术学习与交流/);
  assert.match(readme, /MP4/);
  assert.match(readme, /WebM/);
  assert.match(readme, /非加密.*M3U8|M3U8.*非加密/);
  assert.match(readme, /\[MIT 许可证\]\(LICENSE\)/);
  assert.match(readme, /\[隐私说明\]\(PRIVACY\.md\)/);
  assert.match(readme, /\[使用边界\]\(docs\/使用边界\.md\)/);
  assert.match(readme, /\[安装使用说明\]\(docs\/安装使用说明\.md\)/);
  assert.match(readme, /\[第三方组件说明\]\(THIRD_PARTY_NOTICES\.md\)/);

  const opening = readme.split(/^## /m, 1)[0];
  assert.doesNotMatch(opening, /YouTube|哔哩哔哩|Bilibili|微信视频号/);

  for (const topic of ['不上传', '不包含分析', '不读取 Cookie', '不访问用户账号']) {
    assert.match(privacy, new RegExp(topic), `privacy documentation is missing: ${topic}`);
  }
  for (const topic of [
    '默认关闭', '登录', '会员', '付费', '私有', '加密', 'DRM',
    '地区限制', '机器人验证', '只处理你有权保存的内容',
  ]) {
    assert.match(boundary, new RegExp(topic), `usage boundary is missing: ${topic}`);
  }

  assert.match(notices, /WebVideoHarbor.*MIT/s);
  assert.match(notices, /yt-dlp 2026\.07\.04/);
  assert.match(notices, /Deno 2\.8\.1/);
  assert.match(notices, /FFmpeg/);

  for (const oldPlan of [
    '2026-07-27-webvideoharbor-commercialization-design.md',
    '2026-07-27-webvideoharbor-v0.3-commercial-editions.md',
  ]) {
    const content = fs.readFileSync(path.join(repoRoot, 'docs', 'plans', oldPlan), 'utf8');
    assert.match(content.slice(0, 500), /已废弃|已取代/);
    assert.match(content.slice(0, 500), /2026-07-28-webvideoharbor-community-open-source-design\.md/);
  }

  const productionAndCurrentDocs = [
    ...filesUnder(path.join(repoRoot, 'extension')),
    ...filesUnder(path.join(repoRoot, 'helper')).filter((file) => !file.endsWith('_test.go')),
    ...filesUnder(path.join(repoRoot, 'scripts')),
    path.join(repoRoot, 'README.md'),
    path.join(repoRoot, 'PRIVACY.md'),
    path.join(repoRoot, 'docs', '使用边界.md'),
    path.join(repoRoot, 'docs', '安装使用说明.md'),
  ];
  for (const file of productionAndCurrentDocs) {
    const content = fs.readFileSync(file, 'utf8');
    assert.doesNotMatch(
      content,
      /激活码|付款链接|支付链接|license[_ -]?key|activation[_ -]?(?:key|server)|payment[_ -]?(?:url|endpoint)|pro[_ -]?required/i,
      `commercial gate found in ${path.relative(repoRoot, file)}`,
    );
  }

  const docs = `${readme}\n${guide}\n${privacy}\n${boundary}`;
  for (const topic of [
    'MP4', 'WebM', 'M3U8', 'Cookie', '登录', '会员', 'DRM', 'yt-dlp', 'Deno',
    '默认关闭', '实验性平台兼容', '停止助手', '保留现有配对状态',
  ]) {
    assert.match(docs, new RegExp(topic), `documentation is missing: ${topic}`);
  }
});

function filesUnder(root) {
  const files = [];
  for (const entry of fs.readdirSync(root, { withFileTypes: true })) {
    const target = path.join(root, entry.name);
    if (entry.isDirectory()) files.push(...filesUnder(target));
    else if (entry.isFile() && /\.(?:go|js|mjs|cjs|zsh|html|md)$/.test(entry.name)) files.push(target);
  }
  return files;
}
