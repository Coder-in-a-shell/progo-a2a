(() => {
  'use strict';

  const state = { token: sessionStorage.getItem('progo-console-key') || '', authRequired: false, timer: null };
  const $ = (selector) => document.querySelector(selector);
  const nodes = {
    refresh: $('#refresh-button'), connect: $('#connect-button'), noticeConnect: $('#notice-connect-button'),
    dialog: $('#key-dialog'), close: $('#dialog-close'), form: $('#key-form'), input: $('#api-key'),
    clear: $('#clear-key'), keyError: $('#key-error'), toast: $('#toast'), sync: $('#sync-label'),
    environment: $('#environment-label'),
    systemBadge: $('#system-badge'), systemMessage: $('#system-message'), agentNodes: $('#agent-nodes'),
    agentGrid: $('#agent-grid'), agentCount: $('#agent-count'), authNotice: $('#auth-notice'),
    trafficList: $('#traffic-list'), liveness: $('#liveness-value'), livenessDetail: $('#liveness-detail'),
    readiness: $('#readiness-value'), readinessDetail: $('#readiness-detail'), requests: $('#requests-value'),
    latency: $('#latency-value'), streams: $('#streams-value')
  };

  function make(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  async function request(url, authenticated = false) {
    const headers = { Accept: url === '/metrics' ? 'text/plain' : 'application/json' };
    if (authenticated && state.token) headers.Authorization = `Bearer ${state.token}`;
    const response = await fetch(url, { headers, cache: 'no-store' });
    if (!response.ok) {
      const error = new Error(`Request failed with ${response.status}`);
      error.status = response.status;
      throw error;
    }
    return url === '/metrics' ? response.text() : response.json();
  }

  function metricLines(text, name) {
    return text.split('\n').filter((line) => line.startsWith(name) && !line.startsWith('#'));
  }

  function valueFromLine(line) {
    const value = Number(line.trim().split(/\s+/).pop());
    return Number.isFinite(value) ? value : 0;
  }

  function sumMetric(text, name) {
    return metricLines(text, name).reduce((total, line) => total + valueFromLine(line), 0);
  }

  function parseLabels(line) {
    const labels = {};
    const match = line.match(/\{([^}]*)\}/);
    if (!match) return labels;
    for (const pair of match[1].matchAll(/([a-zA-Z_]+)="((?:\\.|[^"])*)"/g)) {
      labels[pair[1]] = pair[2].replace(/\\n/g, '\n').replace(/\\"/g, '"').replace(/\\\\/g, '\\');
    }
    return labels;
  }

  function summarizeMetrics(text) {
    const requests = sumMetric(text, 'a2a_requests_total');
    const durationSum = sumMetric(text, 'a2a_request_duration_seconds_sum');
    const durationCount = sumMetric(text, 'a2a_request_duration_seconds_count');
    return {
      requests,
      latency: durationCount > 0 ? (durationSum / durationCount) * 1000 : 0,
      streams: sumMetric(text, 'a2a_active_streams'),
      routes: metricLines(text, 'a2a_requests_total').map((line) => ({ ...parseLabels(line), count: valueFromLine(line) })).filter((route) => route.method && route.path)
    };
  }

  function setHealth(target, detail, ok, goodText, badText, detailText) {
    target.textContent = ok ? goodText : badText;
    target.className = ok ? 'status-good' : 'status-bad';
    detail.textContent = detailText;
  }

  function renderSystem(health, ready) {
    const healthy = health.status === 'healthy';
    const prepared = ready.status === 'ready';
    setHealth(nodes.liveness, nodes.livenessDetail, healthy, 'Healthy', 'Offline', healthy ? 'Process responding' : 'No response');
    setHealth(nodes.readiness, nodes.readinessDetail, prepared, 'Ready', 'Not ready', `${ready.adapters || 0} adapters loaded`);
    nodes.systemBadge.className = `state-badge ${healthy && prepared ? 'state-ready' : 'state-warn'}`;
    nodes.systemBadge.textContent = healthy && prepared ? 'ALL ROUTES READY' : 'ATTENTION NEEDED';
    nodes.systemMessage.textContent = healthy && prepared
      ? `The gateway is accepting traffic with ${ready.adapters || 0} adapter types ready to translate requests.`
      : 'The process is reachable, but one or more readiness checks need attention.';
  }

  function renderMetrics(summary) {
    nodes.requests.textContent = new Intl.NumberFormat().format(summary.requests);
    nodes.latency.textContent = summary.latency > 0 ? `${summary.latency < 10 ? summary.latency.toFixed(1) : Math.round(summary.latency)} ms` : '—';
    nodes.streams.textContent = new Intl.NumberFormat().format(summary.streams);
    renderTraffic(summary.routes);
  }

  function initials(name, id) {
    const source = (name || id || 'A').trim().split(/\s+/).slice(0, 2);
    return source.map((part) => part[0] || '').join('').toUpperCase();
  }

  function renderAgents(agents) {
    nodes.agentGrid.replaceChildren();
    nodes.agentNodes.replaceChildren();
    nodes.agentCount.textContent = `${agents.length} agent${agents.length === 1 ? '' : 's'}`;
    nodes.authNotice.classList.add('hidden');

    if (!agents.length) {
      const empty = make('div', 'empty-state');
      const glyph = make('span', 'empty-glyph', '+');
      const copy = make('div'); copy.append(make('strong', '', 'No agents configured'), make('p', '', 'Add an agent to the gateway configuration, then refresh.'));
      empty.append(glyph, copy); nodes.agentGrid.append(empty);
      nodes.agentNodes.append(make('div', 'agent-node', 'No configured routes'));
      return;
    }

    for (const agent of agents) {
      const card = make('article', 'agent-card');
      const head = make('div', 'agent-card-head');
      head.append(make('div', 'agent-monogram', initials(agent.name, agent.id)), make('span', 'agent-health', 'CONFIGURED'));
      card.append(head, make('h3', '', agent.name || agent.id), make('p', '', agent.description || `Routes requests to ${agent.id}.`));
      const caps = make('div', 'capabilities');
      const capabilities = Array.isArray(agent.capabilities) ? agent.capabilities : [];
      for (const capability of capabilities.slice(0, 4)) caps.append(make('span', 'capability', capability));
      if (!capabilities.length) caps.append(make('span', 'capability', 'general'));
      card.append(caps); nodes.agentGrid.append(card);

      if (nodes.agentNodes.children.length < 4) {
        const node = make('div', 'agent-node');
        node.append(make('span'), (() => { const copy = make('div'); copy.append(make('strong', '', agent.name || agent.id), make('small', '', (capabilities[0] || 'GENERAL').toUpperCase())); return copy; })());
        nodes.agentNodes.append(node);
      }
    }
  }

  function renderLockedAgents() {
    nodes.agentGrid.replaceChildren();
    nodes.agentCount.textContent = 'Protected';
    nodes.authNotice.classList.remove('hidden');
    nodes.agentNodes.replaceChildren();
    const locked = make('div', 'agent-node');
    locked.append(make('span'), (() => { const copy = make('div'); copy.append(make('strong', '', 'Protected inventory'), make('small', '', 'CONNECT KEY')); return copy; })());
    nodes.agentNodes.append(locked);
  }

  function renderTraffic(routes) {
    nodes.trafficList.replaceChildren();
    const sorted = [...routes].sort((a, b) => b.count - a.count).slice(0, 8);
    if (!sorted.length) {
      const empty = make('div', 'empty-state'); empty.append(make('span', 'empty-glyph', '↗'));
      const copy = make('div'); copy.append(make('strong', '', 'No request traffic yet'), make('p', '', 'Routes appear here after the gateway handles requests.')); empty.append(copy);
      nodes.trafficList.append(empty); return;
    }
    const total = sorted.reduce((sum, route) => sum + route.count, 0) || 1;
    for (const route of sorted) {
      const row = make('div', 'traffic-row');
      const name = make('div', 'route-name'); name.append(make('span', 'method-label', route.method), make('code', '', route.path));
      const status = make('span', `status-code ${Number(route.status) < 400 ? 'ok' : 'warn'}`, route.status || '—');
      const track = make('div', 'volume-track'); const fill = make('div', 'volume-fill'); fill.style.width = `${Math.max(3, (route.count / total) * 100)}%`; track.append(fill);
      row.append(name, status, track, make('span', 'route-count', new Intl.NumberFormat().format(route.count))); nodes.trafficList.append(row);
    }
  }

  function showToast(message) {
    nodes.toast.textContent = message; nodes.toast.classList.add('visible');
    window.clearTimeout(showToast.timer); showToast.timer = window.setTimeout(() => nodes.toast.classList.remove('visible'), 2600);
  }

  async function refresh({ quiet = false } = {}) {
    if (!quiet) nodes.refresh.classList.add('spinning');
    try {
      const bootstrap = await request('/console/api/bootstrap');
      state.authRequired = Boolean(bootstrap.auth_required);
      nodes.environment.textContent = `${String(bootstrap.role || 'api').toUpperCase()} · ${String(bootstrap.storage || 'memory').toUpperCase()}`;
    } catch (_) {
      state.authRequired = false;
    }
    const agentsRequest = state.authRequired && !state.token ? Promise.resolve(null) : request('/a2a/v1/agents', true);
    const [healthResult, readyResult, metricsResult, agentsResult] = await Promise.allSettled([
      request('/healthz'), request('/readyz'), request('/metrics'), agentsRequest
    ]);

    const health = healthResult.status === 'fulfilled' ? healthResult.value : { status: 'offline' };
    const ready = readyResult.status === 'fulfilled' ? readyResult.value : { status: 'not ready', adapters: 0 };
    renderSystem(health, ready);
    if (metricsResult.status === 'fulfilled') renderMetrics(summarizeMetrics(metricsResult.value));
    if (agentsResult.status === 'fulfilled' && agentsResult.value === null) renderLockedAgents();
    else if (agentsResult.status === 'fulfilled') renderAgents(agentsResult.value.agents || []);
    else if (agentsResult.reason && agentsResult.reason.status === 401) renderLockedAgents();
    else renderAgents([]);

    nodes.sync.textContent = `Last synced ${new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}`;
    nodes.refresh.classList.remove('spinning');
    if (!quiet) showToast('Gateway data refreshed');
  }

  function openDialog() {
    nodes.input.value = state.token; nodes.keyError.textContent = '';
    nodes.dialog.showModal(); window.setTimeout(() => nodes.input.focus(), 30);
  }

  nodes.connect.addEventListener('click', openDialog);
  nodes.noticeConnect.addEventListener('click', openDialog);
  nodes.close.addEventListener('click', () => nodes.dialog.close());
  nodes.refresh.addEventListener('click', () => refresh());
  nodes.clear.addEventListener('click', () => {
    state.token = ''; sessionStorage.removeItem('progo-console-key'); nodes.input.value = ''; nodes.dialog.close(); showToast('Gateway key cleared'); refresh({ quiet: true });
  });
  nodes.form.addEventListener('submit', async (event) => {
    event.preventDefault();
    const candidate = nodes.input.value.trim();
    if (!candidate) { nodes.keyError.textContent = 'Enter an API key to connect.'; return; }
    state.token = candidate;
    try {
      await request('/a2a/v1/agents', true);
      sessionStorage.setItem('progo-console-key', candidate); nodes.dialog.close(); showToast('Gateway connected'); refresh({ quiet: true });
    } catch (error) {
      state.token = sessionStorage.getItem('progo-console-key') || '';
      nodes.keyError.textContent = error.status === 401 ? 'The gateway rejected this key.' : 'The gateway could not verify this key.';
    }
  });
  nodes.dialog.addEventListener('click', (event) => {
    if (event.target === nodes.dialog) nodes.dialog.close();
  });

  const observer = new IntersectionObserver((entries) => {
    const visible = entries.filter((entry) => entry.isIntersecting).sort((a, b) => b.intersectionRatio - a.intersectionRatio)[0];
    if (!visible) return;
    document.querySelectorAll('.nav-item').forEach((item) => item.classList.toggle('active', item.dataset.section === visible.target.id));
  }, { rootMargin: '-20% 0px -65% 0px', threshold: [0, .25, .75] });
  document.querySelectorAll('main section[id]').forEach((section) => observer.observe(section));

  refresh({ quiet: true });
  state.timer = window.setInterval(() => refresh({ quiet: true }), 15000);
})();
