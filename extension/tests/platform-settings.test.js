'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');

const { createPlatformSettingsController } = require('../lib/platform-settings.js');

const NOTICE_VERSION = '2026-07-28-v1';

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((promiseResolve, promiseReject) => {
    resolve = promiseResolve;
    reject = promiseReject;
  });
  return { promise, resolve, reject };
}

function settings(enabled, noticeVersion = NOTICE_VERSION) {
  return {
    experimentalPlatformCompatibilityEnabled: enabled,
    platformNoticeVersion: enabled ? noticeVersion : '',
    currentPlatformNoticeVersion: noticeVersion,
  };
}

function harness(clientOverrides = {}) {
  const calls = [];
  const renders = [];
  const dialogs = [];
  const client = {
    async getSettings() {
      calls.push({ method: 'GET' });
      return settings(false);
    },
    async setPlatformCompatibility(input) {
      calls.push({ method: 'PUT', input });
      return settings(input.enabled);
    },
    ...clientOverrides,
  };
  const view = {
    render(model) { renders.push({ ...model }); },
    showNotice() { dialogs.push('show'); },
    closeNotice() { dialogs.push('close'); },
  };
  const controller = createPlatformSettingsController({ client, view });
  return { calls, client, controller, dialogs, renders };
}

test('load renders the helper disabled state and current notice version', async () => {
  const { calls, controller, renders } = harness();

  await controller.load();

  assert.deepEqual(calls, [{ method: 'GET' }]);
  assert.equal(renders[0].status, 'loading');
  assert.equal(renders.at(-1).status, 'disabled');
  assert.equal(renders.at(-1).enabled, false);
  assert.equal(renders.at(-1).available, true);
  assert.equal(renders.at(-1).noticeVersion, NOTICE_VERSION);
});

test('requesting enable shows the notice without saving and cancel keeps disabled', async () => {
  const { calls, controller, dialogs, renders } = harness();
  await controller.load();

  controller.requestEnable();

  assert.deepEqual(dialogs, ['show']);
  assert.equal(calls.filter((call) => call.method === 'PUT').length, 0);

  controller.cancelEnable();

  assert.deepEqual(dialogs, ['show', 'close']);
  assert.equal(renders.at(-1).enabled, false);
  assert.equal(calls.filter((call) => call.method === 'PUT').length, 0);
});

test('confirm sends the current GET notice version and enables only after PUT succeeds', async () => {
  const save = deferred();
  const currentVersion = '2026-08-03-v2';
  const { calls, controller, renders } = harness({
    async getSettings() {
      calls.push({ method: 'GET' });
      return settings(false, currentVersion);
    },
    async setPlatformCompatibility(input) {
      calls.push({ method: 'PUT', input });
      return save.promise;
    },
  });
  await controller.load();
  controller.requestEnable();

  const confirmation = controller.confirmEnable();
  await Promise.resolve();

  assert.deepEqual(calls.at(-1), {
    method: 'PUT',
    input: { enabled: true, acknowledged: true, noticeVersion: currentVersion },
  });
  assert.equal(renders.at(-1).enabled, false);
  assert.equal(renders.at(-1).busy, true);
  assert.equal(renders.at(-1).pendingAction, 'enable');

  save.resolve(settings(true, currentVersion));
  await confirmation;

  assert.equal(renders.at(-1).status, 'enabled');
  assert.equal(renders.at(-1).enabled, true);
  assert.equal(renders.at(-1).busy, false);
  assert.equal(renders.at(-1).pendingAction, '');
});

test('failed enable restores authoritative disabled state and a fixed short error', async () => {
  const { controller, renders } = harness({
    async setPlatformCompatibility() {
      const error = new Error('/Users/private/settings.json could not be written');
      error.code = 'settings_unavailable';
      throw error;
    },
  });
  await controller.load();
  controller.requestEnable();

  await controller.confirmEnable();

  assert.equal(renders.at(-1).status, 'disabled');
  assert.equal(renders.at(-1).enabled, false);
  assert.equal(renders.at(-1).busy, false);
  assert.equal(renders.at(-1).error, '无法保存本地设置');
  assert.doesNotMatch(renders.at(-1).error, /Users|settings\.json/);
});

test('disable is immediate, idempotent, and sends no acknowledgment', async () => {
  const save = deferred();
  const { calls, controller, renders } = harness({
    async getSettings() {
      calls.push({ method: 'GET' });
      return settings(true);
    },
    async setPlatformCompatibility(input) {
      calls.push({ method: 'PUT', input });
      return save.promise;
    },
  });
  await controller.load();

  const first = controller.disable();
  const second = controller.disable();
  await Promise.resolve();

  assert.equal(first, second);
  assert.equal(renders.at(-1).enabled, false);
  assert.equal(renders.at(-1).busy, true);
  assert.equal(renders.at(-1).pendingAction, 'disable');
  assert.deepEqual(calls.filter((call) => call.method === 'PUT'), [
    { method: 'PUT', input: { enabled: false } },
  ]);

  save.resolve(settings(false));
  await first;
  await controller.disable();

  assert.equal(calls.filter((call) => call.method === 'PUT').length, 1);
  assert.equal(renders.at(-1).status, 'disabled');
  assert.equal(renders.at(-1).busy, false);
});

test('each options controller reloads helper state instead of using browser cache', async () => {
  let enabled = false;
  let reads = 0;
  const client = {
    async getSettings() {
      reads += 1;
      return settings(enabled);
    },
    async setPlatformCompatibility() { throw new Error('unused'); },
  };
  const first = harness(client);
  await first.controller.load();
  enabled = true;
  const reopened = harness(client);

  await reopened.controller.load();

  assert.equal(reads, 2);
  assert.equal(first.renders.at(-1).enabled, false);
  assert.equal(reopened.renders.at(-1).enabled, true);
});

test('concurrent loads and repeated confirmations coalesce without stale overwrites', async () => {
  const firstLoad = deferred();
  const secondLoad = deferred();
  const save = deferred();
  let getCount = 0;
  let putCount = 0;
  const { controller, renders } = harness({
    getSettings() {
      getCount += 1;
      return getCount === 1 ? firstLoad.promise : secondLoad.promise;
    },
    setPlatformCompatibility() {
      putCount += 1;
      return save.promise;
    },
  });

  const older = controller.load();
  const newer = controller.load();
  secondLoad.resolve(settings(false, '2026-08-03-v2'));
  await newer;
  firstLoad.resolve(settings(true, NOTICE_VERSION));
  await older;

  assert.equal(renders.at(-1).enabled, false);
  assert.equal(renders.at(-1).noticeVersion, '2026-08-03-v2');

  controller.requestEnable();
  const firstConfirmation = controller.confirmEnable();
  const repeatedConfirmation = controller.confirmEnable();
  await Promise.resolve();
  assert.equal(firstConfirmation, repeatedConfirmation);
  assert.equal(putCount, 1);
  save.resolve(settings(true, '2026-08-03-v2'));
  await firstConfirmation;
  assert.equal(renders.at(-1).enabled, true);
});

test('a disable intent wins over an older pending enable response', async () => {
  const enableSave = deferred();
  const disableSave = deferred();
  const calls = [];
  const { controller, renders } = harness({
    async setPlatformCompatibility(input) {
      calls.push(input);
      if (input.enabled) return enableSave.promise;
      return disableSave.promise;
    },
  });
  await controller.load();
  controller.requestEnable();
  const enable = controller.confirmEnable();

  const disable = controller.disable();
  await Promise.resolve();

  assert.equal(renders.at(-1).enabled, false);
  assert.deepEqual(calls, [{ enabled: true, acknowledged: true, noticeVersion: NOTICE_VERSION }]);
  enableSave.resolve(settings(true));
  await enable;
  await Promise.resolve();
  assert.deepEqual(calls, [
    { enabled: true, acknowledged: true, noticeVersion: NOTICE_VERSION },
    { enabled: false },
  ]);
  assert.equal(renders.at(-1).enabled, false);
  disableSave.resolve(settings(false));
  await disable;
  assert.equal(renders.at(-1).enabled, false);
  assert.equal(renders.at(-1).status, 'disabled');
});
