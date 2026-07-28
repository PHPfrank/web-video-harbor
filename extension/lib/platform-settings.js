(function initPlatformSettings(root, factory) {
  'use strict';

  const api = factory();
  root.VideoGrabberPlatformSettings = api;
  if (typeof module === 'object' && module.exports) module.exports = api;
}(typeof globalThis !== 'undefined' ? globalThis : this, function createPlatformSettingsApi() {
  'use strict';

  const ERROR_MESSAGES = {
    missing_token: '请先完成本地助手配对',
    unauthorized: '配对密钥无效，请重新配对',
    connection_failed: '无法连接本地助手',
    timeout: '本地助手响应超时',
    platform_compatibility_disabled: '实验性平台兼容尚未开启',
    invalid_acknowledgment: '请先阅读并确认使用边界',
    notice_outdated: '使用提示已更新，请重新阅读后确认',
    settings_unavailable: '无法保存本地设置',
  };

  function safeErrorMessage(error, fallback) {
    const code = error && typeof error.code === 'string' ? error.code : '';
    return ERROR_MESSAGES[code] || fallback;
  }

  function createPlatformSettingsController(options) {
    const settings = options || {};
    const client = settings.client;
    const view = settings.view;
    if (!client || typeof client.getSettings !== 'function'
      || typeof client.setPlatformCompatibility !== 'function') {
      throw new TypeError('A platform settings client is required');
    }
    if (!view || typeof view.render !== 'function') {
      throw new TypeError('A platform settings view is required');
    }

    const model = {
      status: 'loading',
      enabled: false,
      available: false,
      busy: false,
      pendingAction: '',
      noticeVersion: '',
      error: '',
    };
    let authoritative = null;
    let loadRevision = 0;
    let intentRevision = 0;
    let mutationQueue = Promise.resolve();
    let activeIntent = null;

    function render() {
      view.render({ ...model });
    }

    function applySettings(value) {
      const next = value && typeof value === 'object' ? value : {};
      const noticeVersion = typeof next.currentPlatformNoticeVersion === 'string'
        ? next.currentPlatformNoticeVersion : '';
      const available = noticeVersion !== '';
      const enabled = available && next.experimentalPlatformCompatibilityEnabled === true;
      authoritative = {
        experimentalPlatformCompatibilityEnabled: enabled,
        platformNoticeVersion: enabled && typeof next.platformNoticeVersion === 'string'
          ? next.platformNoticeVersion : '',
        currentPlatformNoticeVersion: noticeVersion,
      };
      model.available = available;
      model.enabled = enabled;
      model.noticeVersion = noticeVersion;
      model.status = available ? (enabled ? 'enabled' : 'disabled') : 'unavailable';
      model.busy = false;
      model.pendingAction = '';
    }

    function restoreAuthoritative() {
      if (authoritative) applySettings(authoritative);
      else {
        model.status = 'unavailable';
        model.enabled = false;
        model.available = false;
        model.busy = false;
        model.pendingAction = '';
        model.noticeVersion = '';
      }
    }

    function load() {
      const revision = ++loadRevision;
      const startedIntentRevision = intentRevision;
      if (!activeIntent) {
        model.status = 'loading';
        model.enabled = false;
        model.available = false;
        model.busy = false;
        model.pendingAction = '';
        model.noticeVersion = '';
      }
      model.error = '';
      render();
      return Promise.resolve().then(() => client.getSettings()).then((result) => {
        if (revision !== loadRevision || startedIntentRevision !== intentRevision) return;
        applySettings(result);
        model.error = '';
        render();
      }, (error) => {
        if (revision !== loadRevision || startedIntentRevision !== intentRevision) return;
        authoritative = null;
        restoreAuthoritative();
        model.error = safeErrorMessage(error, '无法读取平台兼容设置');
        render();
      });
    }

    function requestEnable() {
      if (!model.available || model.enabled || model.busy) return false;
      model.error = '';
      render();
      if (typeof view.showNotice === 'function') view.showNotice();
      return true;
    }

    function cancelEnable() {
      if (typeof view.closeNotice === 'function') view.closeNotice();
      restoreAuthoritative();
      model.error = '';
      render();
    }

    function queueMutation(type, input, optimisticEnabled) {
      if (activeIntent && activeIntent.type === type) return activeIntent.promise;
      const revision = ++intentRevision;
      loadRevision += 1;
      const previousAuthoritative = authoritative;
      model.error = '';
      model.busy = true;
      model.pendingAction = type;
      if (optimisticEnabled === false) {
        model.enabled = false;
        model.status = 'disabled';
      }
      render();

      const intent = { type, promise: null };
      const operation = mutationQueue.then(() => client.setPlatformCompatibility(input));
      const completion = operation.then((result) => {
        if (revision !== intentRevision) return;
        loadRevision += 1;
        applySettings(result);
        model.error = '';
        render();
      }, (error) => {
        if (revision !== intentRevision) return;
        loadRevision += 1;
        authoritative = previousAuthoritative;
        restoreAuthoritative();
        model.error = safeErrorMessage(error, '无法保存平台兼容设置');
        render();
      }).finally(() => {
        if (activeIntent === intent) activeIntent = null;
      });
      intent.promise = completion;
      activeIntent = intent;
      mutationQueue = completion.catch(() => {});
      return completion;
    }

    function confirmEnable() {
      if (activeIntent && activeIntent.type === 'enable') return activeIntent.promise;
      if (typeof view.closeNotice === 'function') view.closeNotice();
      if (!model.available || model.enabled || model.busy || !model.noticeVersion) {
        return Promise.resolve();
      }
      return queueMutation('enable', {
        enabled: true,
        acknowledged: true,
        noticeVersion: model.noticeVersion,
      });
    }

    function disable() {
      if (activeIntent && activeIntent.type === 'disable') return activeIntent.promise;
      if (typeof view.closeNotice === 'function') view.closeNotice();
      const enablePending = activeIntent && activeIntent.type === 'enable';
      if (!enablePending && (!authoritative || !authoritative.experimentalPlatformCompatibilityEnabled)) {
        restoreAuthoritative();
        model.error = '';
        render();
        return Promise.resolve();
      }
      return queueMutation('disable', { enabled: false }, false);
    }

    return {
      load,
      requestEnable,
      cancelEnable,
      confirmEnable,
      disable,
    };
  }

  return { createPlatformSettingsController };
}));
