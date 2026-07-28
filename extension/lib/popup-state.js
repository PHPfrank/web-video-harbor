(function initPopupState(root, factory) {
  'use strict';

  const api = factory();
  root.VideoGrabberPopupState = api;
  if (typeof module === 'object' && module.exports) module.exports = api;
}(typeof globalThis !== 'undefined' ? globalThis : this, function createPopupState() {
  'use strict';

  const CONNECTIONS = {
    connected: { label: '本地助手已连接', tone: 'online', detail: '可以开始下载。' },
    connecting: { label: '正在连接本地助手', tone: 'pending', detail: '正在检查服务状态…' },
    disconnected: { label: '未连接本地助手', tone: 'offline', detail: '请先在设置中输入配对密钥。' },
    error: { label: '本地助手暂不可用', tone: 'offline', detail: '请确认助手已启动，然后重试。' },
  };

  const TASKS = {
    queued: { statusLabel: '等待中', tone: 'active', canCancel: true },
    downloading: { statusLabel: '下载中', tone: 'active', canCancel: true },
    merging: { statusLabel: '正在合并音视频', tone: 'active', canCancel: true },
    completed: { statusLabel: '已完成', tone: 'success', canReveal: true },
    failed: { statusLabel: '下载失败', tone: 'danger', canRetry: true },
    canceled: { statusLabel: '已取消', tone: 'muted', canRetry: true },
  };

  const PLATFORM_QUALITY_OPTIONS = [
    { value: 'best', label: '最佳画质' },
    { value: '1080', label: '1080P' },
    { value: '720', label: '720P' },
  ];

  function clampProgress(value) {
    const number = Number(value);
    if (!Number.isFinite(number)) return 0;
    return Math.max(0, Math.min(100, Math.round(number)));
  }

  function shortText(value, fallback, limit) {
    const text = typeof value === 'string' ? value.trim() : '';
    if (!text) return fallback;
    return text.length > limit ? `${text.slice(0, limit)}…` : text;
  }

  function isWeChatPage(value) {
    try {
      const hostname = new URL(value).hostname.toLowerCase();
      return hostname === 'weixin.qq.com' || hostname.endsWith('.weixin.qq.com')
        || hostname === 'wechat.com' || hostname.endsWith('.wechat.com');
    } catch (_error) {
      return false;
    }
  }

  function sortHlsVariants(variants) {
    if (!Array.isArray(variants)) return [];
    return variants.filter((item) => item && typeof item === 'object')
      .map((item) => ({ ...item }))
      .sort((left, right) => {
        const height = (Number(right.height) || 0) - (Number(left.height) || 0);
        if (height !== 0) return height;
        return (Number(right.bandwidth) || 0) - (Number(left.bandwidth) || 0);
      });
  }

  function downloadableVariant(variant) {
    if (!variant || typeof variant !== 'object' || typeof variant.url !== 'string') return false;
    try {
      const url = new URL(variant.url);
      return (url.protocol === 'http:' || url.protocol === 'https:') && !url.username && !url.password;
    } catch (_error) {
      return false;
    }
  }

  function applyInspection(candidate, inspection) {
    const next = candidate && typeof candidate === 'object' ? { ...candidate } : {};
    next.inspecting = false;
    next.error = '';
    if (inspection && inspection.mediaType === 'mp4') {
      next.kind = 'mp4';
      next.variants = [];
      return next;
    }
    next.kind = 'hls';
    const variants = sortHlsVariants(inspection && inspection.variants).filter(downloadableVariant);
    next.variants = variants.length ? variants : [{ url: next.url, label: '原始画质' }].filter(downloadableVariant);
    return next;
  }

  function candidateView(candidate, index) {
    const isHls = candidate && candidate.kind === 'hls';
    const isPlatform = candidate && candidate.kind === 'platform';
    const isWebM = candidate && candidate.kind === 'webm';
    const width = Number(candidate && candidate.width) || 0;
    const height = Number(candidate && candidate.height) || 0;
    const providerLabel = candidate && candidate.provider === 'youtube' ? 'YouTube' : '哔哩哔哩';
    const selectedQuality = PLATFORM_QUALITY_OPTIONS.some((option) => (
      option.value === (candidate && candidate.selectedQuality)
    )) ? candidate.selectedQuality : 'best';
    const view = {
      id: `candidate-${index}`,
      url: candidate && typeof candidate.url === 'string' ? candidate.url : '',
      kind: isPlatform ? 'platform' : (isHls ? 'hls' : (isWebM ? 'webm' : 'mp4')),
      typeLabel: isPlatform ? providerLabel : (isHls ? 'M3U8' : (isWebM ? 'WebM' : 'MP4')),
      title: shortText(candidate && candidate.title, '未命名视频', 120),
      detail: isPlatform ? '仅支持无需登录即可观看的公开视频'
        : (width && height ? `${width} × ${height}` : (isHls ? '需要检查可用画质' : '可直接下载')),
      variants: sortHlsVariants(candidate && candidate.variants),
      qualityOptions: isPlatform ? PLATFORM_QUALITY_OPTIONS.map((option) => ({ ...option })) : [],
      selectedQuality: isPlatform ? selectedQuality : '',
      inspecting: Boolean(candidate && candidate.inspecting),
      pending: Boolean(candidate && candidate.pending),
      canUse: candidate && typeof candidate.canUse === 'boolean' ? candidate.canUse : true,
      selectedVariant: candidate && typeof candidate.selectedVariant === 'string' ? candidate.selectedVariant : '',
      error: shortText(candidate && candidate.error, '', 80),
      blockedReason: shortText(candidate && candidate.blockedReason, '', 80),
    };
    if (!isPlatform && candidate && candidate.blockedReason) {
      view.detail = shortText(candidate.blockedReason, view.detail, 80);
    }
    return view;
  }

  function taskView(task) {
    const descriptor = TASKS[task && task.status] || {
      statusLabel: '状态未知', tone: 'muted', canCancel: false, canRetry: false, canReveal: false,
    };
    const error = task && task.status === 'failed' ? shortText(task.error, '本地助手未能完成下载', 80) : '';
    return {
      id: task && typeof task.id === 'string' ? task.id : '',
      title: shortText(task && task.title, '未命名视频', 120),
      status: task && task.status,
      statusLabel: descriptor.statusLabel,
      tone: descriptor.tone,
      progress: clampProgress(task && task.progress),
      detail: error,
      canCancel: Boolean(descriptor.canCancel),
      canRetry: Boolean(descriptor.canRetry),
      canReveal: Boolean(descriptor.canReveal),
      pending: Boolean(task && task.pending),
    };
  }

  function buildViewModel(input) {
    const source = input && typeof input === 'object' ? input : {};
    const connectionName = Object.hasOwn(CONNECTIONS, source.connection) ? source.connection : 'disconnected';
    const candidates = Array.isArray(source.candidates) ? source.candidates.map(candidateView).filter((item) => item.url) : [];
    const tasks = Array.isArray(source.tasks) ? source.tasks.map(taskView).filter((item) => item.id) : [];
    let emptyMessage = '尚未发现可下载的视频';
    if (source.experimentalPlatformBlocked === true) {
      emptyMessage = '实验性平台兼容尚未开启，可在设置中阅读说明后开启';
    } else if (source.scanning) emptyMessage = '正在重新扫描当前页面…';
    else if (isWeChatPage(source.pageUrl)) emptyMessage = '请先在浏览器中播放视频几秒，再重新扫描';
    const connection = { ...CONNECTIONS[connectionName] };
    if (connectionName === 'connected' && source.ffmpegAvailable === false) {
      connection.detail = '本地助手已连接；未安装 FFmpeg，MP4 仍可下载。';
    }
    return {
      connection,
      candidates,
      tasks,
      scanning: Boolean(source.scanning),
      experimentalPlatformBlocked: source.experimentalPlatformBlocked === true,
      emptyMessage,
      canDownload: connectionName === 'connected',
    };
  }

  function setText(element, value) {
    if (element) element.textContent = value == null ? '' : String(value);
  }

  return { applyInspection, buildViewModel, isWeChatPage, setText, sortHlsVariants };
}));
