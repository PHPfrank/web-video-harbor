'use strict';

(function startPopup() {
  const viewState = globalThis.VideoGrabberPopupState;
  const helper = globalThis.VideoGrabberHelper;
  if (!viewState || !helper || typeof chrome === 'undefined') return;

  const client = helper.createHelperClient({ storageLocal: chrome.storage.local });
  const model = {
    connection: 'connecting',
    scanning: false,
    candidates: [],
    tasks: [],
    pageUrl: '',
  };
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
  let activeTabId = null;
  let pollTimer = null;

  function makeElement(tagName, className, text) {
    const node = document.createElement(tagName);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = String(text);
    return node;
  }

  function setNotice(message, tone) {
    viewState.setText(elements.notice, message || '');
    elements.notice.dataset.tone = tone || '';
  }

  function chromeCall(target, method, argument) {
    return new Promise((resolve, reject) => {
      let settled = false;
      function finish(value) {
        if (settled) return;
        settled = true;
        const runtimeError = chrome.runtime.lastError;
        if (runtimeError) reject(new Error('浏览器扩展暂时无法完成此操作'));
        else resolve(value);
      }
      try {
        const returned = target[method](argument, finish);
        if (returned && typeof returned.then === 'function') returned.then(finish, reject);
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

  function renderCandidate(candidate, index, canDownload) {
    const card = makeElement('article', 'candidate-card');
    card.dataset.index = String(index);
    const top = makeElement('div', 'card-top');
    const copy = makeElement('div');
    copy.append(makeElement('h3', 'card-title', candidate.title));
    copy.append(makeElement('p', 'card-detail', candidate.error || candidate.detail));
    const badge = makeElement('span', 'format-badge', candidate.typeLabel);
    top.append(copy, badge);
    card.append(top);

    const actions = makeElement('div', 'candidate-actions');
    if (candidate.kind === 'hls' && candidate.variants.length) {
      const label = makeElement('label', 'visually-hidden', '选择画质');
      label.htmlFor = `${candidate.id}-quality`;
      const select = makeElement('select', 'quality-select');
      select.id = `${candidate.id}-quality`;
      select.setAttribute('aria-label', '选择画质');
      for (const variant of candidate.variants) {
        const option = makeElement('option', '', variant.label || '原始画质');
        option.value = variant.url;
        select.append(option);
      }
      actions.append(label, select);
    }
    if (candidate.kind === 'hls' && !candidate.variants.length) {
      const inspectButton = makeElement('button', 'button button-secondary', candidate.inspecting ? '检查中…' : '检查画质');
      inspectButton.type = 'button';
      inspectButton.dataset.action = 'inspect';
      inspectButton.disabled = candidate.inspecting || !canDownload;
      actions.append(inspectButton);
    } else {
      const downloadButton = makeElement('button', 'button button-primary', '下载');
      downloadButton.type = 'button';
      downloadButton.dataset.action = 'download';
      downloadButton.disabled = !canDownload;
      actions.append(downloadButton);
    }
    card.append(actions);
    return card;
  }

  function renderTask(task) {
    const card = makeElement('article', 'task-card');
    card.dataset.taskId = task.id;
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
    if (task.canCancel) {
      const button = makeElement('button', 'button button-quiet', '取消');
      button.type = 'button';
      button.dataset.action = 'cancel';
      footer.append(button);
    }
    if (task.canRetry) {
      const button = makeElement('button', 'button button-secondary', '重试');
      button.type = 'button';
      button.dataset.action = 'retry';
      footer.append(button);
    }
    if (task.canReveal) {
      const button = makeElement('button', 'button button-secondary', '在 Finder 中显示');
      button.type = 'button';
      button.dataset.action = 'reveal';
      footer.append(button);
    }
    card.append(footer);
    return card;
  }

  function render() {
    const view = viewState.buildViewModel(model);
    elements.connectionPanel.dataset.tone = view.connection.tone;
    viewState.setText(elements.connectionTitle, view.connection.label);
    viewState.setText(elements.connectionDetail, view.connection.detail);
    elements.rescanButton.disabled = view.scanning;
    viewState.setText(elements.rescanButton, view.scanning ? '扫描中…' : '重新扫描');

    const candidateNodes = view.candidates.map((candidate, index) => renderCandidate(candidate, index, view.canDownload));
    elements.candidateList.replaceChildren(...candidateNodes);
    elements.candidateEmpty.hidden = candidateNodes.length > 0;
    viewState.setText(elements.candidateEmptyText, view.emptyMessage);

    const taskNodes = view.tasks.map(renderTask);
    elements.taskList.replaceChildren(...taskNodes);
    elements.taskEmpty.hidden = taskNodes.length > 0;
  }

  async function refreshCandidates() {
    const tab = await currentTab();
    if (!tab || !Number.isInteger(tab.id)) throw new Error('无法读取当前标签页');
    activeTabId = tab.id;
    const response = await runtimeMessage({ type: 'GET_TAB_MEDIA', tabId: activeTabId });
    if (!response || !response.ok) throw new Error('无法读取页面中的视频');
    model.pageUrl = response.pageUrl || tab.url || '';
    model.candidates = Array.isArray(response.candidates) ? response.candidates : [];
    render();
  }

  async function refreshTasks() {
    try {
      const tasks = await client.listTasks();
      model.connection = 'connected';
      model.tasks = Array.isArray(tasks) ? tasks : [];
    } catch (error) {
      model.connection = error && error.code === 'missing_token' ? 'disconnected' : 'error';
      if (error && error.code === 'unauthorized') model.connection = 'disconnected';
      model.tasks = [];
    }
    render();
  }

  async function rescan() {
    if (!Number.isInteger(activeTabId)) return;
    model.scanning = true;
    setNotice('');
    render();
    try {
      const response = await runtimeMessage({ type: 'RESCAN', tabId: activeTabId });
      if (!response || !response.ok) throw new Error(response && response.error ? response.error : '页面扫描器不可用');
      await new Promise((resolve) => setTimeout(resolve, 120));
      await refreshCandidates();
    } catch (error) {
      setNotice(error && error.message ? error.message : '无法重新扫描当前页面');
    } finally {
      model.scanning = false;
      render();
    }
  }

  async function inspectCandidate(index) {
    const candidate = model.candidates[index];
    if (!candidate) return;
    candidate.inspecting = true;
    candidate.error = '';
    render();
    try {
      const inspection = await client.inspect(candidate.url);
      if (inspection && inspection.mediaType === 'mp4') {
        candidate.kind = 'mp4';
        candidate.variants = [];
      } else {
        candidate.variants = viewState.sortHlsVariants(inspection && inspection.variants);
        if (!candidate.variants.length) candidate.error = '未找到可用画质';
      }
    } catch (error) {
      candidate.error = error && error.message ? error.message : '无法检查视频画质';
    } finally {
      candidate.inspecting = false;
      render();
    }
  }

  async function downloadCandidate(index, card) {
    const candidate = model.candidates[index];
    if (!candidate) return;
    let url = candidate.url;
    if (candidate.kind === 'hls') {
      const quality = card.querySelector('select');
      if (!quality || !quality.value) return;
      url = quality.value;
    }
    setNotice('已添加到本地下载队列。', 'success');
    try {
      const task = await client.createTask({ url, title: candidate.title || '未命名视频', mediaType: candidate.kind });
      if (task && task.id) model.tasks = [task, ...model.tasks.filter((item) => item.id !== task.id)];
      await refreshTasks();
    } catch (error) {
      setNotice(error && error.message ? error.message : '无法创建下载任务');
    }
    render();
  }

  async function taskAction(taskId, action) {
    try {
      if (action === 'cancel') await client.cancelTask(taskId);
      if (action === 'retry') await client.retryTask(taskId);
      if (action === 'reveal') {
        await client.revealTask(taskId);
        setNotice('已在 Finder 中显示文件。', 'success');
      }
      await refreshTasks();
    } catch (error) {
      setNotice(error && error.message ? error.message : '无法操作该任务');
    }
  }

  elements.candidateList.addEventListener('click', function onCandidateAction(event) {
    const button = event.target.closest('button[data-action]');
    const card = button && button.closest('.candidate-card');
    if (!button || !card) return;
    const index = Number(card.dataset.index);
    if (button.dataset.action === 'inspect') void inspectCandidate(index);
    if (button.dataset.action === 'download') void downloadCandidate(index, card);
  });

  elements.taskList.addEventListener('click', function onTaskAction(event) {
    const button = event.target.closest('button[data-action]');
    const card = button && button.closest('.task-card');
    if (!button || !card || !card.dataset.taskId) return;
    void taskAction(card.dataset.taskId, button.dataset.action);
  });

  elements.rescanButton.addEventListener('click', function onRescan() { void rescan(); });
  elements.optionsButton.addEventListener('click', function openOptions() { void chrome.runtime.openOptionsPage(); });

  function startPolling() {
    if (pollTimer !== null) return;
    pollTimer = setInterval(function pollTasks() {
      if (model.connection === 'connected') void refreshTasks();
    }, 1200);
  }

  function stopPolling() {
    if (pollTimer !== null) clearInterval(pollTimer);
    pollTimer = null;
  }

  window.addEventListener('unload', stopPolling);
  render();
  void Promise.allSettled([refreshCandidates(), refreshTasks()]).then(startPolling);
}());
