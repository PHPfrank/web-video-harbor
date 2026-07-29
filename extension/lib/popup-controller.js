(function initPopupController(root, factory) {
  'use strict';

  const api = factory();
  root.VideoGrabberPopupController = api;
  if (typeof module === 'object' && module.exports) module.exports = api;
}(typeof globalThis !== 'undefined' ? globalThis : this, function createControllerApi() {
  'use strict';

  function createPopupController(options) {
    const settings = options || {};
    const helper = settings.helper;
    const bridge = settings.bridge;
    const renderer = settings.renderer;
    const viewState = settings.viewState;
    const defaultPlatformQualityOptions = [
      { value: 'best', label: '最佳画质' },
      { value: '1080', label: '1080P' },
      { value: '720', label: '720P' },
    ];
    const configuredQualityOptions = Array.isArray(settings.platformQualityOptions)
      ? settings.platformQualityOptions : [];
    const platformQualityOptions = defaultPlatformQualityOptions.map((fallback, index) => {
      const configured = configuredQualityOptions[index];
      return configured && configured.value === fallback.value && typeof configured.label === 'string'
        ? { value: fallback.value, label: configured.label }
        : { ...fallback };
    });
    const scheduler = settings.scheduler || {
      setTimeout: globalThis.setTimeout.bind(globalThis),
      clearTimeout: globalThis.clearTimeout.bind(globalThis),
    };
    const connectedPollMs = settings.connectedPollMs || 1200;
    const disconnectedPollMs = settings.disconnectedPollMs || 5000;
    const model = {
      connection: 'connecting',
      ffmpegAvailable: null,
      platformDownloaderAvailable: null,
      javascriptRuntimeAvailable: null,
      scanning: false,
      candidates: [],
      tasks: [],
      pageUrl: '',
      experimentalPlatformBlocked: false,
      recommendationHighlighted: false,
    };
    const selectedVariants = new Map();
    const selectedQualities = new Map();
    const candidateOperations = new Map();
    const taskOperations = new Map();
    const pendingCandidates = new Set();
    const pendingTasks = new Set();
    let focusedCandidate = null;
    let focusedTask = null;
    let taskSnapshot = '[]';
    let refreshInFlight = null;
    let pollCycle = null;
    let pollTimer = null;
    let stopped = false;

    function capabilityKey() {
      return `${model.connection}:${String(model.ffmpegAvailable)}:${String(model.platformDownloaderAvailable)}:${String(model.javascriptRuntimeAvailable)}`;
    }

    function candidateModels() {
      return model.candidates.map((candidate) => {
        const hlsBlocked = candidate.kind === 'hls' && model.ffmpegAvailable === false;
        const platformParserBlocked = candidate.kind === 'platform'
          && model.platformDownloaderAvailable === false;
        const platformRuntimeBlocked = candidate.kind === 'platform'
          && model.javascriptRuntimeAvailable === false;
        const platformFFmpegBlocked = candidate.kind === 'platform' && model.ffmpegAvailable === false;
        const variants = viewState.sortHlsVariants(candidate.variants);
        const selected = selectedVariants.get(candidate.url);
        const validSelection = variants.some((variant) => variant.url === selected) ? selected : '';
        const selectedVariant = validSelection || (variants[0] && variants[0].url) || '';
        if (selectedVariant) selectedVariants.set(candidate.url, selectedVariant);
        const quality = selectedQualities.get(candidate.url);
        const selectedQuality = platformQualityOptions.some((option) => option.value === quality)
          ? quality : 'best';
        if (candidate.kind === 'platform') selectedQualities.set(candidate.url, selectedQuality);
        let blockedReason = '';
        if (hlsBlocked) blockedReason = '未安装 FFmpeg，无法处理 M3U8 视频';
        else if (platformParserBlocked) blockedReason = '安装包不完整：缺少平台解析器';
        else if (platformRuntimeBlocked) blockedReason = '安装包不完整：缺少 JavaScript 解析组件';
        else if (platformFFmpegBlocked) blockedReason = '未安装 FFmpeg，无法合并平台视频';
        return {
          ...candidate,
          variants,
          selectedVariant,
          qualityOptions: candidate.kind === 'platform'
            ? platformQualityOptions.map((option) => ({ ...option })) : [],
          selectedQuality: candidate.kind === 'platform' ? selectedQuality : '',
          pending: pendingCandidates.has(candidate.url),
          canUse: model.connection === 'connected' && !hlsBlocked
            && !platformParserBlocked && !platformRuntimeBlocked && !platformFFmpegBlocked,
          blockedReason,
        };
      });
    }

    function taskModels() {
      return model.tasks.map((task) => ({ ...task, pending: pendingTasks.has(task.id) }));
    }

    function buildView() {
      return viewState.buildViewModel({
        ...model,
        candidates: candidateModels(),
        tasks: taskModels(),
      });
    }

    function renderStatus() {
      renderer.renderStatus(buildView());
    }

    function renderCandidates() {
      renderer.renderCandidates({ ...buildView(), focusedCandidate: focusedCandidate ? { ...focusedCandidate } : null });
    }

    function renderTasks() {
      renderer.renderTasks({
        ...buildView(),
        focusedTask: focusedTask ? { ...focusedTask } : null,
        recommendationHighlighted: model.recommendationHighlighted,
      });
    }

    function setNotice(message, tone) {
      if (renderer.setNotice) renderer.setNotice(message || '', tone || '');
    }

    function errorMessage(error, fallback) {
      if (error && error.name === 'HelperClientError' && typeof error.message === 'string'
        && error.message && error.message.length <= 100) return error.message;
      return fallback;
    }

    function findCandidate(url) {
      return model.candidates.find((candidate) => candidate.url === url);
    }

    function replaceTask(task) {
      if (!task || typeof task.id !== 'string') return;
      const index = model.tasks.findIndex((item) => item.id === task.id);
      if (index === -1) model.tasks = [task, ...model.tasks];
      else model.tasks = model.tasks.map((item, itemIndex) => (itemIndex === index ? task : item));
      taskSnapshot = tasksFingerprint(model.tasks);
    }

    function tasksFingerprint(tasks) {
      return JSON.stringify(tasks.map((task) => [
        task && task.id,
        task && task.title,
        task && task.status,
        Number(task && task.progress) || 0,
        task && task.error,
        task && task.errorCode,
      ]));
    }

    function didTaskComplete(previousTasks, nextTasks) {
      const previousStatuses = new Map(
        previousTasks
          .filter((task) => task && typeof task.id === 'string')
          .map((task) => [task.id, task.status]),
      );
      return nextTasks.some((task) => task && task.status === 'completed'
        && previousStatuses.has(task.id)
        && previousStatuses.get(task.id) !== 'completed');
    }

    function refreshCandidates() {
      return Promise.resolve().then(() => bridge.getTabMedia()).then((response) => {
        model.pageUrl = response && response.pageUrl ? response.pageUrl : '';
        model.candidates = response && Array.isArray(response.candidates) ? response.candidates : [];
        model.experimentalPlatformBlocked = Boolean(response && response.experimentalPlatformBlocked);
        const currentURLs = new Set(model.candidates.map((candidate) => candidate.url));
        for (const url of selectedVariants.keys()) {
          if (!currentURLs.has(url)) selectedVariants.delete(url);
        }
        for (const url of selectedQualities.keys()) {
          if (!currentURLs.has(url)) selectedQualities.delete(url);
        }
        if (focusedCandidate && !currentURLs.has(focusedCandidate.url)) focusedCandidate = null;
        renderCandidates();
        return response;
      });
    }

    function refreshTasks() {
      if (refreshInFlight) return refreshInFlight;
      const previousCapability = capabilityKey();
      refreshInFlight = (async () => {
        let tasksChanged = false;
        try {
          const health = await helper.health();
          model.ffmpegAvailable = Boolean(health && health.ffmpeg);
          model.platformDownloaderAvailable = Boolean(health && health.platformDownloader
            && health.platformDownloader.available);
          model.javascriptRuntimeAvailable = Boolean(health && health.javascriptRuntime
            && health.javascriptRuntime.available);
          const tasks = await helper.listTasks();
          model.connection = 'connected';
          const nextTasks = Array.isArray(tasks) ? tasks : [];
          const nextSnapshot = tasksFingerprint(nextTasks);
          if (nextSnapshot !== taskSnapshot) {
            if (!model.recommendationHighlighted && didTaskComplete(model.tasks, nextTasks)) {
              model.recommendationHighlighted = true;
            }
            model.tasks = nextTasks;
            taskSnapshot = nextSnapshot;
            tasksChanged = true;
            if (focusedTask && !model.tasks.some((task) => task.id === focusedTask.id)) focusedTask = null;
          }
        } catch (error) {
          model.connection = error && (error.code === 'missing_token' || error.code === 'unauthorized')
            ? 'disconnected' : 'error';
        }
        renderStatus();
        if (tasksChanged) renderTasks();
        if (previousCapability !== capabilityKey()) renderCandidates();
        return model.tasks;
      })().finally(() => {
        refreshInFlight = null;
      });
      return refreshInFlight;
    }

    function scheduleNextPoll() {
      if (stopped || pollTimer !== null) return;
      const delay = model.connection === 'connected' ? connectedPollMs : disconnectedPollMs;
      pollTimer = scheduler.setTimeout(() => {
        pollTimer = null;
        return startPolling();
      }, delay);
    }

    function startPolling() {
      if (pollCycle) return pollCycle;
      if (stopped) return Promise.resolve();
      if (pollTimer !== null) {
        scheduler.clearTimeout(pollTimer);
        pollTimer = null;
      }
      pollCycle = refreshTasks().finally(() => {
        pollCycle = null;
        scheduleNextPoll();
      });
      return pollCycle;
    }

    function stop() {
      stopped = true;
      if (pollTimer !== null) scheduler.clearTimeout(pollTimer);
      pollTimer = null;
      if (helper && typeof helper.abortAll === 'function') helper.abortAll();
    }

    function selectVariant(candidateURL, variantURL) {
      const candidate = findCandidate(candidateURL);
      if (!candidate || candidate.kind !== 'hls') return false;
      const variants = viewState.sortHlsVariants(candidate.variants);
      if (!variants.some((variant) => variant.url === variantURL)) return false;
      selectedVariants.set(candidateURL, variantURL);
      return true;
    }

    function selectQuality(candidateURL, quality) {
      const candidate = findCandidate(candidateURL);
      if (!candidate || candidate.kind !== 'platform') return false;
      if (!platformQualityOptions.some((option) => option.value === quality)) return false;
      selectedQualities.set(candidateURL, quality);
      return true;
    }

    function focusCandidate(candidateURL, control) {
      if (!findCandidate(candidateURL)) return;
      focusedCandidate = { url: candidateURL, control };
    }

    function focusTask(id, action) {
      if (!model.tasks.some((task) => task.id === id)) return;
      focusedTask = { id, action };
    }

    function unavailableForCandidate(candidate) {
      if (model.connection !== 'connected') {
        setNotice('本地助手尚未连接');
        return true;
      }
      if (candidate.kind === 'hls' && model.ffmpegAvailable === false) {
        setNotice('未安装 FFmpeg，无法下载 M3U8 视频');
        return true;
      }
      if (candidate.kind === 'platform' && model.platformDownloaderAvailable === false) {
        setNotice('安装包不完整：缺少平台解析器');
        return true;
      }
      if (candidate.kind === 'platform' && model.javascriptRuntimeAvailable === false) {
        setNotice('安装包不完整：缺少 JavaScript 解析组件');
        return true;
      }
      if (candidate.kind === 'platform' && model.ffmpegAvailable === false) {
        setNotice('未安装 FFmpeg，无法合并平台视频');
        return true;
      }
      return false;
    }

    function runCandidateOperation(url, operation) {
      if (candidateOperations.has(url)) return candidateOperations.get(url);
      pendingCandidates.add(url);
      renderCandidates();
      let started;
      try {
        started = operation();
      } catch (error) {
        started = Promise.reject(error);
      }
      const promise = Promise.resolve(started).finally(() => {
        candidateOperations.delete(url);
        pendingCandidates.delete(url);
        renderCandidates();
      });
      candidateOperations.set(url, promise);
      return promise;
    }

    function inspectCandidate(url) {
      const candidate = findCandidate(url);
      if (!candidate || candidate.kind === 'platform' || unavailableForCandidate(candidate)) {
        return Promise.resolve(null);
      }
      return runCandidateOperation(url, async () => {
        candidate.inspecting = true;
        candidate.error = '';
        try {
          const inspection = await helper.inspect(candidate.url);
          Object.assign(candidate, viewState.applyInspection(candidate, inspection));
          const firstVariant = candidate.variants && candidate.variants[0];
          if (firstVariant) selectedVariants.set(candidate.url, firstVariant.url);
          return candidate;
        } catch (error) {
          candidate.error = errorMessage(error, '无法检查视频画质');
          setNotice(candidate.error);
          return null;
        } finally {
          candidate.inspecting = false;
        }
      });
    }

    function downloadCandidate(url) {
      const candidate = findCandidate(url);
      if (!candidate || unavailableForCandidate(candidate)) return Promise.resolve(null);
      let sourceURL = candidate.url;
      if (candidate.kind === 'hls') {
        const variants = viewState.sortHlsVariants(candidate.variants);
        sourceURL = selectedVariants.get(candidate.url) || (variants[0] && variants[0].url) || '';
        if (!sourceURL) {
          setNotice('请先检查可用画质');
          return Promise.resolve(null);
        }
      }
      return runCandidateOperation(url, async () => {
        try {
          const spec = {
            url: sourceURL,
            title: candidate.title || '未命名视频',
            mediaType: candidate.kind,
            pageUrl: candidate.pageUrl || model.pageUrl,
          };
          if (candidate.kind === 'platform') {
            const quality = selectedQualities.get(candidate.url);
            spec.quality = platformQualityOptions.some((option) => option.value === quality)
              ? quality : 'best';
          }
          const task = await helper.createTask(spec);
          replaceTask(task);
          renderTasks();
          setNotice('已添加到本地下载队列。', 'success');
          return task;
        } catch (error) {
          const message = errorMessage(error, '无法创建下载任务');
          candidate.error = message;
          setNotice(message);
          return null;
        }
      });
    }

    function runTaskOperation(id, operation) {
      if (taskOperations.has(id)) return taskOperations.get(id);
      pendingTasks.add(id);
      renderTasks();
      let started;
      try {
        started = operation();
      } catch (error) {
        started = Promise.reject(error);
      }
      const promise = Promise.resolve(started).catch((error) => {
        setNotice(errorMessage(error, '无法操作该任务'));
        return null;
      }).finally(() => {
        taskOperations.delete(id);
        pendingTasks.delete(id);
        renderTasks();
      });
      taskOperations.set(id, promise);
      return promise;
    }

    function taskAction(id, action) {
      return runTaskOperation(id, async () => {
        let result;
        if (action === 'cancel') result = await helper.cancelTask(id);
        else if (action === 'retry') result = await helper.retryTask(id);
        else if (action === 'reveal') {
          result = await helper.revealTask(id);
          setNotice('已在 Finder 中显示文件。', 'success');
        } else {
          throw new Error('不支持的任务操作');
        }
        if (action === 'retry') replaceTask(result);
        else if (action !== 'reveal') replaceTask(result);
        renderTasks();
        return result;
      });
    }

    function rescan() {
      model.scanning = true;
      renderStatus();
      return Promise.resolve().then(() => bridge.rescan()).then(() => refreshCandidates()).finally(() => {
        model.scanning = false;
        renderStatus();
      });
    }

    function start() {
      stopped = false;
      renderStatus();
      return Promise.allSettled([refreshCandidates(), startPolling()]);
    }

    function snapshot() {
      return {
        ...model,
        candidates: candidateModels(),
        tasks: taskModels(),
        focusedCandidate: focusedCandidate ? { ...focusedCandidate } : null,
      };
    }

    return {
      downloadCandidate,
      focusCandidate,
      focusTask,
      inspectCandidate,
      refreshCandidates,
      refreshTasks,
      rescan,
      selectQuality,
      selectVariant,
      snapshot,
      start,
      startPolling,
      stop,
      taskAction,
    };
  }

  return { createPopupController };
}));
