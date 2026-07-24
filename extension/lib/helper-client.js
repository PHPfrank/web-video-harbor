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
  const HEALTH_VERSION_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$/;
  const ERROR_MESSAGES = {
    unauthorized: '配对密钥无效，请重新配对',
    unsafe_source: '视频地址不安全或无效',
    http_status: '视频服务器拒绝了请求',
    unsupported_media: '未识别到支持的视频格式',
    invalid_manifest: '视频清单格式无效',
    encrypted_hls: '不支持加密或 DRM 视频',
    response_too_large: '视频清单过大',
    network: '无法读取视频地址',
    invalid_request: '下载请求参数无效',
    not_found: '未找到该下载任务',
    invalid_state: '任务当前状态不允许此操作',
    task_error: '本地助手无法执行该任务',
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

  function describeHealth(health) {
    if (health && health.ready === true && health.ffmpeg === false) {
      return { message: '助手已连接，但未安装 FFmpeg', tone: 'error' };
    }
    return { message: '连接成功，本地助手可以使用。', tone: 'success' };
  }

  function safeHealthVersion(value) {
    return typeof value === 'string' && HEALTH_VERSION_PATTERN.test(value) ? value : '';
  }

  function normalizePlatformDownloader(value) {
    if (!value || typeof value !== 'object' || Array.isArray(value)) {
      return { available: false, version: '' };
    }
    const version = safeHealthVersion(value.version);
    if (value.available !== true || !version) {
      return { available: false, version: '' };
    }
    return { available: true, version };
  }

  function normalizeHealth(value) {
    const health = value && typeof value === 'object' && !Array.isArray(value) ? value : {};
    return {
      ready: health.ready === true,
      version: safeHealthVersion(health.version),
      ffmpeg: health.ffmpeg === true,
      pid: Number.isSafeInteger(health.pid) && health.pid > 1 ? health.pid : 0,
      platformDownloader: normalizePlatformDownloader(health.platformDownloader),
    };
  }

  function createHelperClient(options) {
    const settings = options || {};
    const fetchImpl = settings.fetchImpl || (typeof fetch === 'function' ? fetch.bind(globalThis) : null);
    const storageLocal = settings.storageLocal;
    const timeoutMs = Number.isFinite(settings.timeoutMs) && settings.timeoutMs > 0
      ? settings.timeoutMs : DEFAULT_TIMEOUT_MS;
    const activeControllers = new Set();

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
      let timedOut = false;
      activeControllers.add(controller);
      const timer = setTimeout(() => {
        timedOut = true;
        controller.abort();
      }, timeoutMs);
      try {
        const response = await fetchImpl(`${BASE_URL}${path}`, {
          method: config.method || 'GET',
          headers,
          body,
          signal: controller.signal,
          cache: 'no-store',
        });
        let result = null;
        try {
          result = await response.json();
        } catch (error) {
          if (controller.signal.aborted) throw error;
          if (response.ok) throw new HelperClientError('本地助手返回了无效数据', 'invalid_response');
        }
        if (!response.ok) throw errorForResponse(response.status, result);
        return result;
      } catch (error) {
        if (error instanceof HelperClientError) throw error;
        if (controller.signal.aborted && timedOut) {
          throw new HelperClientError('本地助手响应超时', 'timeout');
        }
        if (controller.signal.aborted) throw new HelperClientError('操作已取消', 'aborted');
        throw new HelperClientError('无法连接本地助手', 'connection_failed');
      } finally {
        clearTimeout(timer);
        activeControllers.delete(controller);
      }
    }

    function taskAction(id, action) {
      return request(`/v1/tasks/${encodeURIComponent(id)}/${action}`, { method: 'POST', authenticated: true });
    }

    return {
      async health() { return normalizeHealth(await request('/health')); },
      inspect(url) { return request('/v1/inspect', { method: 'POST', authenticated: true, body: { url } }); },
      listTasks() { return request('/v1/tasks', { authenticated: true }); },
      createTask(spec) { return request('/v1/tasks', { method: 'POST', authenticated: true, body: spec }); },
      getTask(id) { return request(`/v1/tasks/${encodeURIComponent(id)}`, { authenticated: true }); },
      cancelTask(id) { return taskAction(id, 'cancel'); },
      retryTask(id) { return taskAction(id, 'retry'); },
      revealTask(id) { return taskAction(id, 'reveal'); },
      abortAll() {
        for (const controller of activeControllers) controller.abort();
      },
    };
  }

  return {
    BASE_URL,
    TOKEN_KEY,
    HelperClientError,
    createHelperClient,
    describeHealth,
    readToken,
    saveToken,
  };
}));
