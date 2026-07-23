(function initHelperClient(root, factory) {
  'use strict';

  const api = factory();
  root.VideoGrabberHelper = api;
  if (typeof module === 'object' && module.exports) module.exports = api;
}(typeof globalThis !== 'undefined' ? globalThis : this, function createHelperApi() {
  'use strict';

  const BASE_URL = 'http://127.0.0.1:17432';
  const TOKEN_KEY = 'videoHelperToken';
  const DEFAULT_TIMEOUT_MS = 8000;
  const ERROR_MESSAGES = {
    unauthorized: '配对密钥无效，请重新配对',
    unsafe_source: '视频地址不安全或无效',
    unsupported_media: '未识别到支持的视频格式',
    invalid_manifest: '视频清单格式无效',
    encrypted_hls: '不支持加密或 DRM 视频',
    network: '无法读取视频地址',
    not_found: '未找到该下载任务',
    invalid_state: '任务当前状态不允许此操作',
    not_revealable: '任务文件尚不可显示',
    reveal_failed: '无法在 Finder 中显示文件',
  };

  class HelperClientError extends Error {
    constructor(message, code) {
      super(message);
      this.name = 'HelperClientError';
      this.code = code || 'helper_error';
    }
  }

  function storageCall(storageLocal, method, value) {
    return new Promise((resolve, reject) => {
      const failureMessage = method === 'get'
        ? '无法读取本地配对信息' : '无法保存本地配对信息';
      if (!storageLocal || typeof storageLocal[method] !== 'function') {
        reject(new HelperClientError(failureMessage, 'storage_unavailable'));
        return;
      }
      let settled = false;
      function finish(result) {
        if (settled) return;
        if (typeof chrome !== 'undefined' && chrome.runtime && chrome.runtime.lastError) {
          fail();
          return;
        }
        settled = true;
        resolve(result);
      }
      function fail() {
        if (settled) return;
        settled = true;
        reject(new HelperClientError(failureMessage, 'storage_unavailable'));
      }
      try {
        const returned = storageLocal[method](value, finish);
        if (returned && typeof returned.then === 'function') returned.then(finish, fail);
      } catch (_error) {
        fail();
      }
    });
  }

  async function readToken(storageLocal) {
    const result = await storageCall(storageLocal, 'get', TOKEN_KEY);
    const token = result && typeof result[TOKEN_KEY] === 'string' ? result[TOKEN_KEY].trim() : '';
    return token;
  }

  async function saveToken(storageLocal, rawToken) {
    const token = typeof rawToken === 'string' ? rawToken.trim() : '';
    if (!token) throw new HelperClientError('请输入配对密钥', 'missing_token');
    await storageCall(storageLocal, 'set', { [TOKEN_KEY]: token });
  }

  function errorForResponse(status, body) {
    const code = body && typeof body.code === 'string' ? body.code : '';
    if (ERROR_MESSAGES[code]) return new HelperClientError(ERROR_MESSAGES[code], code);
    if (status === 401) return new HelperClientError(ERROR_MESSAGES.unauthorized, 'unauthorized');
    return new HelperClientError('本地助手暂时无法完成此操作', 'helper_error');
  }

  function createHelperClient(options) {
    const settings = options || {};
    const fetchImpl = settings.fetchImpl || (typeof fetch === 'function' ? fetch.bind(globalThis) : null);
    const storageLocal = settings.storageLocal;
    const timeoutMs = Number.isFinite(settings.timeoutMs) && settings.timeoutMs > 0
      ? settings.timeoutMs : DEFAULT_TIMEOUT_MS;

    async function request(path, requestOptions) {
      if (!fetchImpl) throw new HelperClientError('当前浏览器无法访问本地助手', 'fetch_unavailable');
      const config = requestOptions || {};
      const headers = { Accept: 'application/json' };
      if (config.authenticated) {
        const token = await readToken(storageLocal);
        if (!token) throw new HelperClientError('请先完成本地助手配对', 'missing_token');
        headers['X-Video-Helper-Token'] = token;
      }
      let body;
      if (config.body !== undefined) {
        headers['Content-Type'] = 'application/json';
        body = JSON.stringify(config.body);
      }
      const controller = new AbortController();
      const timer = setTimeout(() => controller.abort(), timeoutMs);
      let response;
      try {
        response = await fetchImpl(`${BASE_URL}${path}`, {
          method: config.method || 'GET',
          headers,
          body,
          signal: controller.signal,
          cache: 'no-store',
        });
      } catch (_error) {
        if (controller.signal.aborted) {
          throw new HelperClientError('本地助手响应超时', 'timeout');
        }
        throw new HelperClientError('无法连接本地助手', 'connection_failed');
      } finally {
        clearTimeout(timer);
      }
      let result = null;
      try {
        result = await response.json();
      } catch (_error) {
        if (response.ok) throw new HelperClientError('本地助手返回了无效数据', 'invalid_response');
      }
      if (!response.ok) throw errorForResponse(response.status, result);
      return result;
    }

    function taskAction(id, action) {
      return request(`/v1/tasks/${encodeURIComponent(id)}/${action}`, { method: 'POST', authenticated: true });
    }

    return {
      health() { return request('/health'); },
      inspect(url) { return request('/v1/inspect', { method: 'POST', authenticated: true, body: { url } }); },
      listTasks() { return request('/v1/tasks', { authenticated: true }); },
      createTask(spec) { return request('/v1/tasks', { method: 'POST', authenticated: true, body: spec }); },
      getTask(id) { return request(`/v1/tasks/${encodeURIComponent(id)}`, { authenticated: true }); },
      cancelTask(id) { return taskAction(id, 'cancel'); },
      retryTask(id) { return taskAction(id, 'retry'); },
      revealTask(id) { return taskAction(id, 'reveal'); },
    };
  }

  return { BASE_URL, TOKEN_KEY, HelperClientError, createHelperClient, readToken, saveToken };
}));
