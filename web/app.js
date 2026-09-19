/* Codex Turn State panel.
   Rendered by CPA Management clients (for example CPA Manager Plus) as a
   browser resource under /v0/resource/plugins/<plugin-id>/panel, and served by
   the plugin itself under CPA's /v0/resource/plugins/ prefix.

   Data is read from the plugin Management API. The Management Center key is
   decoded from the same-origin saved sign-in state and kept in memory only. */
(function () {
  'use strict';

  var SALT = 'cli-proxy-api-webui::secure-storage';
  var RESOURCE_MARKER = '/v0/resource/plugins/';
  var RESOURCE_STATUS = '/v0/resource/plugins/cpa-codex-turn-state/status';
  var API_PREFIX = '/v0/management/codex-turn-state';
  var POLL_MS = 5000;
  var STORAGE_KEY = 'cli-proxy-auth';

  var COPY = {
    zh: {
      subtitle: '查看并维护每个 OAuth Codex 账号的 turn state：有效期、下次刷新、探测结果与额度退避。',
      autoPoll: '自动轮询',
      refreshAll: '全部刷新',
      clearInvalid: '清理无效',
      hardClear: '重置全部缓存',
      reconnect: '重新连接',
      noticeTitle: '需要 CPA 管理中心登录状态',
      noticeBody: '请返回 CPA 管理中心重新登录并勾选“记住密码”，然后刷新本页面。',
      noticeKeyMissing: '未在本地登录状态中找到管理密钥。',
      noticeUnsupported: '当前后端未暴露插件管理接口。',
      metricsLabel: '概览',
      metricValid: '有效可注入',
      metricValidHint: '未过期且未被替换',
      metricProbing: '探测中',
      metricProbingHint: '正在向上游获取 state',
      metricPending: '待刷新',
      metricPendingHint: '已排队或错误触发',
      metricBackoff: '额度退避',
      metricBackoffHint: '额度受限后暂停探测',
      metricEmpty: '无 state',
      metricEmptyHint: '尚未获得可用 state',
      metricAccounts: '账号 / 模型',
      filterStatus: '状态',
      filterModel: '模型',
      filterSort: '排序',
      sortSeverity: '按异常优先',
      sortExpiry: '按到期时间',
      sortAccount: '按账号',
      sortModel: '按模型',
      hideUnprobeable: '隐藏不可探测条目',
      searchPlaceholder: '搜索邮箱、账号或模型',
      details: '详情与历史',
      tableTitle: '逐条状态',
      tableDescription: '长度 292（10 块）/ 332（12 块）为常见合规值。',
      colStatus: '状态',
      colAccount: '账号',
      colModel: '模型',
      colState: 'state 长度',
      colIssued: '签发',
      colExpires: '到期',
      colNextRefresh: '下次刷新',
      colLastProbe: '最近探测',
      colActions: '操作',
      colPlan: '套餐',
      colRule: '合规块数',
      colScope: '模型范围',
      loading: '正在读取 turn state…',
      probeTitle: '探测与注入配置',
      probeHint: '来自当前生效的插件配置与运行时状态。',
      configuredTitle: '已配置账号',
      configuredHint: '来自插件 credentials 配置；未获得 state 的账号会显示为 0 条。',
      historyTitle: '最近事件',
      historyHint: '探测结果、state 提升与手动操作，最多保留 50 条。',
      footerPlugin: '插件版本',
      footerUpdated: '数据更新于',
      actions: '立即刷新',
      refreshing: '正在排队…',
      refreshQueued: '已排队 {n} 个条目',
      refreshSkipped: '{n} 个条目被跳过',
      refreshNone: '没有可刷新的条目',
      cleared: '已清理 {n} 个条目（剩余 {n2}）',
      clearedNone: '没有需要清理的条目',
      hardClearConfirm: '确定要清空全部缓存的 turn state 吗？业务请求会重新按需获取。',
      hardCleared: '已清空缓存',
      cancelled: '已取消',
      filterAll: '全部',
      filterStatusValid: '有效',
      filterStatusExpired: '已过期',
      filterStatusProbing: '探测中',
      filterStatusPending: '待刷新',
      filterStatusBackoff: '额度退避',
      filterStatusIncompatible: '不合规',
      filterStatusUnsupported: '不可探测',
      filterStatusEmpty: '无 state',
      badgeValid: '有效',
      badgeExpired: '已过期',
      badgeProbing: '探测中',
      badgePending: '待刷新',
      badgeBackoff: '额度退避',
      badgeIncompatible: '不合规',
      badgeUnsupported: '不可探测',
      badgeEmpty: '无 state',
      blocks: '{n} 块',
      lengthOnly: '{n} 字符',
      noState: '无',
      inFuture: '尚未生效',
      relNow: '刚刚',
      relMinutes: '{n} 分钟后',
      relMinutesAgo: '{n} 分钟前',
      relHours: '{n} 小时后',
      relHoursAgo: '{n} 小时前',
      relDays: '{n} 天后',
      relDaysAgo: '{n} 天前',
      never: '从未',
      backoffUntil: '退避至 {t}',
      probeOk: '成功',
      probeAuthUnavailable: '凭证不可用于探测',
      probeNetwork: '网络错误',
      probeMissing: '上游未返回 state',
      probeRejected: 'state 不合规',
      probeIncomplete: '流未完成',
      probeRate: '上游限流 / 额度用尽',
      probeUpstream: '上游不可用',
      probeFailed: '探测失败',
      probePersist: '持久化失败',
      probeInvalid: '请求无效',
      reasonBeforeExpiry: '到期前刷新',
      reasonMissingState: '无 state',
      reasonManual: '手动刷新',
      reasonRateLimit: '限流触发',
      reasonUpstream: '上游不可用',
      reasonOverload: '上游过载',
      reasonRetry: '凭证重试',
      reasonClearInvalid: '清理无效条目',
      reasonClearExpired: '清理已过期条目',
      reasonClearAll: '清空全部缓存',
      eventProbe: '探测',
      eventPromote: '提升 state',
      eventManual: '手动刷新',
      eventClear: '清理缓存',
      kvAccepting: '插件接受请求',
      kvEnabled: '探测开启',
      kvBackground: '后台刷新',
      kvErrors: '错误触发刷新',
      kvRefreshBefore: '提前刷新',
      kvTimeout: '探测超时',
      kvRetry: '重试间隔',
      kvAttempts: '每轮最多尝试',
      kvBackoff: '额度退避',
      kvPool: '代理池节点',
      kvSource: '代理来源',
      kvFirstProxy: '前置代理',
      kvInjectExpired: '注入过期 state',
      kvTTL: 'state 有效期',
      kvLive: '进行中探测',
      stateRunning: '运行中',
      stateStopped: '已停止',
      stateDisabled: '已关闭',
      kvDefaults: '默认策略',
      scopeAll: '全部模型',
      scopeStateModel: '仅 {m}',
      scopeList: '{n} 个模型',
      yes: '是',
      no: '否',
      seeded: '已 seed',
      unseeded: '未 seed',
      entryCount: '{n} 条',
      emptyRows: '当前筛选下没有条目。',
      emptyHistory: '暂无事件。',
      disabled: '已禁用',
      unavailable: '不可用',
      requestFailed: '请求失败：HTTP {n}',
      invalidResponse: '后端返回了无效响应。',
      unauthorized: '管理密钥无效或已过期。',
      loadFailed: '读取 turn state 失败。'
    },
    en: {
      subtitle: 'Inspect and maintain every OAuth Codex turn state: validity, next refresh, probe outcomes and quota backoff.',
      autoPoll: 'Auto refresh',
      refreshAll: 'Refresh all',
      clearInvalid: 'Clean invalid',
      hardClear: 'Reset all state',
      reconnect: 'Reconnect',
      noticeTitle: 'CPA Management Center session required',
      noticeBody: 'Sign in to the CPA Management Center again with “remember password” enabled, then reload this page.',
      noticeKeyMissing: 'No management key found in the saved sign-in state.',
      noticeUnsupported: 'This backend does not expose plugin management APIs.',
      metricsLabel: 'Summary',
      metricValid: 'Injectable',
      metricValidHint: 'Not expired, not replaced',
      metricProbing: 'Probing',
      metricProbingHint: 'Fetching state upstream',
      metricPending: 'Pending refresh',
      metricPendingHint: 'Queued or error triggered',
      metricBackoff: 'Quota backoff',
      metricBackoffHint: 'Probing paused after quota limit',
      metricEmpty: 'No state',
      metricEmptyHint: 'No usable state yet',
      metricAccounts: 'Accounts / models',
      filterStatus: 'Status',
      filterModel: 'Model',
      filterSort: 'Sort',
      sortSeverity: 'Problems first',
      sortExpiry: 'By expiry',
      sortAccount: 'By account',
      sortModel: 'By model',
      hideUnprobeable: 'Hide unprobeable entries',
      searchPlaceholder: 'Search email, account or model',
      details: 'Details and history',
      tableTitle: 'Per-entry state',
      tableDescription: 'Lengths 292 (10 blocks) and 332 (12 blocks) are the common accepted forms.',
      colStatus: 'Status',
      colAccount: 'Account',
      colModel: 'Model',
      colState: 'State length',
      colIssued: 'Issued',
      colExpires: 'Expires',
      colNextRefresh: 'Next refresh',
      colLastProbe: 'Last probe',
      colActions: 'Actions',
      colPlan: 'Plan',
      colRule: 'Accepted blocks',
      colScope: 'Model scope',
      loading: 'Loading turn state…',
      probeTitle: 'Probe and injection configuration',
      probeHint: 'Taken from the active plugin configuration and runtime state.',
      configuredTitle: 'Configured accounts',
      configuredHint: 'From the plugin credentials section; accounts without state show zero entries.',
      historyTitle: 'Recent events',
      historyHint: 'Probe outcomes, promotions and manual actions, 50 entries max.',
      footerPlugin: 'Plugin version',
      footerUpdated: 'Updated',
      actions: 'Refresh now',
      refreshing: 'Queueing…',
      refreshQueued: 'Queued {n} entries',
      refreshSkipped: '{n} entries skipped',
      refreshNone: 'Nothing to refresh',
      cleared: 'Cleared {n} entries ({n2} left)',
      clearedNone: 'Nothing to clean',
      hardClearConfirm: 'Clear all cached turn state? Business requests will re-acquire it on demand.',
      hardCleared: 'Cache cleared',
      cancelled: 'Cancelled',
      filterAll: 'All',
      filterStatusValid: 'Valid',
      filterStatusExpired: 'Expired',
      filterStatusProbing: 'Probing',
      filterStatusPending: 'Pending',
      filterStatusBackoff: 'Quota backoff',
      filterStatusIncompatible: 'Incompatible',
      filterStatusUnsupported: 'Unprobeable',
      filterStatusEmpty: 'No state',
      badgeValid: 'Valid',
      badgeExpired: 'Expired',
      badgeProbing: 'Probing',
      badgePending: 'Pending',
      badgeBackoff: 'Quota backoff',
      badgeIncompatible: 'Incompatible',
      badgeUnsupported: 'Unprobeable',
      badgeEmpty: 'No state',
      blocks: '{n} blocks',
      lengthOnly: '{n} chars',
      noState: 'None',
      inFuture: 'Not yet valid',
      relNow: 'just now',
      relMinutes: 'in {n} min',
      relMinutesAgo: '{n} min ago',
      relHours: 'in {n} h',
      relHoursAgo: '{n} h ago',
      relDays: 'in {n} d',
      relDaysAgo: '{n} d ago',
      never: 'never',
      backoffUntil: 'backoff until {t}',
      probeOk: 'Success',
      probeAuthUnavailable: 'Credential not probeable',
      probeNetwork: 'Network error',
      probeMissing: 'Upstream returned no state',
      probeRejected: 'State rejected',
      probeIncomplete: 'Stream incomplete',
      probeRate: 'Rate limited / quota used',
      probeUpstream: 'Upstream unavailable',
      probeFailed: 'Probe failed',
      probePersist: 'Persist failed',
      probeInvalid: 'Invalid request',
      reasonBeforeExpiry: 'before expiry',
      reasonMissingState: 'missing state',
      reasonManual: 'manual',
      reasonRateLimit: 'rate limit',
      reasonUpstream: 'upstream down',
      reasonOverload: 'overload',
      reasonRetry: 'credential retry',
      reasonClearInvalid: 'clean invalid',
      reasonClearExpired: 'clean expired',
      reasonClearAll: 'reset all',
      eventProbe: 'Probe',
      eventPromote: 'Promote',
      eventManual: 'Manual refresh',
      eventClear: 'Clear',
      kvAccepting: 'Plugin accepting',
      kvEnabled: 'Probe enabled',
      kvBackground: 'Background refresh',
      kvErrors: 'Refresh on errors',
      kvRefreshBefore: 'Refresh before',
      kvTimeout: 'Probe timeout',
      kvRetry: 'Retry interval',
      kvAttempts: 'Attempts per round',
      kvBackoff: 'Quota backoff',
      kvPool: 'Proxy pool',
      kvSource: 'Proxy source',
      kvFirstProxy: 'First proxy',
      kvInjectExpired: 'Inject expired state',
      kvTTL: 'State TTL',
      kvLive: 'Live probes',
      stateRunning: 'running',
      stateStopped: 'stopped',
      stateDisabled: 'disabled',
      kvDefaults: 'Defaults',
      scopeAll: 'all models',
      scopeStateModel: '{m} only',
      scopeList: '{n} models',
      yes: 'yes',
      no: 'no',
      seeded: 'seeded',
      unseeded: 'not seeded',
      entryCount: '{n} entries',
      emptyRows: 'No entries match the current filter.',
      emptyHistory: 'No events yet.',
      disabled: 'disabled',
      unavailable: 'unavailable',
      requestFailed: 'Request failed: HTTP {n}',
      invalidResponse: 'Backend returned an invalid response.',
      unauthorized: 'Management key is invalid or expired.',
      loadFailed: 'Failed to load turn state.'
    }
  };

  var lang = 'zh';
  var key = '';
  var baseMode = 'derived';
  var payload = null;
  var skew = 0;
  var timer = 0;
  var ticker = 0;
  var busy = false;

  function t(name, params) {
    var table = COPY[lang] || COPY.zh;
    var text = table[name] != null ? table[name] : (COPY.en[name] != null ? COPY.en[name] : name);
    if (params) {
      Object.keys(params).forEach(function (token) {
        text = text.split('{' + token + '}').join(String(params[token]));
      });
    }
    return text;
  }

  function el(id) { return document.getElementById(id); }

  function esc(value) {
    return String(value == null ? '' : value).replace(/[&<>'"]/g, function (ch) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;' }[ch];
    });
  }

  // --- host integration ------------------------------------------------------

  function readParentTheme() {
    try {
      if (window.parent === window) return null;
      var root = window.parent.document.documentElement;
      var theme = root.getAttribute('data-theme');
      if (theme === 'dark' || theme === 'light') return theme;
      var classes = root.classList;
      if (classes && classes.contains('dark')) return 'dark';
      if (classes && classes.contains('light')) return 'light';
      return null;
    } catch (error) {
      return null;
    }
  }

  function readParentLang() {
    try {
      if (window.parent === window) return null;
      var value = window.parent.document.documentElement.getAttribute('lang') || '';
      return value;
    } catch (error) {
      return null;
    }
  }

  function applyTheme() {
    var theme = readParentTheme();
    if (!theme) {
      theme = window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
    }
    document.documentElement.setAttribute('data-theme', theme);
  }

  function watchParent() {
    try {
      if (window.parent === window) return;
      var root = window.parent.document.documentElement;
      var observer = new MutationObserver(function () {
        applyTheme();
        var next = readParentLang();
        if (next && applyLang(next, false)) return;
      });
      observer.observe(root, { attributes: true, attributeFilter: ['data-theme', 'class', 'style', 'lang'] });
    } catch (error) {
      /* cross-origin hosts keep the local theme */
    }
  }

  function applyLang(value, redraw) {
    var next = String(value || '').toLowerCase().indexOf('en') === 0 ? 'en' : 'zh';
    var changed = next !== lang;
    lang = next;
    document.documentElement.setAttribute('lang', lang === 'en' ? 'en' : 'zh-CN');
    el('langToggle').textContent = lang === 'en' ? '中文' : 'EN';
    if (redraw !== false) {
      translateStatic();
      render();
    }
    return changed;
  }

  function translateStatic() {
    document.querySelectorAll('[data-i18n]').forEach(function (node) {
      node.textContent = t(node.getAttribute('data-i18n'));
    });
    document.querySelectorAll('[data-i18n-placeholder]').forEach(function (node) {
      node.setAttribute('placeholder', t(node.getAttribute('data-i18n-placeholder')));
    });
    document.title = 'Codex Turn State';
  }

  // --- management key --------------------------------------------------------

  function decodeStored(raw, xorKey) {
    var binary = atob(raw);
    var bytes = new Uint8Array(binary.length);
    for (var index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index);
    var keyBytes = new TextEncoder().encode(xorKey);
    var output = new Uint8Array(bytes.length);
    for (var position = 0; position < bytes.length; position += 1) output[position] = bytes[position] ^ keyBytes[position % keyBytes.length];
    return new TextDecoder().decode(output);
  }

  function readStoredValue() {
    try {
      var raw = localStorage.getItem(STORAGE_KEY);
      if (!raw) return null;
      var text = raw;
      if (text.indexOf('enc::v2::') === 0) {
        text = decodeStored(text.slice('enc::v2::'.length), SALT + '|v2|' + location.host);
      } else if (text.indexOf('enc::v1::') === 0) {
        text = decodeStored(text.slice('enc::v1::'.length), SALT + '|' + location.host + '|' + navigator.userAgent);
      }
      var parsed = JSON.parse(text);
      return parsed && parsed.state ? parsed.state : parsed;
    } catch (error) {
      return null;
    }
  }

  function readManagementKey() {
    var state = readStoredValue();
    if (!state) return '';
    var value = state.managementKey;
    return typeof value === 'string' ? value.trim() : '';
  }

  // CPAMP embeds this resource cross-origin, so its localStorage is not
  // visible here. Ask the parent for an in-memory key instead of putting it in
  // the iframe URL or persisting it in this page.
  function requestParentManagementKey() {
    try {
      if (window.parent !== window) {
        window.parent.postMessage({ type: 'codex-turn-state:request-management-key' }, '*');
      }
    } catch (error) { /* standalone resource */ }
  }

  window.addEventListener('message', function (event) {
    if (event.source !== window.parent || !event.data || event.data.type !== 'codex-turn-state:management-key') return;
    var value = event.data.managementKey;
    if (typeof value !== 'string' || value.trim() === '') return;
    key = value.trim();
    connect();
  });

  // --- transport -------------------------------------------------------------

  function derivedBase() {
    var index = location.pathname.indexOf(RESOURCE_MARKER);
    return index > 0 ? location.pathname.slice(0, index) : '';
  }

  function requestBase() {
    return baseMode === 'derived' ? derivedBase() : '';
  }

  function api(path, options) {
    var settings = options || {};
    var headers = { Accept: 'application/json' };
    if (key) headers.Authorization = 'Bearer ' + key;
    var body;
    if (settings.body) {
      headers['Content-Type'] = 'application/json';
      body = JSON.stringify(settings.body);
    }
    var target = requestBase() + (path === '/status' && !key ? RESOURCE_STATUS : API_PREFIX + path);
    return fetch(target, { method: settings.method || 'GET', headers: headers, body: body, cache: 'no-store' }).then(function (response) {
      if (response.status === 404 && baseMode === 'derived' && derivedBase() !== '') {
        baseMode = 'root';
        return api(path, options);
      }
      return response.text().then(function (text) {
        var data = null;
        try { data = text ? JSON.parse(text) : null; } catch (error) { data = null; }
        if (!response.ok) {
          var message = (data && (data.error || data.message)) || t('requestFailed', { n: response.status });
          var failure = new Error(message);
          failure.status = response.status;
          throw failure;
        }
        if (!data) throw new Error(t('invalidResponse'));
        return data;
      });
    });
  }

  function toast(message, isError) {
    var node = el('toast');
    node.textContent = message;
    node.classList.toggle('error', !!isError);
    node.classList.add('visible');
    window.clearTimeout(toast.timer);
    toast.timer = window.setTimeout(function () { node.classList.remove('visible'); }, 3200);
  }

  function setBusy(value, label) {
    busy = !!value;
    el('app').setAttribute('aria-busy', busy ? 'true' : 'false');
    ['refreshAll', 'clearInvalid', 'hardClear'].forEach(function (id) {
      el(id).disabled = busy;
    });
    if (label) toast(label);
  }

  function showNotice(message) {
    el('connectionMessage').textContent = message;
    el('connectionNotice').hidden = false;
    el('app').setAttribute('aria-busy', 'false');
  }

  function hideNotice() {
    el('connectionNotice').hidden = true;
  }

  // --- formatting ------------------------------------------------------------

  function parseTime(value) {
    if (!value) return null;
    var date = new Date(value);
    if (Number.isNaN(date.getTime())) return null;
    // The plugin reports the zero time for entries that never carried state.
    if (date.getFullYear() < 2000) return null;
    return date;
  }

  function clockNow() {
    return Date.now() + skew;
  }

  function fmtClock(date) {
    if (!date) return '—';
    return date.toLocaleTimeString(lang === 'en' ? 'en-GB' : 'zh-CN', { hour12: false });
  }

  function fmtStamp(date) {
    if (!date) return '—';
    return date.toLocaleString(lang === 'en' ? 'en-GB' : 'zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false });
  }

  function fmtRelative(seconds) {
    if (seconds == null) return '—';
    var abs = Math.abs(Math.round(seconds));
    var future = seconds >= 0;
    if (abs < 45) return t('relNow');
    if (abs < 3600) {
      var minutes = Math.max(1, Math.round(abs / 60));
      return t(future ? 'relMinutes' : 'relMinutesAgo', { n: minutes });
    }
    if (abs < 86400) {
      var hours = Math.round(abs / 3600);
      return t(future ? 'relHours' : 'relHoursAgo', { n: hours });
    }
    var days = Math.round(abs / 86400);
    return t(future ? 'relDays' : 'relDaysAgo', { n: days });
  }

  function fmtDuration(seconds) {
    if (seconds == null) return '—';
    var total = Math.max(0, Math.round(seconds));
    var hours = Math.floor(total / 3600);
    var minutes = Math.floor((total % 3600) / 60);
    var secs = total % 60;
    if (hours > 0) return hours + 'h ' + String(minutes).padStart(2, '0') + 'm';
    if (minutes > 0) return minutes + 'm ' + String(secs).padStart(2, '0') + 's';
    return secs + 's';
  }

  function probeLabel(outcome) {
    if (!outcome) return '—';
    if (outcome === 'ok') return t('probeOk');
    if (outcome === 'auth_unavailable') return t('probeAuthUnavailable');
    if (outcome === 'network_error') return t('probeNetwork');
    if (outcome === 'state_missing') return t('probeMissing');
    if (outcome === 'state_rejected') return t('probeRejected');
    if (outcome === 'upstream_incomplete') return t('probeIncomplete');
    if (outcome === 'probe_failed') return t('probeFailed');
    if (outcome === 'persist_failed') return t('probePersist');
    if (outcome === 'request_invalid') return t('probeInvalid');
    if (outcome.indexOf('usage_limit_reached') >= 0 || outcome.indexOf('insufficient_quota') >= 0 || outcome.indexOf('429') >= 0 || outcome.indexOf('rate_limit') >= 0) return t('probeRate');
    if (outcome.indexOf('502') >= 0 || outcome.indexOf('503') >= 0 || outcome.indexOf('504') >= 0) return t('probeUpstream');
    return outcome;
  }

  function reasonLabel(reason) {
    if (!reason) return '';
    var map = {
      before_expiry: 'reasonBeforeExpiry',
      missing_state: 'reasonMissingState',
      manual: 'reasonManual',
      rate_limit: 'reasonRateLimit',
      upstream_unavailable: 'reasonUpstream',
      overload: 'reasonOverload',
      credential_retry: 'reasonRetry',
      invalid: 'reasonClearInvalid',
      expired: 'reasonClearExpired',
      all: 'reasonClearAll'
    };
    return map[reason] ? t(map[reason]) : reason;
  }

  function eventLabel(event) {
    var map = { probe: 'eventProbe', promote: 'eventPromote', manual_refresh: 'eventManual', clear: 'eventClear' };
    return map[event] ? t(map[event]) : event;
  }

  function stateCell(entry) {
    if (!entry.state_length) return '<span class="muted">' + esc(t('noState')) + '</span>';
    var detail = entry.blocks ? t('blocks', { n: entry.blocks }) : t('lengthOnly', { n: entry.state_length });
    var suffix = entry.compatible ? '' : ' <span class="warn-text">!</span>';
    return '<span class="mono">' + esc(entry.state_length) + '</span> <span class="muted">' + esc(detail) + '</span>' + suffix;
  }

  function bucketOf(entry) {
    if (entry.quota_backoff) return 'backoff';
    if (!entry.compatible && entry.state_length) return 'incompatible';
    if (entry.probing) return 'probing';
    if (entry.refresh_pending) return 'pending';
    if (entry.valid) return 'valid';
    if (entry.state_length) return 'expired';
    if (entry.last_probe === 'auth_unavailable') return 'unsupported';
    return 'empty';
  }

  function severityOf(bucket) {
    return { backoff: 0, incompatible: 1, probing: 2, pending: 3, expired: 4, unsupported: 5, empty: 6, valid: 7 }[bucket];
  }

  // --- rendering -------------------------------------------------------------

  function bucketLabel(bucket) {
    var map = {
      valid: 'badgeValid', expired: 'badgeExpired', probing: 'badgeProbing', pending: 'badgePending',
      backoff: 'badgeBackoff', incompatible: 'badgeIncompatible', unsupported: 'badgeUnsupported', empty: 'badgeEmpty'
    };
    return t(map[bucket] || 'badgeEmpty');
  }

  function filteredEntries() {
    var entries = (payload && payload.entries) || [];
    var status = el('statusFilter').value;
    var model = el('modelFilter').value;
    var query = el('search').value.trim().toLowerCase();
    var hideUnprobeable = el('hideUnprobeable').checked;
    return entries.filter(function (entry) {
      var bucket = bucketOf(entry);
      if (status !== 'all' && bucket !== status) return false;
      if (model !== 'all' && entry.model !== model) return false;
      if (hideUnprobeable && bucket === 'unsupported') return false;
      if (query) {
        var haystack = [entry.email, entry.account, entry.model, entry.auth_index].join(' ').toLowerCase();
        if (haystack.indexOf(query) < 0) return false;
      }
      return true;
    });
  }

  function sortEntries(entries) {
    var mode = el('sortSelect').value;
    return entries.slice().sort(function (left, right) {
      if (mode === 'expiry') return left.seconds_to_expiry - right.seconds_to_expiry;
      if (mode === 'account') return String(left.email || left.account).localeCompare(String(right.email || right.account));
      if (mode === 'model') return String(left.model).localeCompare(String(right.model));
      var diff = severityOf(bucketOf(left)) - severityOf(bucketOf(right));
      if (diff !== 0) return diff;
      return left.seconds_to_expiry - right.seconds_to_expiry;
    });
  }

  function renderMetrics() {
    var counters = payload.counters || {};
    el('metricValid').textContent = counters.valid || 0;
    el('metricProbing').textContent = counters.probing || 0;
    el('metricProbingHint').textContent = payload.probe ? t('kvLive') + ': ' + (payload.probe.live_probes || 0) : '—';
    el('metricPending').textContent = counters.pending || 0;
    el('metricBackoff').textContent = counters.quota_backoff || 0;
    el('metricBackoffHint').textContent = payload.probe ? t('kvBackoff') + ' ' + fmtDuration(payload.probe.quota_backoff_seconds) : '—';
    el('metricEmpty').textContent = counters.empty_state || 0;
    el('metricAccounts').textContent = (counters.accounts || 0) + ' / ' + (counters.models || 0);
    var failed = counters.failed || 0;
    el('metricAccountsHint').textContent = payload.accounts_resolved === false
      ? (lang === 'en' ? 'account names unavailable' : '账号名称不可用')
      : (lang === 'en' ? failed + ' failed probes' : failed + ' 个探测失败');
  }

  function accountCell(entry) {
    var email = entry.email || '';
    var name = email ? email : entry.account;
    var notes = [];
    if (entry.auth_disabled) notes.push(t('disabled'));
    if (entry.auth_unavailable) notes.push(t('unavailable'));
    var tags = notes.length ? ' <span class="muted">(' + esc(notes.join(', ')) + ')</span>' : '';
    return '<div class="account-cell"><strong>' + esc(name) + '</strong><small>' + esc(entry.account) + (entry.auth_index ? ' · ' + esc(entry.auth_index) : '') + '</small></div>' + tags;
  }

  function renderRows() {
    var rows = el('entryRows');
    var entries = sortEntries(filteredEntries());
    el('entryCount').textContent = entries.length + ' / ' + ((payload.entries || []).length);
    el('tableDescription').textContent = t('tableDescription');
    if (!entries.length) {
      rows.innerHTML = '<tr><td class="empty" colspan="9">' + esc(t('emptyRows')) + '</td></tr>';
      return;
    }
    rows.innerHTML = entries.map(function (entry) {
      var bucket = bucketOf(entry);
      var expires = parseTime(entry.expires_at);
      var refresh = parseTime(entry.next_refresh_at);
      var issued = parseTime(entry.issued_at);
      var probeAt = parseTime(entry.last_probe_at);
      var probeText = probeLabel(entry.last_probe);
      var probeMeta = [];
      if (entry.last_probe_reason) probeMeta.push(reasonLabel(entry.last_probe_reason));
      if (entry.last_probe_at) probeMeta.push(fmtStamp(probeAt));
      if (entry.quota_backoff && entry.blocked_until) probeMeta.push(t('backoffUntil', { t: fmtClock(parseTime(entry.blocked_until)) }));
      var plan = entry.plan ? esc(entry.plan) : (entry.expected_blocks ? String(entry.expected_blocks) : '—');
      return '<tr>' +
        '<td><span class="badge ' + bucket + '">' + esc(bucketLabel(bucket)) + '</span>' +
          (entry.probing ? ' <span class="muted">…</span>' : '') +
          (entry.refresh_pending ? ' <span class="muted">' + esc(reasonLabel(entry.refresh_pending)) + '</span>' : '') + '</td>' +
        '<td>' + accountCell(entry) + '</td>' +
        '<td><span class="mono">' + esc(entry.model) + '</span><br><small class="muted">' + plan + '</small></td>' +
        '<td>' + stateCell(entry) + '</td>' +
        '<td>' + (issued ? '<div class="time-cell"><span>' + esc(fmtClock(issued)) + '</span><small>' + esc(fmtStamp(issued)) + '</small></div>' : '<span class="muted">' + esc(t('noState')) + '</span>') + '</td>' +
        '<td>' + (expires && entry.state_length
          ? '<div class="time-cell"><span>' + esc(fmtStamp(expires)) + '</span><small data-countdown="' + esc(entry.seconds_to_expiry) + '">' + esc(fmtDuration(entry.seconds_to_expiry)) + '</small></div>'
          : '<span class="muted">—</span>') + '</td>' +
        '<td>' + (refresh
          ? '<div class="time-cell"><span>' + esc(fmtStamp(refresh)) + '</span><small data-countdown="' + esc(entry.seconds_to_refresh) + '">' + esc(fmtDuration(entry.seconds_to_refresh)) + '</small></div>'
          : '<span class="muted">—</span>') + '</td>' +
        '<td><div class="time-cell"><span>' + esc(probeText) + '</span><small>' + esc(probeMeta.join(' · ')) + '</small></div></td>' +
        '<td><button class="button ghost tiny" type="button" data-refresh-key="' + esc(entry.key) + '">' + esc(t('actions')) + '</button></td>' +
      '</tr>';
    }).join('');
  }

  function renderProbe() {
    var probe = payload.probe || {};
    var grid = el('probeGrid');
    var yes = function (value) { return value ? t('yes') : t('no'); };
    var items = [
      [t('kvAccepting'), yes(probe.accepting)],
      [t('kvEnabled'), yes(probe.enabled)],
      [t('kvBackground'), yes(probe.background_refresh)],
      [t('kvErrors'), yes(probe.refresh_on_errors)],
      [t('kvRefreshBefore'), fmtDuration(probe.refresh_before_seconds)],
      [t('kvTTL'), fmtDuration(payload.state_ttl_seconds)],
      [t('kvTimeout'), (probe.timeout_seconds || 0) + 's'],
      [t('kvRetry'), fmtDuration(probe.retry_seconds)],
      [t('kvAttempts'), String(probe.max_attempts || 0)],
      [t('kvBackoff'), fmtDuration(probe.quota_backoff_seconds)],
      [t('kvPool'), String(probe.proxy_pool_size || 0) + (probe.proxy_source ? ' (' + probe.proxy_source + ')' : '')],
      [t('kvFirstProxy'), yes(probe.first_proxy)],
      [t('kvInjectExpired'), yes(probe.inject_expired)],
      [t('kvLive'), String(probe.live_probes || 0)]
    ];
    if (payload.defaults) {
      var defaults = payload.defaults;
      var rule = defaults.accepted_blocks && defaults.accepted_blocks.length
        ? defaults.accepted_blocks.join(' / ')
        : (defaults.normal_blocks ? String(defaults.normal_blocks) : '—');
      items.push([t('kvDefaults'), (defaults.plan ? defaults.plan + ' · ' : '') + rule + (defaults.auto_update ? ' · auto' : '')]);
    }
    grid.innerHTML = items.map(function (item) {
      return '<div><dt>' + esc(item[0]) + '</dt><dd>' + esc(item[1]) + '</dd></div>';
    }).join('');
    el('probeState').textContent = probe.enabled
      ? (probe.accepting ? t('stateRunning') : t('stateStopped'))
      : t('stateDisabled');
  }

  function scopeText(view) {
    if (view.state_model) return t('scopeStateModel', { m: view.state_model });
    if (view.models && view.models.length) return t('scopeList', { n: view.models.length }) + ' (' + view.models.slice(0, 3).join(', ') + ')';
    return t('scopeAll');
  }

  function renderConfigured() {
    var configured = payload.configured || [];
    var rows = el('configuredRows');
    el('configuredCount').textContent = String(configured.length);
    if (!configured.length) {
      rows.innerHTML = '<tr><td class="empty" colspan="5">' + esc(t('emptyRows')) + '</td></tr>';
      return;
    }
    rows.innerHTML = configured.map(function (view) {
      var rule = view.accepted_blocks && view.accepted_blocks.length
        ? view.accepted_blocks.join(' / ')
        : (view.normal_blocks ? String(view.normal_blocks) : '—');
      var state = t('entryCount', { n: view.entry_count || 0 }) + ' · ' + (view.seeded ? t('seeded') : t('unseeded'));
      if (!view.has_state) state = '<span class="warn-text">' + esc(state) + '</span>';
      var notes = [];
      if (view.auth_disabled) notes.push(t('disabled'));
      return '<tr>' +
        '<td><div class="account-cell"><strong>' + esc(view.email || view.account) + '</strong><small>' + esc(view.account) + '</small></div>' + (notes.length ? ' <span class="muted">(' + esc(notes.join(', ')) + ')</span>' : '') + '</td>' +
        '<td>' + esc(view.plan || '—') + '</td>' +
        '<td class="mono">' + esc(rule) + '</td>' +
        '<td>' + esc(scopeText(view)) + '</td>' +
        '<td>' + state + '</td>' +
      '</tr>';
    }).join('');
  }

  function renderHistory() {
    var history = payload.history || [];
    var list = el('historyList');
    if (!history.length) {
      list.innerHTML = '<div class="empty-block">' + esc(t('emptyHistory')) + '</div>';
      return;
    }
    list.innerHTML = history.map(function (item) {
      var who = item.email || item.account || '';
      var parts = [];
      if (item.model) parts.push(item.model);
      if (item.outcome) parts.push(probeLabel(item.outcome));
      if (item.reason) parts.push(reasonLabel(item.reason));
      if (item.detail) parts.push(item.detail);
      return '<article class="history-item"><div><strong>' + esc(eventLabel(item.event)) + '</strong>' +
        (who ? ' <span class="muted">' + esc(who) + '</span>' : '') +
        '<p>' + esc(parts.join(' · ')) + '</p></div>' +
        '<time>' + esc(fmtStamp(parseTime(item.at))) + '</time></article>';
    }).join('');
  }

  var filterSignature = { model: '', status: '' };

  function renderModelFilter() {
    var select = el('modelFilter');
    var current = select.value || 'all';
    var models = {};
    ((payload && payload.entries) || []).forEach(function (entry) { if (entry.model) models[entry.model] = true; });
    var names = Object.keys(models).sort();
    var signature = names.join('|') + '#' + lang;
    if (signature !== filterSignature.model) {
      var options = ['<option value="all">' + esc(t('filterAll')) + '</option>'];
      names.forEach(function (name) {
        options.push('<option value="' + esc(name) + '">' + esc(name) + '</option>');
      });
      select.innerHTML = options.join('');
      filterSignature.model = signature;
    }
    select.value = names.indexOf(current) >= 0 ? current : 'all';
  }

  function renderStatusFilter() {
    var select = el('statusFilter');
    var current = select.value || 'all';
    var buckets = [
      ['all', 'filterAll'],
      ['valid', 'filterStatusValid'],
      ['expired', 'filterStatusExpired'],
      ['probing', 'filterStatusProbing'],
      ['pending', 'filterStatusPending'],
      ['backoff', 'filterStatusBackoff'],
      ['incompatible', 'filterStatusIncompatible'],
      ['unsupported', 'filterStatusUnsupported'],
      ['empty', 'filterStatusEmpty']
    ];
    var signature = 'v1#' + lang;
    if (signature !== filterSignature.status) {
      select.innerHTML = buckets.map(function (bucket) {
        return '<option value="' + bucket[0] + '">' + esc(t(bucket[1])) + '</option>';
      }).join('');
      filterSignature.status = signature;
    }
    select.value = current;
  }

  function render() {
    if (!payload) return;
    var dot = el('probeDot');
    dot.className = 'dot ' + (payload.probe_enabled && payload.probe && payload.probe.accepting ? 'on' : 'off');
    el('statusLabel').textContent = payload.probe_enabled
      ? (payload.background_refresh ? (lang === 'en' ? 'background refresh active' : '后台刷新运行中') : (lang === 'en' ? 'on-demand probe only' : '仅按需探测'))
      : (lang === 'en' ? 'probing disabled' : '探测已关闭');
    el('pluginVersion').textContent = payload.version || '—';
    el('serverClock').textContent = (lang === 'en' ? 'server ' : '服务器 ') + fmtClock(parseTime(payload.now));
    el('updatedAt').textContent = fmtClock(new Date());
    renderMetrics();
    renderRows();
    renderProbe();
    renderConfigured();
    renderHistory();
  }

  function tick() {
    document.querySelectorAll('[data-countdown]').forEach(function (node) {
      var base = Number(node.getAttribute('data-countdown'));
      if (!Number.isFinite(base)) return;
      var stamp = Number(node.getAttribute('data-countdown-at') || 0);
      if (!stamp) {
        stamp = clockNow() + base * 1000;
        node.setAttribute('data-countdown-at', String(stamp));
      }
      node.textContent = fmtDuration((stamp - clockNow()) / 1000);
    });
    if (payload && payload.now) el('serverClock').textContent = (lang === 'en' ? 'server ' : '服务器 ') + fmtClock(new Date(clockNow()));
  }

  // --- actions ---------------------------------------------------------------

  function load(silent) {
    return api('/status').then(function (data) {
      payload = data;
      var serverNow = parseTime(data.now);
      if (serverNow) skew = serverNow.getTime() - Date.now();
      renderModelFilter();
      renderStatusFilter();
      render();
      tick();
      hideNotice();
    }).catch(function (error) {
      if (error && (error.status === 401 || error.status === 403)) {
        key = '';
        showNotice(t('unauthorized') + ' ' + t('noticeBody'));
        return;
      }
      if (error && error.status === 404) {
        showNotice(t('noticeUnsupported'));
        return;
      }
      if (!silent) toast((error && error.message) || t('loadFailed'), true);
    });
  }

  function refreshAll() {
    setBusy(true, t('refreshing'));
    api('/refresh', { method: 'POST', body: { all: true } }).then(function (result) {
      var queued = (result.queued || []).length;
      var skipped = (result.skipped || []).length;
      var message = result.message || (queued ? t('refreshQueued', { n: queued }) : t('refreshNone'));
      if (skipped) message += ' · ' + t('refreshSkipped', { n: skipped });
      toast(message, !queued && skipped > 0);
      return load(true);
    }).catch(function (error) {
      toast((error && error.message) || t('loadFailed'), true);
    }).finally(function () { setBusy(false); });
  }

  function refreshEntry(entryKey, button) {
    if (button) button.disabled = true;
    api('/refresh', { method: 'POST', body: { keys: [entryKey] } }).then(function (result) {
      var queued = (result.queued || []).length;
      var skipped = result.skipped || [];
      if (queued) {
        toast(result.message || t('refreshQueued', { n: queued }));
      } else if (skipped.length) {
        toast(skipped[0].message || t('refreshNone'), true);
      } else {
        toast(t('refreshNone'));
      }
      return load(true);
    }).catch(function (error) {
      toast((error && error.message) || t('loadFailed'), true);
    }).finally(function () { if (button) button.disabled = false; });
  }

  function clearScope(scope, all) {
    var body = { scope: scope };
    if (all) body.all = true;
    return api('/clear', { method: 'POST', body: body }).then(function (result) {
      var removed = (result.removed || []).length;
      toast(removed ? t('cleared', { n: removed, n2: result.remaining }) : t('clearedNone'), false);
      if (result.failed) toast(result.failed, true);
      return load(true);
    });
  }

  function clearInvalid() {
    setBusy(true);
    clearScope('invalid', false).catch(function (error) {
      toast((error && error.message) || t('loadFailed'), true);
    }).finally(function () { setBusy(false); });
  }

  function hardClear() {
    if (!window.confirm(t('hardClearConfirm'))) {
      toast(t('cancelled'));
      return;
    }
    setBusy(true);
    clearScope('all', true).catch(function (error) {
      toast((error && error.message) || t('loadFailed'), true);
    }).finally(function () { setBusy(false); });
  }

  // --- bootstrap -------------------------------------------------------------

  function bind() {
    el('entryRows').addEventListener('click', function (event) {
      var button = event.target.closest('button[data-refresh-key]');
      if (!button) return;
      refreshEntry(button.getAttribute('data-refresh-key'), button);
    });
    ['statusFilter', 'modelFilter', 'sortSelect'].forEach(function (id) {
      el(id).addEventListener('change', function () { renderRows(); tick(); });
    });
    el('hideUnprobeable').addEventListener('change', function () { renderRows(); tick(); });
    el('search').addEventListener('input', function () { renderRows(); tick(); });
    el('autoPoll').addEventListener('change', function () { schedule(); });
    el('refreshAll').addEventListener('click', refreshAll);
    el('clearInvalid').addEventListener('click', clearInvalid);
    el('hardClear').addEventListener('click', hardClear);
    el('retryConnection').addEventListener('click', connect);
    el('langToggle').addEventListener('click', function () { applyLang(lang === 'zh' ? 'en' : 'zh'); });
    el('detailToggle').addEventListener('click', function () {
      var panel = el('detailPanels');
      var next = panel.hidden;
      panel.hidden = !next;
      this.setAttribute('aria-expanded', next ? 'true' : 'false');
    });
    document.addEventListener('visibilitychange', function () {
      if (document.hidden) {
        window.clearInterval(ticker);
        ticker = 0;
      } else {
        startTicker();
        if (el('autoPoll').checked) load(true);
      }
    });
  }

  function schedule() {
    window.clearInterval(timer);
    timer = 0;
    if (!el('autoPoll').checked || !key) return;
    timer = window.setInterval(function () {
      if (!document.hidden) load(true);
    }, POLL_MS);
  }

  function startTicker() {
    if (ticker) return;
    ticker = window.setInterval(tick, 1000);
  }

  function connect() {
    var localKey = readManagementKey();
    if (localKey) key = localKey;
    if (!key) {
      requestParentManagementKey();
      showNotice(t('noticeKeyMissing') + ' ' + t('noticeBody'));
      return;
    }
    setBusy(true);
    load().finally(function () {
      setBusy(false);
      schedule();
    });
  }

  function boot() {
    applyLang(readParentLang() || 'zh', false);
    applyTheme();
    watchParent();
    translateStatic();
    renderStatusFilter();
    bind();
    startTicker();
    connect();
  }

  boot();
})();
