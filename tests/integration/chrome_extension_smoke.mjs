import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { selectChromeLaunch } from './chrome_launch.mjs';

const repoRoot = process.env.SMOKE_REPO_ROOT;
const fixtureURL = process.env.SMOKE_FIXTURE_URL;
const helperToken = process.env.SMOKE_HELPER_TOKEN;
const browserRoot = process.env.SMOKE_BROWSER_ROOT;
const downloadDir = process.env.SMOKE_DOWNLOAD_DIR;
const resultsPath = process.env.SMOKE_BROWSER_RESULTS_PATH;
const chromePath = process.env.SMOKE_CHROME_PATH;
const chromePIDPath = process.env.SMOKE_CHROME_PID_PATH;
for (const [name, value] of Object.entries({ repoRoot, fixtureURL, helperToken, browserRoot, downloadDir, resultsPath, chromePath, chromePIDPath })) {
  if (!value) throw new Error(`missing ${name}`);
}

class CDPConnection {
  constructor(webSocketURL) {
    this.nextID = 1;
    this.pending = new Map();
    this.socket = new WebSocket(webSocketURL);
    this.ready = new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error('CDP connection timed out')), 10000);
      this.socket.addEventListener('open', () => { clearTimeout(timer); resolve(); }, { once: true });
      this.socket.addEventListener('error', (error) => { clearTimeout(timer); reject(error); }, { once: true });
    });
    this.socket.addEventListener('message', (event) => {
      const message = JSON.parse(String(event.data));
      if (!message.id || !this.pending.has(message.id)) return;
      const { resolve, reject } = this.pending.get(message.id);
      this.pending.delete(message.id);
      if (message.error) reject(new Error(`${message.error.code}: ${message.error.message}`));
      else resolve(message.result || {});
    });
    this.socket.addEventListener('close', () => {
      for (const { reject } of this.pending.values()) reject(new Error('CDP connection closed'));
      this.pending.clear();
    });
  }

  async send(method, params = {}, sessionId = undefined) {
    await this.ready;
    const id = this.nextID++;
    const message = { id, method, params };
    if (sessionId) message.sessionId = sessionId;
    const response = new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`${method} timed out`));
      }, 10000);
      this.pending.set(id, {
        resolve(value) { clearTimeout(timer); resolve(value); },
        reject(error) { clearTimeout(timer); reject(error); },
      });
    });
    this.socket.send(JSON.stringify(message));
    return response;
  }

  close() { this.socket.close(); }
}

const delay = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds));

async function poll(description, operation, timeout = 10000) {
  const deadline = Date.now() + timeout;
  let lastError;
  while (Date.now() < deadline) {
    if (terminationError) throw terminationError;
    try {
      const value = await operation();
      if (value) return value;
    } catch (error) {
      lastError = error;
    }
    await delay(100);
  }
  throw new Error(`${description} timed out${lastError ? `: ${lastError.message}` : ''}`);
}

async function evaluate(cdp, sessionId, expression) {
  const result = await cdp.send('Runtime.evaluate', {
    expression,
    awaitPromise: true,
    returnByValue: true,
    userGesture: true,
  }, sessionId);
  if (result.exceptionDetails) {
    throw new Error(result.exceptionDetails.exception?.description || result.exceptionDetails.text || 'Runtime.evaluate failed');
  }
  return result.result?.value;
}

async function attach(cdp, targetId) {
  const { sessionId } = await cdp.send('Target.attachToTarget', { targetId, flatten: true });
  await cdp.send('Runtime.enable', {}, sessionId);
  return sessionId;
}

async function currentTargets(cdp) {
  return (await cdp.send('Target.getTargets')).targetInfos || [];
}

async function helperTasks() {
  const response = await fetch('http://127.0.0.1:17432/v1/tasks', {
    headers: { 'X-Video-Helper-Token': helperToken },
    cache: 'no-store',
  });
  assert.equal(response.status, 200);
  return response.json();
}

const profileDir = fs.mkdtempSync(path.join(browserRoot, 'chrome-profile-'));
const chromeLogPath = path.join(browserRoot, 'chrome-cdp.log');
const snapshotPath = path.join(browserRoot, 'popup-snapshot.txt');
const screenshotPath = path.join(browserRoot, 'popup.png');
const chromeLog = fs.openSync(chromeLogPath, 'w', 0o600);
const chromeArguments = [
  '--headless=new',
  `--user-data-dir=${profileDir}`,
  '--remote-debugging-port=0',
  '--remote-allow-origins=*',
  '--enable-unsafe-extension-debugging',
  '--disable-background-networking',
  '--disable-component-update',
  '--disable-default-apps',
  '--disable-sync',
  '--no-first-run',
  '--no-default-browser-check',
  'about:blank',
];
const nativeArm64Available = process.platform === 'darwin' && process.arch === 'x64'
  && spawnSync('/usr/bin/arch', ['-arm64', '/usr/bin/true'], { stdio: 'ignore' }).status === 0;
const chromeLaunch = selectChromeLaunch({ chromePath, chromeArguments, nativeArm64Available });
const chrome = spawn(chromeLaunch.command, chromeLaunch.arguments, { stdio: ['ignore', 'ignore', chromeLog] });
try {
  if (!Number.isSafeInteger(chrome.pid) || chrome.pid <= 1) {
    throw new Error('Chrome did not publish a valid process ID');
  }
  const chromePIDTempPath = `${chromePIDPath}.tmp-${process.pid}`;
  fs.writeFileSync(chromePIDTempPath, `${JSON.stringify({ pid: chrome.pid, profileDir })}\n`, { mode: 0o600, flag: 'wx' });
  fs.renameSync(chromePIDTempPath, chromePIDPath);
} catch (error) {
  if (chrome.exitCode === null && chrome.signalCode === null) chrome.kill('SIGKILL');
  fs.closeSync(chromeLog);
  throw error;
}
const chromeExit = new Promise((resolve) => chrome.once('exit', (code, signal) => resolve({ code, signal })));

let cdp;
let evidence = { status: 'failed', profileDir, chromeLogPath };
let terminationError;
let rejectTermination;
const termination = new Promise((_, reject) => { rejectTermination = reject; });
function handleTermination(signal) {
  if (terminationError) return;
  terminationError = new Error(`received ${signal}`);
  if (cdp) cdp.close();
  if (chrome.exitCode === null && chrome.signalCode === null) chrome.kill('SIGTERM');
  rejectTermination(terminationError);
}
const onSIGTERM = () => handleTermination('SIGTERM');
const onSIGINT = () => handleTermination('SIGINT');
process.once('SIGTERM', onSIGTERM);
process.once('SIGINT', onSIGINT);
try {
  await Promise.race([
    (async () => {
  const activePort = await poll('Chrome DevToolsActivePort', () => {
    const activePortPath = path.join(profileDir, 'DevToolsActivePort');
    if (!fs.existsSync(activePortPath)) return null;
    const [port, browserPath] = fs.readFileSync(activePortPath, 'utf8').trim().split(/\r?\n/);
    return port && browserPath ? { port, browserPath } : null;
  }, 15000);
  cdp = new CDPConnection(`ws://127.0.0.1:${activePort.port}${activePort.browserPath}`);
  const version = await cdp.send('Browser.getVersion');
  const loaded = await cdp.send('Extensions.loadUnpacked', { path: path.join(repoRoot, 'extension') });
  assert.match(loaded.id, /^[a-p]{32}$/);
  const extensions = (await cdp.send('Extensions.getExtensions')).extensions || [];
  const extension = extensions.find((item) => item.id === loaded.id);
  assert.ok(extension, 'loaded extension missing from Extensions.getExtensions');
  assert.equal(extension.name, '网页视频港');
  assert.equal(extension.enabled, true);
  assert.equal(path.resolve(extension.path), path.join(repoRoot, 'extension'));

  const extensionBase = `chrome-extension://${loaded.id}`;
  const wakePopup = await cdp.send('Target.createTarget', { url: `${extensionBase}/popup.html`, background: true });
  const serviceWorker = await poll('extension service worker target', async () => {
    const targets = await currentTargets(cdp);
    return targets.find((target) => target.type === 'service_worker' && target.url === `${extensionBase}/background.js`);
  });
  await cdp.send('Target.closeTarget', { targetId: wakePopup.targetId });

  const pageTarget = await cdp.send('Target.createTarget', { url: `${fixtureURL}/`, background: false });
  const pageSession = await attach(cdp, pageTarget.targetId);
  await poll('fixture document ready', () => evaluate(cdp, pageSession, 'document.readyState === "complete"'));
  await evaluate(cdp, pageSession, `(() => {
    const video = document.getElementById('direct-video');
    if (video) void video.play().catch(() => {});
    return document.title;
  })()`);

  const messageTarget = await cdp.send('Target.createTarget', { url: `${extensionBase}/options.html`, background: false });
  await cdp.send('Target.activateTarget', { targetId: messageTarget.targetId });
  const messageSession = await attach(cdp, messageTarget.targetId);
  await poll('extension message page ready', () => evaluate(cdp, messageSession, 'document.readyState === "complete"'));
  async function queryBackgroundMedia() {
    const result = await evaluate(cdp, messageSession, `(async () => {
      const tabs = await chrome.tabs.query({ url: ${JSON.stringify(`${fixtureURL}/*`)} });
      if (!tabs.length) return null;
      return chrome.runtime.sendMessage({ type: 'GET_TAB_MEDIA', tabId: tabs[0].id });
    })()`);
    return result && result.ok && Array.isArray(result.candidates) ? result : null;
  }
  await poll('DOM MP4 and HLS discovery', async () => {
    const result = await queryBackgroundMedia();
    if (!result) return null;
    const paths = result.candidates.map((candidate) => `${candidate.kind}:${new URL(candidate.url).pathname}`);
    return paths.includes('mp4:/direct.mp4') && paths.includes('hls:/master.m3u8') ? result : null;
  }, 15000);
  await evaluate(cdp, pageSession, `fetch('/wechat-stream?id=after-playback', {
    headers: { Range: 'bytes=0-1023' }
  }).then((response) => response.arrayBuffer()).then(() => true)`);
  const backgroundResult = await poll('extensionless WeChat-like response discovery', async () => {
    const result = await queryBackgroundMedia();
    return result && result.candidates.some((candidate) => new URL(candidate.url).pathname === '/wechat-stream') ? result : null;
  }, 15000);
  const discovered = backgroundResult.candidates;
  assert.ok(discovered.some((candidate) => candidate.kind === 'mp4' && new URL(candidate.url).pathname === '/direct.mp4'));
  assert.ok(discovered.some((candidate) => candidate.kind === 'hls' && new URL(candidate.url).pathname === '/master.m3u8'));
  assert.ok(discovered.some((candidate) => candidate.kind === 'mp4' && new URL(candidate.url).pathname === '/wechat-stream'));
  const directCandidateIndex = discovered.findIndex((candidate) => candidate.kind === 'mp4' && new URL(candidate.url).pathname === '/direct.mp4');
  const masterCandidateIndex = discovered.findIndex((candidate) => candidate.kind === 'hls' && new URL(candidate.url).pathname === '/master.m3u8');
  await evaluate(cdp, messageSession, `chrome.storage.local.set({ videoHelperToken: ${JSON.stringify(helperToken)} })`);
  await cdp.send('Target.closeTarget', { targetId: messageTarget.targetId });

  const beforeTaskIDs = new Set((await helperTasks()).map((task) => task.id));
  await cdp.send('Target.activateTarget', { targetId: pageTarget.targetId });
  const popupTarget = await cdp.send('Target.createTarget', { url: `${extensionBase}/popup.html`, background: true });
  const popupSession = await attach(cdp, popupTarget.targetId);
  await cdp.send('Page.enable', {}, popupSession);
  const popupSnapshot = await poll('popup connected candidate view', async () => {
    const state = await evaluate(cdp, popupSession, `({
      ready: document.readyState,
      text: document.body ? document.body.innerText : '',
      cards: document.querySelectorAll('.candidate-card').length,
      connected: document.body ? document.body.innerText.includes('本地助手已连接') : false
    })`);
    return state && state.ready === 'complete' && state.connected && state.cards >= 3 ? state : null;
  }, 15000);
  fs.writeFileSync(snapshotPath, `${popupSnapshot.text}\n`, { mode: 0o600 });
  const screenshot = await cdp.send('Page.captureScreenshot', { format: 'png' }, popupSession);
  fs.writeFileSync(screenshotPath, Buffer.from(screenshot.data, 'base64'), { mode: 0o600 });

  const directClicked = await evaluate(cdp, popupSession, `(() => {
    const card = document.querySelectorAll('.candidate-card')[${directCandidateIndex}];
    const button = card?.querySelector('button[data-action="download"]');
    if (!button || button.disabled) return false;
    button.click();
    return true;
  })()`);
  assert.equal(directClicked, true);

  const inspectClicked = await evaluate(cdp, popupSession, `(() => {
    const card = document.querySelectorAll('.candidate-card')[${masterCandidateIndex}];
    const button = card?.querySelector('button[data-action="inspect"]');
    if (!button || button.disabled) return false;
    button.click();
    return true;
  })()`);
  assert.equal(inspectClicked, true);
  await poll('popup HLS quality inspection', () => evaluate(cdp, popupSession,
    `Boolean(document.querySelectorAll('.candidate-card')[${masterCandidateIndex}]
      ?.querySelector('select.quality-select'))`));
  const hlsClicked = await evaluate(cdp, popupSession, `(() => {
    const card = document.querySelectorAll('.candidate-card')[${masterCandidateIndex}];
    const button = card?.querySelector('button[data-action="download"]');
    if (!button || button.disabled) return false;
    button.click();
    return true;
  })()`);
  assert.equal(hlsClicked, true);

  const browserTasks = await poll('popup-created downloads', async () => {
    const created = (await helperTasks()).filter((task) => !beforeTaskIDs.has(task.id));
    if (created.some((task) => task.status === 'failed' || task.status === 'canceled')) {
      throw new Error(`popup task failed: ${JSON.stringify(created.map((task) => ({ status: task.status, code: task.errorCode })))}`);
    }
    return created.length >= 2 && created.every((task) => task.status === 'completed') ? created : null;
  }, 25000);
  const directTask = browserTasks.find((task) => task.url.endsWith('/direct.mp4'));
  const hlsTask = browserTasks.find((task) => task.url.includes('/1080/index.m3u8'));
  assert.ok(directTask && hlsTask, JSON.stringify(browserTasks.map((task) => ({
    url: task.url,
    title: task.title,
    status: task.status,
    output: task.outputPath,
  }))));
  for (const task of [directTask, hlsTask]) {
    const relative = path.relative(downloadDir, task.outputPath);
    assert.ok(relative && relative !== '..' && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative));
    assert.ok(fs.statSync(task.outputPath).size > 0);
  }

  evidence = {
    status: 'passed',
    browser: version.product,
    extension: { id: loaded.id, name: extension.name, version: extension.version, enabled: extension.enabled },
    targets: { serviceWorker: serviceWorker.url, popup: `${extensionBase}/popup.html`, fixture: `${fixtureURL}/` },
    candidates: discovered.map((candidate) => ({ kind: candidate.kind, path: new URL(candidate.url).pathname })),
    outputs: { direct: directTask.outputPath, hls: hlsTask.outputPath },
    artifacts: { snapshotPath, screenshotPath, chromeLogPath, profileDir },
  };
  fs.writeFileSync(resultsPath, `${JSON.stringify(evidence, null, 2)}\n`, { mode: 0o600 });
  process.stdout.write(`Chrome CDP extension smoke passed: ${loaded.id}\n`);
    })(),
    termination,
  ]);
} catch (error) {
  evidence.error = error && error.stack ? error.stack : String(error);
  fs.writeFileSync(resultsPath, `${JSON.stringify(evidence, null, 2)}\n`, { mode: 0o600 });
  throw error;
} finally {
  process.removeListener('SIGTERM', onSIGTERM);
  process.removeListener('SIGINT', onSIGINT);
  if (cdp) cdp.close();
  if (chrome.exitCode === null && chrome.signalCode === null) chrome.kill('SIGTERM');
  await Promise.race([chromeExit, delay(3000)]);
  if (chrome.exitCode === null && chrome.signalCode === null) {
    chrome.kill('SIGKILL');
    await Promise.race([chromeExit, delay(3000)]);
  }
  fs.closeSync(chromeLog);
}
