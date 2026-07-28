'use strict';

(function startOptions() {
  const helper = globalThis.VideoGrabberHelper;
  const platformSettings = globalThis.VideoGrabberPlatformSettings;
  if (!helper || !platformSettings || typeof chrome === 'undefined'
    || !chrome.storage || !chrome.storage.local) return;

  const storageLocal = chrome.storage.local;
  const client = helper.createHelperClient({ storageLocal });
  const form = document.getElementById('pairing-form');
  const tokenInput = document.getElementById('token-input');
  const toggleButton = document.getElementById('toggle-token');
  const testButton = document.getElementById('test-button');
  const status = document.getElementById('settings-status');
  const platformToggle = document.getElementById('platform-compatibility-toggle');
  const platformStatus = document.getElementById('platform-settings-status');
  const platformDialog = document.getElementById('platform-notice-dialog');
  const platformCancel = document.getElementById('platform-notice-cancel');
  const platformConfirm = document.getElementById('platform-notice-confirm');

  const platformController = platformSettings.createPlatformSettingsController({
    client,
    view: {
      render(model) {
        platformToggle.checked = model.enabled;
        platformToggle.disabled = !model.available || model.busy || model.status === 'loading';
        platformToggle.setAttribute('aria-busy', String(model.busy));
        let message = '';
        let tone = '';
        if (model.busy) message = model.pendingAction === 'enable'
          ? '正在开启平台兼容…' : '正在关闭平台兼容…';
        else if (model.status === 'loading') message = '正在读取本地设置…';
        else if (model.status === 'enabled') {
          message = '已开启。请仅处理您有权下载的公开内容。';
          tone = 'success';
        } else if (model.status === 'disabled') message = '已关闭。普通网页视频下载不受影响。';
        else message = '当前不可用，请先完成配对并确认本地助手已启动。';
        if (model.error) {
          message = model.error;
          tone = 'error';
        }
        platformStatus.textContent = message;
        platformStatus.dataset.tone = tone;
      },
      showNotice() {
        if (!platformDialog.open) platformDialog.showModal();
      },
      closeNotice() {
        if (platformDialog.open) platformDialog.close();
      },
    },
  });

  function showStatus(message, tone) {
    status.textContent = message;
    status.dataset.tone = tone || '';
  }

  async function testConnection() {
    testButton.disabled = true;
    testButton.setAttribute('aria-busy', 'true');
    showStatus('正在测试连接…', '');
    try {
      const health = await client.health();
      await client.listTasks();
      const summary = helper.describeHealth(health);
      showStatus(summary.message, summary.tone);
      await platformController.load();
      return true;
    } catch (error) {
      showStatus(error && error.message ? error.message : '无法连接本地助手', 'error');
      return false;
    } finally {
      testButton.disabled = false;
      testButton.setAttribute('aria-busy', 'false');
    }
  }

  toggleButton.addEventListener('click', function toggleTokenVisibility() {
    const showing = tokenInput.type === 'text';
    tokenInput.type = showing ? 'password' : 'text';
    toggleButton.setAttribute('aria-pressed', String(!showing));
    toggleButton.textContent = showing ? '显示' : '隐藏';
    tokenInput.focus();
  });

  form.addEventListener('submit', async function savePairing(event) {
    event.preventDefault();
    try {
      await helper.saveToken(storageLocal, tokenInput.value);
      showStatus('配对密钥已保存，正在检查连接…', 'success');
      await testConnection();
    } catch (error) {
      showStatus(error && error.message ? error.message : '无法保存配对密钥', 'error');
    }
  });

  testButton.addEventListener('click', function onTestConnection() {
    void testConnection();
  });

  platformToggle.addEventListener('change', function changePlatformCompatibility() {
    if (platformToggle.checked) platformController.requestEnable();
    else void platformController.disable();
  });

  platformCancel.addEventListener('click', function cancelPlatformEnable() {
    platformController.cancelEnable();
  });

  platformConfirm.addEventListener('click', function confirmPlatformEnable() {
    void platformController.confirmEnable();
  });

  platformDialog.addEventListener('cancel', function cancelPlatformDialog(event) {
    event.preventDefault();
    platformController.cancelEnable();
  });

  void helper.readToken(storageLocal).then((token) => {
    tokenInput.value = token;
  }, () => {
    showStatus('无法读取已保存的配对密钥。', 'error');
  });
  void platformController.load();
}());
