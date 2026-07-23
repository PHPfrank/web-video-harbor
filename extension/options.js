'use strict';

(function startOptions() {
  const helper = globalThis.VideoGrabberHelper;
  if (!helper || typeof chrome === 'undefined' || !chrome.storage || !chrome.storage.local) return;

  const storageLocal = chrome.storage.local;
  const client = helper.createHelperClient({ storageLocal });
  const form = document.getElementById('pairing-form');
  const tokenInput = document.getElementById('token-input');
  const toggleButton = document.getElementById('toggle-token');
  const testButton = document.getElementById('test-button');
  const status = document.getElementById('settings-status');

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

  void helper.readToken(storageLocal).then((token) => {
    tokenInput.value = token;
  }, () => {
    showStatus('无法读取已保存的配对密钥。', 'error');
  });
}());
