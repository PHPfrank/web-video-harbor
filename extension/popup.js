'use strict';

(function startPopup() {
  const viewState = globalThis.VideoGrabberPopupState;
  const helperApi = globalThis.VideoGrabberHelper;
  const controllerApi = globalThis.VideoGrabberPopupController;
  const platformApi = globalThis.VideoGrabberPlatform;
  if (!viewState || !helperApi || !controllerApi || !platformApi || typeof chrome === 'undefined') return;

  const helper = helperApi.createHelperClient({ storageLocal: chrome.storage.local });
  const elements = {
    connectionPanel: document.querySelector('.connection-panel'),
    connectionTitle: document.getElementById('connection-title'),
    connectionDetail: document.getElementById('connection-detail'),
    notice: document.getElementById('notice'),
    candidateList: document.getElementById('candidate-list'),
    candidateEmpty: document.getElementById('candidate-empty'),
    candidateEmptyText: document.getElementById('candidate-empty-text'),
    taskList: document.getElementById('task-list'),
    taskEmpty: document.getElementById('task-empty'),
    rescanButton: document.getElementById('rescan-button'),
    optionsButton: document.getElementById('options-button'),
  };
  const candidateURLs = new WeakMap();
  const taskIDs = new WeakMap();
  let activeTabId = null;
  let controller = null;

  function makeElement(tagName, className, text) {
    const node = document.createElement(tagName);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = String(text);
    return node;
  }

  function chromeCall(target, method, argument) {
    return new Promise((resolve, reject) => {
      let settled = false;
      function finish(value) {
        if (settled) return;
        settled = true;
        if (chrome.runtime.lastError) reject(new Error('浏览器扩展暂时无法完成此操作'));
        else resolve(value);
      }
      try {
        const returned = target[method](argument, finish);
        if (returned && typeof returned.then === 'function') returned.then(finish, () => {
          reject(new Error('浏览器扩展暂时无法完成此操作'));
        });
      } catch (_error) {
        reject(new Error('浏览器扩展暂时无法完成此操作'));
      }
    });
  }

  function runtimeMessage(message) {
    return chromeCall(chrome.runtime, 'sendMessage', message);
  }

  async function currentTab() {
    const tabs = await chromeCall(chrome.tabs, 'query', { active: true, currentWindow: true });
    return Array.isArray(tabs) && tabs.length ? tabs[0] : null;
  }

  const bridge = {
    async getTabMedia() {
      const tab = await currentTab();
      if (!tab || !Number.isInteger(tab.id)) throw new Error('无法读取当前标签页');
      activeTabId = tab.id;
      const response = await runtimeMessage({ type: 'GET_TAB_MEDIA', tabId: activeTabId });
      if (!response || !response.ok) throw new Error('无法读取页面中的视频');
      const candidates = Array.isArray(response.candidates) ? response.candidates : [];
      const platformCandidate = platformApi.candidateForPage({ url: tab.url, title: tab.title });
      const combinedCandidates = platformCandidate
        && !candidates.some((candidate) => candidate && candidate.url === platformCandidate.url)
        ? [platformCandidate, ...candidates] : candidates;
      return {
        pageUrl: response.pageUrl || tab.url || '',
        candidates: combinedCandidates,
      };
    },
    async rescan() {
      if (!Number.isInteger(activeTabId)) throw new Error('无法读取当前标签页');
      const response = await runtimeMessage({ type: 'RESCAN', tabId: activeTabId });
      if (!response || !response.ok) throw new Error(response && response.error ? response.error : '页面扫描器不可用');
      return response;
    },
  };

  function renderCandidate(candidate) {
    const card = makeElement('article', 'candidate-card');
    if (candidate.kind === 'platform') card.classList.add('candidate-card-platform');
    card.dataset.kind = candidate.kind;
    candidateURLs.set(card, candidate.url);
    card.setAttribute('aria-busy', String(candidate.pending));
    const top = makeElement('div', 'card-top');
    const copy = makeElement('div');
    copy.append(makeElement('h3', 'card-title', candidate.title));
    copy.append(makeElement('p', 'card-detail', candidate.error || candidate.detail));
    if (candidate.blockedReason) {
      copy.append(makeElement('p', 'card-disabled-detail', candidate.blockedReason));
    }
    top.append(copy, makeElement('span', 'format-badge', candidate.typeLabel));
    card.append(top);

    const actions = makeElement('div', 'candidate-actions');
    if (candidate.kind === 'platform') {
      const select = makeElement('select', 'quality-select platform-quality-select');
      select.dataset.control = 'quality';
      select.dataset.choice = 'platform';
      select.setAttribute('aria-label', `选择平台视频画质：${candidate.title}`);
      select.disabled = !candidate.canUse || candidate.pending;
      for (const quality of candidate.qualityOptions) {
        const option = makeElement('option', '', quality.label);
        option.value = quality.value;
        select.append(option);
      }
      select.value = candidate.selectedQuality;
      actions.append(select);
    }
    if (candidate.kind === 'hls' && candidate.variants.length) {
      const select = makeElement('select', 'quality-select');
      select.dataset.control = 'quality';
      select.dataset.choice = 'hls';
      select.setAttribute('aria-label', `选择画质：${candidate.title}`);
      select.disabled = !candidate.canUse || candidate.pending;
      for (const variant of candidate.variants) {
        const option = makeElement('option', '', variant.label || '原始画质');
        option.value = variant.url;
        select.append(option);
      }
      if (candidate.selectedVariant) select.value = candidate.selectedVariant;
      actions.append(select);
    }
    if (candidate.kind === 'hls' && !candidate.variants.length) {
      const inspectButton = makeElement('button', 'button button-secondary', candidate.pending ? '检查中…' : '检查画质');
      inspectButton.type = 'button';
      inspectButton.dataset.action = 'inspect';
      inspectButton.dataset.control = 'inspect';
      inspectButton.setAttribute('aria-label', `检查画质：${candidate.title}`);
      inspectButton.disabled = !candidate.canUse || candidate.pending;
      actions.append(inspectButton);
    } else {
      const downloadButton = makeElement('button', 'button button-primary', candidate.pending ? '处理中…' : '下载');
      downloadButton.type = 'button';
      downloadButton.dataset.action = 'download';
      downloadButton.dataset.control = 'download';
      downloadButton.setAttribute('aria-label', `下载：${candidate.title}`);
      downloadButton.disabled = !candidate.canUse || candidate.pending;
      actions.append(downloadButton);
    }
    card.append(actions);
    return card;
  }

  function renderTask(task) {
    const card = makeElement('article', 'task-card');
    taskIDs.set(card, task.id);
    card.setAttribute('aria-busy', String(task.pending));
    const top = makeElement('div', 'card-top');
    const copy = makeElement('div');
    copy.append(makeElement('h3', 'card-title', task.title));
    if (task.detail) copy.append(makeElement('p', 'task-detail', task.detail));
    const status = makeElement('span', 'status-label', task.statusLabel);
    status.dataset.tone = task.tone;
    top.append(copy, status);
    card.append(top);

    if (task.status === 'queued' || task.status === 'downloading' || task.status === 'merging') {
      const track = makeElement('div', 'progress-track');
      track.setAttribute('role', 'progressbar');
      track.setAttribute('aria-label', `${task.title}下载进度`);
      track.setAttribute('aria-valuemin', '0');
      track.setAttribute('aria-valuemax', '100');
      track.setAttribute('aria-valuenow', String(task.progress));
      const bar = makeElement('div', 'progress-bar');
      bar.style.width = `${task.progress}%`;
      track.append(bar);
      card.append(track);
    }

    const footer = makeElement('div', 'task-footer');
    footer.append(makeElement('span', 'progress-text', task.status === 'downloading' ? `${task.progress}%` : ''));
    const actions = [
      [task.canCancel, 'cancel', '取消'],
      [task.canRetry, 'retry', '重试'],
      [task.canReveal, 'reveal', '在 Finder 中显示'],
    ];
    for (const [visible, action, label] of actions) {
      if (!visible) continue;
      const button = makeElement('button', action === 'cancel' ? 'button button-quiet' : 'button button-secondary', label);
      button.type = 'button';
      button.dataset.action = action;
      button.dataset.control = action;
      button.setAttribute('aria-label', `${label}：${task.title}`);
      button.disabled = task.pending;
      footer.append(button);
    }
    card.append(footer);
    return card;
  }

  const renderer = {
    renderStatus(view) {
      elements.connectionPanel.dataset.tone = view.connection.tone;
      viewState.setText(elements.connectionTitle, view.connection.label);
      viewState.setText(elements.connectionDetail, view.connection.detail);
      elements.rescanButton.disabled = view.scanning;
      viewState.setText(elements.rescanButton, view.scanning ? '扫描中…' : '重新扫描');
    },
    renderCandidates(view) {
      const cards = view.candidates.map(renderCandidate);
      elements.candidateList.replaceChildren(...cards);
      elements.candidateEmpty.hidden = cards.length > 0;
      viewState.setText(elements.candidateEmptyText, view.emptyMessage);
      if (view.focusedCandidate) {
        const card = cards.find((item) => candidateURLs.get(item) === view.focusedCandidate.url);
        const control = card && card.querySelector(`[data-control="${view.focusedCandidate.control}"]`);
        if (control && !control.disabled) control.focus({ preventScroll: true });
      }
    },
    renderTasks(view) {
      const cards = view.tasks.map(renderTask);
      elements.taskList.replaceChildren(...cards);
      elements.taskEmpty.hidden = cards.length > 0;
      if (view.focusedTask) {
        const card = cards.find((item) => taskIDs.get(item) === view.focusedTask.id);
        const control = card && card.querySelector(`[data-control="${view.focusedTask.action}"]`);
        if (control && !control.disabled) control.focus({ preventScroll: true });
      }
    },
    setNotice(message, tone) {
      viewState.setText(elements.notice, message || '');
      elements.notice.dataset.tone = tone || '';
    },
  };

  controller = controllerApi.createPopupController({
    helper,
    bridge,
    renderer,
    viewState,
    platformQualityOptions: platformApi.QUALITY_OPTIONS,
  });

  elements.candidateList.addEventListener('change', function onQualityChange(event) {
    const select = event.target.closest('select[data-control="quality"]');
    const card = select && select.closest('.candidate-card');
    const url = card && candidateURLs.get(card);
    if (select && url) {
      if (select.dataset.choice === 'platform') controller.selectQuality(url, select.value);
      else controller.selectVariant(url, select.value);
    }
  });

  elements.candidateList.addEventListener('focusin', function onCandidateFocus(event) {
    const control = event.target.closest('[data-control]');
    const card = control && control.closest('.candidate-card');
    const url = card && candidateURLs.get(card);
    if (control && url) controller.focusCandidate(url, control.dataset.control);
  });

  elements.candidateList.addEventListener('click', function onCandidateAction(event) {
    const button = event.target.closest('button[data-action]');
    const card = button && button.closest('.candidate-card');
    const url = card && candidateURLs.get(card);
    if (!button || !url) return;
    const operation = button.dataset.action === 'inspect'
      ? controller.inspectCandidate(url) : controller.downloadCandidate(url);
    void operation;
  });

  elements.taskList.addEventListener('focusin', function onTaskFocus(event) {
    const control = event.target.closest('[data-control]');
    const card = control && control.closest('.task-card');
    const id = card && taskIDs.get(card);
    if (control && id) controller.focusTask(id, control.dataset.control);
  });

  elements.taskList.addEventListener('click', function onTaskAction(event) {
    const button = event.target.closest('button[data-action]');
    const card = button && button.closest('.task-card');
    const id = card && taskIDs.get(card);
    if (!button || !id) return;
    void controller.taskAction(id, button.dataset.action);
  });

  elements.rescanButton.addEventListener('click', function onRescan() {
    void controller.rescan().catch((error) => renderer.setNotice(error && error.message ? error.message : '无法重新扫描'));
  });
  elements.optionsButton.addEventListener('click', function openOptions() { void chrome.runtime.openOptionsPage(); });
  window.addEventListener('pagehide', function stopOnPageHide() { controller.stop(); }, { once: true });
  window.addEventListener('unload', function stopOnUnload() { controller.stop(); }, { once: true });
  void controller.start();
}());
