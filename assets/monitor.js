(() => {
  'use strict';

  // r475 Phase 3 improves detail readability, disk drill-down/sorting, memory
  // visualization, and compact Network/Cheats presentation.

  const monitor = {
    overlay: null,
    settingsModal: null,
    scheduleModal: null,
    memoryModal: null,
    itemModal: null,
    state: null,
    aiState: null,
    open: false,
    tab: 'processes',
    selectedPID: 0,
    selectedItemRef: null,
    itemDetail: null,
    itemBusy: false,
    processFilter: '',
    detailsOpen: false,
    stateTimer: null,
    heartbeatTimer: null,
    aiTimer: null,
    clientID: '',
    lastAISequence: 0,
    lastNotificationID: 0,
    stagedActivePoll: 2,
    stagedPassivePoll: 10,
    stagedHistoryMax: 10,
    stagedScheduledRunLimit: 100,
    stagedAIMemoryMax: 20,
    stagedSchedule: 'off',
    stagedCron: '',
    stagedVTEnv: 'VIRUSTOTAL_API_KEY',
    aiExpanded: false,
    tabFilter: {},
    tableSort: {},
    cheatCategory: '',
    diskGroup: 'all',
    history: { cpu: [], gpu: [], memory: [], disk: [], network: [] },
    processHistory: {},
    lastStateAt: 0,
  };

  function escapeHtml(value) {
    return String(value ?? '')
      .replaceAll('&', '&amp;')
      .replaceAll('<', '&lt;')
      .replaceAll('>', '&gt;')
      .replaceAll('"', '&quot;')
      .replaceAll("'", '&#039;');
  }

  function number(value, fallback = 0) {
    const n = Number(value);
    return Number.isFinite(n) ? n : fallback;
  }

  function nullableNumber(value) {
    if (value === null || value === undefined || value === '') return null;
    const n = Number(value);
    return Number.isFinite(n) ? n : null;
  }

  function formatBytes(bytes) {
    const value = Math.max(0, number(bytes));
    if (!value) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let v = value;
    let i = 0;
    while (v >= 1024 && i < units.length - 1) { v /= 1024; i += 1; }
    const digits = v >= 100 || i === 0 ? 0 : v >= 10 ? 1 : 2;
    return `${v.toFixed(digits)} ${units[i]}`;
  }

  function formatRate(bytes) {
    if (bytes === null || bytes === undefined) return '—';
    return `${formatBytes(bytes)}/s`;
  }

  function formatPercent(value, digits = 0) {
    const n = nullableNumber(value);
    return n === null ? '—' : `${n.toFixed(digits)}%`;
  }

  function formatTemp(value) {
    const n = nullableNumber(value);
    return n === null ? '—' : `${n.toFixed(n >= 100 ? 0 : 1)}°C`;
  }

  function formatUptime(seconds) {
    let remaining = Math.max(0, Math.floor(number(seconds)));
    const days = Math.floor(remaining / 86400); remaining %= 86400;
    const hours = Math.floor(remaining / 3600); remaining %= 3600;
    const minutes = Math.floor(remaining / 60);
    if (days) return `${days}d ${hours}h`;
    if (hours) return `${hours}h ${minutes}m`;
    return `${minutes}m`;
  }

  function localTime(value) {
    if (!value) return '—';
    const d = new Date(value);
    if (Number.isNaN(d.getTime())) return String(value);
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
  }

  function clientID() {
    if (monitor.clientID) return monitor.clientID;
    const key = 'agentgo-monitor-client-id';
    let id = '';
    try { id = sessionStorage.getItem(key) || ''; } catch (_) {}
    if (!id) {
      if (window.crypto && typeof window.crypto.randomUUID === 'function') id = `monitor-${window.crypto.randomUUID()}`;
      else id = `monitor-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
      try { sessionStorage.setItem(key, id); } catch (_) {}
    }
    monitor.clientID = id;
    return id;
  }

  function activelyViewed() {
    return !!(monitor.open && document.visibilityState !== 'hidden');
  }

  async function sendPresence() {
    if (typeof window.api !== 'function') return;
    try {
      const response = await window.api('/api/monitor/presence', {
        method: 'POST',
        body: JSON.stringify({ client_id: clientID(), active: activelyViewed() }),
        timeoutMs: 5000,
      });
      handleAIPresenceSummary(response?.ai);
    } catch (_) {
      // Browser-presence heartbeats are intentionally silent. Connection recovery
      // already owns user-facing web transport warnings elsewhere in AgentGO.
    }
  }

  function sendDisconnect() {
    const payload = JSON.stringify({ client_id: clientID(), active: false });
    try {
      if (navigator.sendBeacon) {
        const blob = new Blob([payload], { type: 'application/json' });
        navigator.sendBeacon('/api/monitor/disconnect', blob);
        return;
      }
    } catch (_) {}
    try { fetch('/api/monitor/disconnect', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: payload, keepalive: true }); } catch (_) {}
  }

  function buildUI() {
    if (document.getElementById('agentGOMonitorOverlay')) {
      monitor.overlay = document.getElementById('agentGOMonitorOverlay');
      monitor.settingsModal = document.getElementById('agentGOMonitorSettings');
      return;
    }
    const releaseText = (document.querySelector('.build-tag')?.textContent || 'AgentGO r475').trim();
    const overlay = document.createElement('div');
    overlay.id = 'agentGOMonitorOverlay';
    overlay.className = 'agentgo-monitor-overlay';
    overlay.setAttribute('aria-hidden', 'true');
    overlay.innerHTML = `
      <div class="monitor-shell">
        <aside class="monitor-sidebar" aria-label="AgentGO Monitor navigation">
          <button class="monitor-side-btn" type="button" data-monitor-nav="desktop"><span class="monitor-side-icon">▣</span><span>Desktop</span></button>
          <button class="monitor-side-btn" type="button" data-monitor-nav="work"><span class="monitor-side-icon">◇</span><span>Work Mode</span></button>
          <button class="monitor-side-btn" type="button" data-monitor-open-settings><span class="monitor-side-icon">⚙</span><span>Settings</span></button>
          <div class="monitor-sidebar-spacer"></div>
          <div class="monitor-build"><strong>${escapeHtml(releaseText)}</strong><span>System Watch</span></div>
        </aside>
        <main class="monitor-main">
          <header class="monitor-topbar">
            <div class="monitor-heading"><div class="monitor-heading-copy"><h2>AgentGO Monitor</h2><p>Watch · Analyze · Keep You Safe</p></div></div>
            <div class="monitor-top-actions">
              <div class="monitor-pill"><button class="monitor-switch" id="monitorTopToggle" type="button" aria-pressed="false" title="Turn AgentGO Monitor on or off"></button><span id="monitorTopToggleText">Monitor: OFF</span><span class="monitor-state-dot" id="monitorStateDot"></span></div>
              <span class="monitor-schedule-badge" id="monitorScheduleBadge" hidden>SCHEDULE ON</span>
              <label class="monitor-pill monitor-model-shell" for="monitorAIModelSelect"><span>AI Model</span><select id="monitorAIModelSelect" aria-label="Monitor AI model"><option value="">None</option></select></label>
            </div>
            <button class="monitor-top-btn monitor-close-btn" id="monitorCloseBtn" type="button" aria-label="Close AgentGO Monitor" title="Back to Desktop">×</button>
          </header>
          <section class="monitor-overview-row">
            <div class="monitor-overview monitor-glass">
              <div class="monitor-panel-title"><span>System Overview</span><small id="monitorRuntimeMode">Monitor off</small></div>
              <div class="monitor-metrics">
                ${metricHTML('cpu','CPU')}
                ${metricHTML('gpu','GPU')}
                ${metricHTML('memory','Memory')}
                ${metricHTML('disk','Disk')}
                ${metricHTML('network','Network')}
              </div>
            </div>
            <div class="monitor-system-info monitor-glass">
              <div class="monitor-panel-title"><span>System Info</span><small>Quick Reference</small></div>
              <div class="monitor-system-info-body" id="monitorSystemInfo"><div class="monitor-empty">Opening system profile…</div></div>
            </div>
          </section>
          <nav class="monitor-tabs" id="monitorTabs" aria-label="AgentGO Monitor tools">
            ${['Processes','Performance','Startup Apps','Services','System Info','Logs','Security','Cheats'].map((label,index)=>`<button class="monitor-tab${index===0?' is-active':''}" type="button" data-monitor-tab="${label.toLowerCase().replaceAll(' ','-')}">${label}</button>`).join('')}
          </nav>
          <section class="monitor-body-grid">
            <div class="monitor-left-stack">
              <section class="monitor-workspace monitor-glass" id="monitorWorkspace"></section>
              <div class="monitor-quick-grid">
                <section class="monitor-quick-card monitor-glass" id="monitorQuickThermalCard" data-quick-slot="thermal"></section>
                <section class="monitor-quick-card monitor-glass" id="monitorQuickCard2" data-quick-slot="2"></section>
                <section class="monitor-quick-card monitor-glass" id="monitorQuickCard3" data-quick-slot="3"></section>
                <section class="monitor-quick-card monitor-glass" id="monitorQuickCard4" data-quick-slot="4"></section>
              </div>
            </div>
            <div class="monitor-right-stack">
              <section class="monitor-event-log monitor-glass">
                <div class="monitor-event-head"><h3>Event Log</h3><select class="monitor-mini-select" id="monitorEventFilter" aria-label="Event filter"><option value="all">All Events</option><option value="system">System</option><option value="process">Process</option><option value="network">Network</option><option value="security">Security</option><option value="performance">Performance</option><option value="thermal">Thermal</option><option value="service">Services</option><option value="startup">Startup</option><option value="warning">Warnings</option><option value="collector">Collector</option></select><button class="monitor-action-btn" id="monitorClearEventsBtn" type="button">Clear</button></div>
                <div class="monitor-event-list" id="monitorEventList"><div class="monitor-empty">No Monitor events yet.</div></div>
              </section>
              <section class="monitor-ai-panel monitor-glass" id="monitorAIPanel">
                <div class="monitor-ai-head"><h3>AI Chat &amp; Reports</h3><span class="monitor-ai-status" id="monitorAIStatus">AI idle</span><div class="monitor-ai-actions"><button class="monitor-ai-head-btn primary" id="monitorReportNowBtn" type="button">Report Now</button><button class="monitor-ai-head-btn" id="monitorScheduleBtn" type="button">Schedule</button><button class="monitor-ai-head-btn maximize" id="monitorAIMaximizeBtn" type="button" aria-label="Maximize AI Chat & Reports" title="Maximize AI Chat & Reports">⛶</button></div></div>
                <div class="monitor-ai-feed" id="monitorAIFeed"><div class="monitor-ai-message monitor-ai-system"><strong>AgentGO Monitor</strong>Select an AI model to enable on-demand questions and system reviews. Scheduled reports are off by default.<span class="monitor-phase-badge">Phase 3 AI analyst</span></div></div>
                <div class="monitor-ai-compose"><input id="monitorAIInput" type="text" placeholder="Ask a question about your system…"/><button class="monitor-ai-send" id="monitorAISendBtn" type="button">➤</button></div>
              </section>
            </div>
          </section>
        </main>
      </div>`;
    document.body.appendChild(overlay);

    const settings = document.createElement('div');
    settings.id = 'agentGOMonitorSettings';
    settings.className = 'monitor-settings-modal';
    settings.setAttribute('aria-hidden','true');
    settings.innerHTML = `
      <div class="monitor-settings-card monitor-glass" role="dialog" aria-modal="true" aria-labelledby="monitorSettingsTitle">
        <div class="monitor-settings-head"><div><h3 id="monitorSettingsTitle">AgentGO Monitor Settings</h3><div class="monitor-setting-help">System monitoring, bounded history, AI analyst memory, and report scheduling controls.</div></div><button class="monitor-settings-close" type="button" data-monitor-settings-close aria-label="Close">×</button></div>
        <div class="monitor-settings-body">
          <div class="monitor-setting-section"><div class="monitor-setting-title">Active System Polling</div><div class="monitor-setting-help">Used while you are actively viewing the Monitor command center.</div><div class="monitor-segments" data-monitor-setting="active">${segmentHTML([2,5,10],2)}</div></div>
          <div class="monitor-setting-section"><div class="monitor-setting-title">Passive System Polling</div><div class="monitor-setting-help">Used while Monitor is on but you are working elsewhere in AgentGO. This helps reduce overhead during gameplay and development.</div><div class="monitor-segments" data-monitor-setting="passive">${segmentHTML([5,10,20],10)}</div></div>
          <div class="monitor-setting-section"><div class="monitor-setting-title">Monitor History</div><div class="monitor-setting-help">AgentGO stores compact telemetry summaries and Monitor events locally. Oldest history is removed first at the selected limit.</div><div class="monitor-segments" data-monitor-setting="history">${historySegmentHTML([5,10,20,50],10)}</div></div>
          <div class="monitor-setting-section"><label class="monitor-setting-title" for="monitorScheduledRunLimitInput">Scheduled AI Run Limit</label><div class="monitor-setting-help">Hard per-session ceiling for automatic scheduled AI reports. Allowed range is 100–1000; default is 100. When reached, automatic reports stay stopped until AgentGO is restarted.</div><div class="monitor-run-limit-row"><input class="monitor-run-limit-input" id="monitorScheduledRunLimitInput" type="number" min="100" max="1000" step="1" inputmode="numeric"/><span>automatic reports / session</span></div></div>
          <div class="monitor-setting-section"><div class="monitor-setting-title">AI Memory Max</div><div class="monitor-setting-help">Hard ceiling for compact Monitor-only UGG memory. AgentGO sends only relevant memory to each AI request, not the entire maximum.</div><div class="monitor-segments" data-monitor-setting="ai-memory">${memorySegmentHTML([20,50,100],20)}</div><div class="monitor-memory-actions"><button class="monitor-action-btn" id="monitorViewMemoryBtn" type="button">View AI Memory</button><button class="monitor-action-btn danger" id="monitorClearMemoryBtn" type="button">Clear AI Memory</button><span id="monitorMemoryUsage">0 B stored</span></div></div>
          <div class="monitor-setting-section"><div class="monitor-setting-title">Hash Reputation · Advanced</div><div class="monitor-setting-help">Check Hash always calculates SHA-256 locally. To optionally query an existing VirusTotal hash report, enter the name of an environment variable containing your VirusTotal API key. AgentGO never uploads the file. Only use API access permitted by your VirusTotal terms/license.</div><label class="monitor-vt-env-label" for="monitorVTEnvInput">API key environment variable</label><input class="monitor-cron-input" id="monitorVTEnvInput" type="text" spellcheck="false" autocomplete="off" placeholder="VIRUSTOTAL_API_KEY"/></div>
          <div class="monitor-setting-section"><div class="monitor-setting-title">Browser Presence</div><div class="monitor-setting-help">If all AgentGO browser clients disconnect, collection and scheduled AI reports pause after the grace period. A forgotten Go server will not make unattended AI calls.</div></div>
          <div class="monitor-settings-actions"><button class="monitor-settings-cancel" type="button" data-monitor-settings-close>Cancel</button><button class="monitor-settings-save" id="monitorSettingsSaveBtn" type="button">Save Settings</button></div>
        </div>
      </div>`;
    document.body.appendChild(settings);

    const schedule = document.createElement('div');
    schedule.id = 'agentGOMonitorSchedule';
    schedule.className = 'monitor-settings-modal';
    schedule.setAttribute('aria-hidden','true');
    schedule.innerHTML = `
      <div class="monitor-settings-card monitor-schedule-card monitor-glass" role="dialog" aria-modal="true" aria-labelledby="monitorScheduleTitle">
        <div class="monitor-settings-head"><div><h3 id="monitorScheduleTitle">Schedule AI Reports</h3><div class="monitor-setting-help">Automatic reviews use the selected Monitor AI model. Scheduling is off by default.</div></div><button class="monitor-settings-close" type="button" data-monitor-schedule-close aria-label="Close">×</button></div>
        <div class="monitor-settings-body">
          <div class="monitor-paid-warning"><strong>Paid model notice:</strong> Frequent scheduled reports can use significant API tokens. Most users will need no more than one automatic report per day, if any. You can always use <strong>Report Now</strong> for an on-demand review.</div>
          <div class="monitor-setting-section"><div class="monitor-setting-title">Report Interval</div><div class="monitor-schedule-segments" data-monitor-schedule-group>${scheduleSegmentHTML()}</div></div>
          <div class="monitor-setting-section monitor-cron-section" id="monitorCronSection"><label class="monitor-setting-title" for="monitorCronInput">Custom Cron</label><input class="monitor-cron-input" id="monitorCronInput" type="text" spellcheck="false" placeholder="5 * * * *"/><div class="monitor-cron-readout" id="monitorCronReadout">Enter a five-field cron expression.</div></div>
          <div class="monitor-settings-actions"><button class="monitor-settings-cancel" type="button" data-monitor-schedule-close>Cancel</button><button class="monitor-settings-save" id="monitorScheduleSaveBtn" type="button">Save Schedule</button></div>
        </div>
      </div>`;
    document.body.appendChild(schedule);

    const memory = document.createElement('div');
    memory.id = 'agentGOMonitorMemory';
    memory.className = 'monitor-settings-modal';
    memory.setAttribute('aria-hidden','true');
    memory.innerHTML = `
      <div class="monitor-settings-card monitor-memory-card monitor-glass" role="dialog" aria-modal="true" aria-labelledby="monitorMemoryTitle">
        <div class="monitor-settings-head"><div><h3 id="monitorMemoryTitle">Monitor AI Memory</h3><div class="monitor-setting-help">Monitor-only durable machine knowledge. Transient telemetry and complete reports do not belong here.</div></div><button class="monitor-settings-close" type="button" data-monitor-memory-close aria-label="Close">×</button></div>
        <textarea class="monitor-memory-viewer" id="monitorMemoryViewer" readonly placeholder="Monitor AI memory is empty."></textarea>
        <div class="monitor-settings-actions"><button class="monitor-settings-save" type="button" data-monitor-memory-close>Close</button></div>
      </div>`;
    document.body.appendChild(memory);

    const itemModal = document.createElement('div');
    itemModal.id = 'agentGOMonitorItem';
    itemModal.className = 'monitor-settings-modal monitor-item-modal';
    itemModal.setAttribute('aria-hidden','true');
    itemModal.innerHTML = `
      <div class="monitor-settings-card monitor-item-card monitor-glass" role="dialog" aria-modal="true" aria-labelledby="monitorItemTitle">
        <div class="monitor-settings-head"><div><h3 id="monitorItemTitle">Selected Item</h3><div class="monitor-setting-help" id="monitorItemSubtitle">Interactive file/process investigation</div></div><button class="monitor-settings-close" type="button" data-monitor-item-close aria-label="Close">×</button></div>
        <div class="monitor-item-modal-body" id="monitorItemModalBody"><div class="monitor-empty">Select an item to inspect.</div></div>
        <div class="monitor-item-modal-actions"><button class="monitor-action-btn" id="monitorItemAIBtn" type="button">Ask AI</button><button class="monitor-action-btn" id="monitorItemHashBtn" type="button">Check Hash</button><button class="monitor-action-btn" id="monitorItemKnownBtn" type="button">Mark Known</button><button class="monitor-action-btn" id="monitorItemCopyPathBtn" type="button">Copy Path</button><button class="monitor-action-btn" id="monitorItemCopyHashBtn" type="button">Copy SHA-256</button><button class="monitor-action-btn" id="monitorItemRevealBtn" type="button">Show in Folder</button><button class="monitor-action-btn" id="monitorItemVTBtn" type="button">Open VirusTotal</button><button class="monitor-settings-save" type="button" data-monitor-item-close>Close</button></div>
      </div>`;
    document.body.appendChild(itemModal);

    monitor.overlay = overlay;
    monitor.settingsModal = settings;
    monitor.scheduleModal = schedule;
    monitor.memoryModal = memory;
    monitor.itemModal = itemModal;
    bindUI();
    renderWorkspace();
  }

  function metricHTML(kind,label) {
    return `<div class="monitor-metric" data-kind="${kind}" data-monitor-overview="${kind}" role="button" tabindex="0" aria-label="Open ${label} details"><div class="monitor-metric-label">${label}</div><div class="monitor-gauge" id="monitorGauge-${kind}"><span class="monitor-gauge-value" id="monitorGaugeValue-${kind}">—</span></div><div class="monitor-metric-meta" id="monitorMetricMeta-${kind}">Unavailable</div><svg class="monitor-spark" id="monitorSpark-${kind}" viewBox="0 0 100 20" preserveAspectRatio="none"><polygon class="monitor-spark-area" points="0,18 100,18"></polygon><line class="monitor-zero-line" x1="0" y1="18" x2="100" y2="18"></line><polyline points="0,18 100,18"></polyline></svg></div>`;
  }

  function segmentHTML(values, selected) {
    return values.map(v=>`<button class="monitor-segment${v===selected?' is-selected':''}" type="button" data-poll-seconds="${v}">${v} sec</button>`).join('');
  }

  function historySegmentHTML(values, selected) {
    return values.map(v=>`<button class="monitor-segment${v===selected?' is-selected':''}" type="button" data-history-mb="${v}">${v} MB</button>`).join('');
  }

  function memorySegmentHTML(values, selected) {
    return values.map(v=>`<button class="monitor-segment${v===selected?' is-selected':''}" type="button" data-ai-memory-kb="${v}">${v} KB</button>`).join('');
  }

  function scheduleSegmentHTML() {
    const items = [['off','Off'],['15m','15 min'],['30m','30 min'],['1h','1 hr'],['6h','6 hr'],['24h','24 hr · Recommended'],['custom','Custom']];
    return items.map(([value,label])=>`<button class="monitor-segment${value==='off'?' is-selected':''}" type="button" data-ai-schedule="${value}">${label}</button>`).join('');
  }

  function bindUI() {
    monitor.overlay.addEventListener('click', event => {
      const nav = event.target.closest('[data-monitor-nav]');
      if (nav) {
        const target = nav.getAttribute('data-monitor-nav');
        if (target === 'desktop') closeMonitor();
        if (target === 'work') {
          closeMonitor();
          if (typeof window.startWorkModeFromYetiMenu === 'function') window.startWorkModeFromYetiMenu();
        }
        return;
      }
      if (event.target.closest('[data-monitor-open-settings]')) return openSettings();
      if (event.target.closest('#monitorCloseBtn')) return closeMonitor();
      if (event.target.closest('#monitorTopToggle')) return toggleMonitorEnabled();
      const quickCycle = event.target.closest('[data-quick-cycle]');
      if (quickCycle) {
        const slot = String(quickCycle.getAttribute('data-quick-slot') || '');
        const direction = number(quickCycle.getAttribute('data-quick-cycle'), 1) < 0 ? -1 : 1;
        return cycleQuickCard(slot, direction);
      }
      const tab = event.target.closest('[data-monitor-tab]');
      if (tab) return selectTab(tab.getAttribute('data-monitor-tab') || 'processes');
      const overview = event.target.closest('[data-monitor-overview]');
      if (overview) return openOverviewDetail(overview.getAttribute('data-monitor-overview') || 'cpu');
      const cheatCategory = event.target.closest('[data-cheat-category]');
      if (cheatCategory) {
        monitor.cheatCategory = String(cheatCategory.getAttribute('data-cheat-category') || '');
        monitor.tabFilter.cheats = '';
        renderWorkspace();
        return;
      }
      if (event.target.closest('[data-cheat-back]')) {
        monitor.cheatCategory = '';
        monitor.tabFilter.cheats = '';
        renderWorkspace();
        return;
      }
      const cheatCopy = event.target.closest('[data-cheat-copy]');
      if (cheatCopy) return copyText(cheatCopy.getAttribute('data-cheat-copy') || '', 'Command');
      const sortHeader = event.target.closest('[data-monitor-sort-key]');
      if (sortHeader) {
        const table = String(sortHeader.getAttribute('data-monitor-sort-table') || '');
        const key = String(sortHeader.getAttribute('data-monitor-sort-key') || '');
        if (table && key) {
          const previous = monitor.tableSort[table];
          monitor.tableSort[table] = { key, direction: previous?.key === key && previous?.direction === 'asc' ? 'desc' : 'asc' };
          renderWorkspace();
        }
        return;
      }
      const diskGroup = event.target.closest('[data-monitor-disk-group]');
      if (diskGroup) {
        monitor.diskGroup = String(diskGroup.getAttribute('data-monitor-disk-group') || 'all');
        renderWorkspace();
        return;
      }
      const processRow = event.target.closest('[data-monitor-process-pid]');
      if (processRow) {
        monitor.selectedPID = number(processRow.getAttribute('data-monitor-process-pid'));
        monitor.selectedItemRef = processItemRef(monitor.selectedPID);
        monitor.itemDetail = null;
        renderWorkspace();
        return;
      }
      const startupRow = event.target.closest('[data-monitor-startup-index]');
      if (startupRow) {
        monitor.selectedItemRef = startupItemRef(number(startupRow.getAttribute('data-monitor-startup-index'),-1));
        monitor.itemDetail = null;
        renderWorkspace();
        return;
      }
      const serviceRow = event.target.closest('[data-monitor-service-index]');
      if (serviceRow) {
        monitor.selectedItemRef = serviceItemRef(number(serviceRow.getAttribute('data-monitor-service-index'),-1));
        monitor.itemDetail = null;
        renderWorkspace();
        return;
      }
      const networkRow = event.target.closest('[data-monitor-network-index]');
      if (networkRow) {
        const ref = networkItemRef(number(networkRow.getAttribute('data-monitor-network-index'),-1));
        if (ref?.pid) {
          monitor.selectedPID = number(ref.pid);
          monitor.selectedItemRef = processItemRef(monitor.selectedPID) || ref;
          monitor.itemDetail = null;
          selectTab('processes');
        } else {
          monitor.selectedItemRef = ref;
          monitor.itemDetail = null;
          renderWorkspace();
        }
        return;
      }
      const inspect = event.target.closest('[data-monitor-security-index]');
      if (inspect) {
        event.stopPropagation();
        monitor.selectedItemRef = securityItemRef(number(inspect.getAttribute('data-monitor-security-index'),-1));
        monitor.itemDetail = null;
        return openSelectedItemModal();
      }
      const action = event.target.closest('[data-monitor-item-action]');
      if (action) {
        const name = String(action.getAttribute('data-monitor-item-action') || '');
        if (name === 'ai') return askAIAboutSelectedItem();
        if (name === 'hash') return checkSelectedItemHash();
        if (name === 'reveal') return showSelectedItemInFolder();
        if (name === 'virustotal') return openVirusTotalForSelected();
        if (name === 'known') return markSelectedItemKnown();
        if (name === 'details') return openSelectedItemModal();
      }
      const advanced = event.target.closest('#monitorAdvancedProcessBtn');
      if (advanced) {
        monitor.detailsOpen = !monitor.detailsOpen;
        renderWorkspace();
      }
    });
    monitor.overlay.addEventListener('input', event => {
      if (event.target.id === 'monitorProcessSearch') {
        monitor.processFilter = String(event.target.value || '').trim().toLowerCase();
        renderProcessRows();
        return;
      }
      if (event.target.matches('[data-monitor-tab-search]')) {
        const value = String(event.target.value || '');
        monitor.tabFilter[monitor.tab] = value;
        const cursor = event.target.selectionStart ?? value.length;
        renderWorkspace();
        const field = monitor.overlay.querySelector('[data-monitor-tab-search]');
        if (field) {
          field.focus();
          try { field.setSelectionRange(cursor,cursor); } catch (_) {}
        }
      }
    });
    monitor.overlay.addEventListener('change', event => {
      if (event.target.id === 'monitorEventFilter') renderEvents();
      if (event.target.id === 'monitorAIModelSelect') selectMonitorAIModel(event.target.value);
    });
    monitor.overlay.addEventListener('mouseover', event => {
      const cell = event.target.closest?.('[data-disk-hover-value]');
      if (!cell) return;
      const readout = document.getElementById('monitorDiskHoverReadout');
      if (!readout) return;
      const label = String(cell.getAttribute('data-disk-hover-label') || 'Value');
      const value = String(cell.getAttribute('data-disk-hover-value') || '—');
      readout.textContent = `${label}: ${value}`;
      readout.classList.add('is-active');
    });
    monitor.overlay.addEventListener('mouseout', event => {
      const cell = event.target.closest?.('[data-disk-hover-value]');
      if (!cell || cell.contains(event.relatedTarget)) return;
      const readout = document.getElementById('monitorDiskHoverReadout');
      if (!readout) return;
      readout.textContent = '';
      readout.classList.remove('is-active');
    });
    monitor.overlay.addEventListener('keydown', event => {
      const sortHeader = event.target.closest?.('[data-monitor-sort-key]');
      if (sortHeader && (event.key === 'Enter' || event.key === ' ')) {
        event.preventDefault();
        sortHeader.click();
        return;
      }
      const overview = event.target.closest?.('[data-monitor-overview]');
      if (overview && (event.key === 'Enter' || event.key === ' ')) {
        event.preventDefault();
        openOverviewDetail(overview.getAttribute('data-monitor-overview') || 'cpu');
        return;
      }
      const cheatCopy = event.target.closest?.('[data-cheat-copy]');
      if (cheatCopy && (event.key === 'Enter' || event.key === ' ')) {
        event.preventDefault();
        copyText(cheatCopy.getAttribute('data-cheat-copy') || '', 'Command');
        return;
      }
      if (event.target.id === 'monitorAIInput' && event.key === 'Enter' && !event.shiftKey) {
        event.preventDefault();
        sendMonitorAIQuestion();
      }
    });
    monitor.overlay.addEventListener('click', event => {
      if (event.target.closest('#monitorReportNowBtn')) return reportNow();
      if (event.target.closest('#monitorScheduleBtn')) return openSchedule();
      if (event.target.closest('#monitorAIMaximizeBtn')) return toggleAIMaximize();
      if (event.target.closest('#monitorAISendBtn')) return sendMonitorAIQuestion();
    });
    monitor.settingsModal.addEventListener('click', event => {
      if (event.target === monitor.settingsModal || event.target.closest('[data-monitor-settings-close]')) return closeSettings();
      const historySegment = event.target.closest('[data-history-mb]');
      if (historySegment) {
        const group = historySegment.closest('[data-monitor-setting="history"]');
        if (!group) return;
        group.querySelectorAll('.monitor-segment').forEach(btn => btn.classList.remove('is-selected'));
        historySegment.classList.add('is-selected');
        monitor.stagedHistoryMax = number(historySegment.getAttribute('data-history-mb'),10);
        return;
      }
      const memorySegment = event.target.closest('[data-ai-memory-kb]');
      if (memorySegment) {
        const group = memorySegment.closest('[data-monitor-setting="ai-memory"]');
        if (!group) return;
        group.querySelectorAll('.monitor-segment').forEach(btn => btn.classList.remove('is-selected'));
        memorySegment.classList.add('is-selected');
        monitor.stagedAIMemoryMax = number(memorySegment.getAttribute('data-ai-memory-kb'),20);
        return;
      }
      if (event.target.closest('#monitorViewMemoryBtn')) return openMemoryViewer();
      if (event.target.closest('#monitorClearMemoryBtn')) return clearAIMemory();
      const segment = event.target.closest('[data-poll-seconds]');
      if (segment) {
        const group = segment.closest('[data-monitor-setting]');
        if (!group) return;
        group.querySelectorAll('.monitor-segment').forEach(btn => btn.classList.remove('is-selected'));
        segment.classList.add('is-selected');
        const seconds = number(segment.getAttribute('data-poll-seconds'));
        if (group.getAttribute('data-monitor-setting') === 'active') monitor.stagedActivePoll = seconds;
        else monitor.stagedPassivePoll = seconds;
        return;
      }
      if (event.target.closest('#monitorSettingsSaveBtn')) saveSettingsFromModal();
    });
    monitor.scheduleModal.addEventListener('click', event => {
      if (event.target === monitor.scheduleModal || event.target.closest('[data-monitor-schedule-close]')) return closeSchedule();
      const segment = event.target.closest('[data-ai-schedule]');
      if (segment) {
        monitor.stagedSchedule = String(segment.getAttribute('data-ai-schedule') || 'off');
        setSelectedSchedule(monitor.stagedSchedule);
        updateCronUI();
        return;
      }
      if (event.target.closest('#monitorScheduleSaveBtn')) return saveSchedule();
    });
    monitor.scheduleModal.addEventListener('input', event => {
      if (event.target.id === 'monitorCronInput') {
        monitor.stagedCron = String(event.target.value || '').trim();
        updateCronUI();
      }
    });
    monitor.memoryModal.addEventListener('click', event => {
      if (event.target === monitor.memoryModal || event.target.closest('[data-monitor-memory-close]')) return closeMemoryViewer();
    });
    monitor.itemModal.addEventListener('click', event => {
      if (event.target === monitor.itemModal || event.target.closest('[data-monitor-item-close]')) return closeSelectedItemModal();
      if (event.target.closest('#monitorItemAIBtn')) return askAIAboutSelectedItem();
      if (event.target.closest('#monitorItemHashBtn')) return checkSelectedItemHash();
      if (event.target.closest('#monitorItemKnownBtn')) return markSelectedItemKnown();
      if (event.target.closest('#monitorItemCopyPathBtn')) return copySelectedItemPath();
      if (event.target.closest('#monitorItemCopyHashBtn')) return copySelectedItemHash();
      if (event.target.closest('#monitorItemRevealBtn')) return showSelectedItemInFolder();
      if (event.target.closest('#monitorItemVTBtn')) return openVirusTotalForSelected();
    });
    document.getElementById('monitorClearEventsBtn')?.addEventListener('click', clearEvents);
  }

  function settingsFromState() {
    return monitor.state?.settings || { enabled:false, active_poll_seconds:2, passive_poll_seconds:10, history_max_mb:10, ai_model_id:'', ai_report_schedule:'off', ai_report_cron:'', ai_scheduled_run_limit:100, ai_memory_max_kb:20, virustotal_api_key_env:'VIRUSTOTAL_API_KEY', thermal_card_view:'overview', quick_card_2:'top-load', quick_card_3:'network-ports', quick_card_4:'security' };
  }

  function openSettings() {
    const settings = settingsFromState();
    monitor.stagedActivePoll = number(settings.active_poll_seconds,2);
    monitor.stagedPassivePoll = number(settings.passive_poll_seconds,10);
    monitor.stagedHistoryMax = number(settings.history_max_mb,10);
    monitor.stagedScheduledRunLimit = number(settings.ai_scheduled_run_limit,100);
    monitor.stagedAIMemoryMax = number(settings.ai_memory_max_kb,20);
    monitor.stagedVTEnv = String(settings.virustotal_api_key_env || 'VIRUSTOTAL_API_KEY');
    setSelectedSegment('active', monitor.stagedActivePoll);
    setSelectedSegment('passive', monitor.stagedPassivePoll);
    setSelectedHistorySegment(monitor.stagedHistoryMax);
    setSelectedMemorySegment(monitor.stagedAIMemoryMax);
    updateMemoryUsageLabel();
    const runLimit = document.getElementById('monitorScheduledRunLimitInput');
    if (runLimit) runLimit.value = String(monitor.stagedScheduledRunLimit);
    const vtEnv = document.getElementById('monitorVTEnvInput');
    if (vtEnv) vtEnv.value = monitor.stagedVTEnv;
    monitor.settingsModal.classList.add('open');
    monitor.settingsModal.setAttribute('aria-hidden','false');
  }

  function closeSettings() {
    if (!monitor.settingsModal) return;
    monitor.settingsModal.classList.remove('open');
    monitor.settingsModal.setAttribute('aria-hidden','true');
  }

  function setSelectedSegment(kind, value) {
    const group = monitor.settingsModal.querySelector(`[data-monitor-setting="${kind}"]`);
    if (!group) return;
    group.querySelectorAll('[data-poll-seconds]').forEach(btn => btn.classList.toggle('is-selected', number(btn.getAttribute('data-poll-seconds')) === number(value)));
  }

  function setSelectedHistorySegment(value) {
    const group = monitor.settingsModal.querySelector('[data-monitor-setting="history"]');
    if (!group) return;
    group.querySelectorAll('[data-history-mb]').forEach(btn => btn.classList.toggle('is-selected', number(btn.getAttribute('data-history-mb')) === number(value)));
  }

  function setSelectedMemorySegment(value) {
    const group = monitor.settingsModal.querySelector('[data-monitor-setting="ai-memory"]');
    if (!group) return;
    group.querySelectorAll('[data-ai-memory-kb]').forEach(btn => btn.classList.toggle('is-selected', number(btn.getAttribute('data-ai-memory-kb')) === number(value)));
  }

  function currentSettingsPayload(overrides = {}) {
    const settings = settingsFromState();
    return {
      enabled: !!settings.enabled,
      active_poll_seconds: number(settings.active_poll_seconds,2),
      passive_poll_seconds: number(settings.passive_poll_seconds,10),
      history_max_mb: number(settings.history_max_mb,10),
      ai_model_id: String(settings.ai_model_id || ''),
      ai_report_schedule: String(settings.ai_report_schedule || 'off'),
      ai_report_cron: String(settings.ai_report_cron || ''),
      ai_scheduled_run_limit: number(settings.ai_scheduled_run_limit,100),
      ai_memory_max_kb: number(settings.ai_memory_max_kb,20),
      virustotal_api_key_env: String(settings.virustotal_api_key_env || 'VIRUSTOTAL_API_KEY'),
      thermal_card_view: String(settings.thermal_card_view || 'overview'),
      quick_card_2: String(settings.quick_card_2 || 'top-load'),
      quick_card_3: String(settings.quick_card_3 || 'network-ports'),
      quick_card_4: String(settings.quick_card_4 || 'security'),
      ...overrides,
    };
  }

  async function saveSettingsFromModal() {
    const runLimitRaw = String(document.getElementById('monitorScheduledRunLimitInput')?.value || '').trim();
    const runLimit = Number(runLimitRaw);
    if (!Number.isInteger(runLimit) || runLimit < 100 || runLimit > 1000) {
      if (typeof window.showToast === 'function') window.showToast('Scheduled AI Run Limit must be a whole number from 100 to 1000.', 'warning', 4200);
      return;
    }
    monitor.stagedScheduledRunLimit = runLimit;
    const vtEnv = String(document.getElementById('monitorVTEnvInput')?.value || 'VIRUSTOTAL_API_KEY').trim() || 'VIRUSTOTAL_API_KEY';
    monitor.stagedVTEnv = vtEnv;
    const ok = await saveSettings(currentSettingsPayload({ active_poll_seconds: monitor.stagedActivePoll, passive_poll_seconds: monitor.stagedPassivePoll, history_max_mb: monitor.stagedHistoryMax, ai_scheduled_run_limit: monitor.stagedScheduledRunLimit, ai_memory_max_kb: monitor.stagedAIMemoryMax, virustotal_api_key_env: vtEnv }), { toast:true });
    if (ok) closeSettings();
  }

  async function toggleMonitorEnabled() {
    const settings = settingsFromState();
    await saveSettings(currentSettingsPayload({ enabled: !settings.enabled }), { toast:true });
  }

  async function saveSettings(next, options = {}) {
    try {
      monitor.state = await window.api('/api/monitor/settings', { method:'POST', body:JSON.stringify(next), timeoutMs:10000 });
      renderState();
      await sendPresence();
      scheduleStatePoll();
      if (options.toast && typeof window.showToast === 'function') window.showToast(next.enabled ? 'AgentGO Monitor settings saved.' : 'AgentGO Monitor is off.');
      return true;
    } catch (err) {
      if (typeof window.showToast === 'function') window.showToast(err?.message || 'Could not save Monitor settings.', 'warning', 3600);
      return false;
    }
  }

  function notificationStorageKey() { return 'agentgo-monitor-last-notification'; }

  function rememberedNotificationID() {
    if (monitor.lastNotificationID) return monitor.lastNotificationID;
    try { monitor.lastNotificationID = number(sessionStorage.getItem(notificationStorageKey()),0); } catch (_) {}
    return monitor.lastNotificationID;
  }

  function rememberNotificationID(id) {
    monitor.lastNotificationID = Math.max(monitor.lastNotificationID, number(id));
    try { sessionStorage.setItem(notificationStorageKey(), String(monitor.lastNotificationID)); } catch (_) {}
  }

  function handleAIPresenceSummary(summary) {
    if (!summary) return;
    const seq = number(summary.sequence,0);
    if (seq && seq !== monitor.lastAISequence) {
      monitor.lastAISequence = seq;
      if (monitor.open) loadAIState({ silent:true });
    }
    const notification = summary.notification;
    const notificationID = number(notification?.id,0);
    if (notificationID > rememberedNotificationID()) {
      rememberNotificationID(notificationID);
      const message = String(notification?.message || 'AI System Monitor Warning Arrived');
      if (typeof window.showToast === 'function') window.showToast(message, 'error', 6500);
    }
  }

  async function loadAIModels() {
    const select = document.getElementById('monitorAIModelSelect');
    if (!select || typeof window.api !== 'function') return;
    try {
      const models = await window.api('/api/models', { timeoutMs:10000 });
      const eligible = (Array.isArray(models) ? models : []).filter(model => !model.isVideoGeneration && !model.isMeshGeneration);
      const selected = String(settingsFromState().ai_model_id || '');
      select.innerHTML = '<option value="">None · Manual monitor only</option>' + eligible.map(model => `<option value="${escapeHtml(model.id)}">${escapeHtml(model.label || `Model ${model.id}`)}</option>`).join('');
      select.value = eligible.some(model => String(model.id) === selected) ? selected : '';
      renderAIControls();
    } catch (_) {
      select.innerHTML = '<option value="">No model list available</option>';
      select.value = '';
    }
  }

  async function selectMonitorAIModel(modelID) {
    const ok = await saveSettings(currentSettingsPayload({ ai_model_id:String(modelID || '') }), { toast:false });
    if (ok) {
      await loadAIState({ silent:true });
      renderAIControls();
    }
  }

  function aiModelSelected() {
    return !!String(settingsFromState().ai_model_id || '').trim();
  }

  function renderAIControls() {
    const busy = !!monitor.aiState?.summary?.busy;
    const hasModel = aiModelSelected();
    const report = document.getElementById('monitorReportNowBtn');
    const send = document.getElementById('monitorAISendBtn');
    const input = document.getElementById('monitorAIInput');
    if (report) report.disabled = !hasModel || busy;
    if (send) send.disabled = !hasModel || busy;
    if (input) input.disabled = !hasModel || busy;
    const aiSummary = monitor.aiState?.summary || {};
    const stateSummary = monitor.state?.ai || {};
    const summary = number(stateSummary.sequence,0) > number(aiSummary.sequence,0) ? stateSummary : (monitor.aiState?.summary || monitor.state?.ai || {});
    const settings = settingsFromState();
    const scheduleValue = String(settings.ai_report_schedule || 'off').trim().toLowerCase();
    const scheduleActive = scheduleValue !== '' && scheduleValue !== 'off';
    const limitReached = !!summary.schedule_limit_reached;
    const schedule = monitor.aiState?.schedule_description || monitor.state?.ai?.schedule_description || 'Off';
    const scheduleBadge = document.getElementById('monitorScheduleBadge');
    if (scheduleBadge) {
      scheduleBadge.hidden = !scheduleActive;
      scheduleBadge.textContent = limitReached ? 'SCHEDULE LIMIT REACHED' : 'SCHEDULE ON';
      scheduleBadge.classList.toggle('is-limit-reached', limitReached);
      const runs = number(summary.scheduled_runs,0);
      const limit = number(summary.scheduled_run_limit, number(settings.ai_scheduled_run_limit,100));
      scheduleBadge.title = limitReached ? `Automatic reports stopped at ${runs}/${limit} for this AgentGO session.` : `Automatic AI schedule is configured. Session use: ${runs}/${limit}.`;
    }
    const status = document.getElementById('monitorAIStatus');
    if (status) {
      const last = String(summary.last_report_status || '').trim();
      const scheduleLabel = limitReached ? 'LIMIT REACHED' : schedule;
      const scheduleHTML = scheduleActive ? `<span class="monitor-ai-schedule-active">${escapeHtml(scheduleLabel)}</span>` : escapeHtml(scheduleLabel);
      status.innerHTML = busy ? 'AI reviewing…' : last ? `Last: ${escapeHtml(last.toUpperCase())} · ${scheduleHTML}` : `Schedule: ${scheduleHTML}`;
      status.dataset.status = last || 'normal';
    }
  }

  function renderAIContent(content) {
    const raw = String(content || '');
    try {
      if (typeof window.renderKnowledgeMarkdown === 'function') return window.renderKnowledgeMarkdown(raw);
    } catch (_) {}
    return escapeHtml(raw).replaceAll('\n','<br>');
  }

  function renderAIState() {
    const feed = document.getElementById('monitorAIFeed');
    if (!feed) return;
    const previousTop = feed.scrollTop;
    const wasNearBottom = feed.scrollHeight - feed.clientHeight - feed.scrollTop < 48;
    const messages = Array.isArray(monitor.aiState?.conversation) ? monitor.aiState.conversation : [];
    if (!messages.length) {
      feed.innerHTML = aiModelSelected()
        ? ''
        : '<div class="monitor-ai-message monitor-ai-system"><strong>AgentGO Monitor</strong>Select an AI model, then ask a system question or run <b>Report Now</b>. Scheduled reports are off by default.<span class="monitor-phase-badge">Phase 3 AI analyst</span></div>';
    } else {
      feed.innerHTML = messages.slice(-80).map(message => {
        const role = String(message.role || 'assistant');
        const status = String(message.status || 'normal').toLowerCase();
        const label = role === 'user' ? 'You' : message.kind === 'report' ? (message.scheduled ? 'Scheduled AI Report' : 'AI System Report') : role === 'system' ? 'AgentGO Monitor' : (message.model_label || 'Monitor AI');
        const statusBadge = message.kind === 'report' || role === 'assistant' ? `<span class="monitor-ai-result-status" data-status="${escapeHtml(status)}">${escapeHtml(status.toUpperCase())}</span>` : '';
        return `<article class="monitor-ai-message monitor-ai-${escapeHtml(role)}" data-status="${escapeHtml(status)}"><div class="monitor-ai-message-meta"><strong>${escapeHtml(label)}</strong>${statusBadge}<time>${escapeHtml(localTime(message.time))}</time></div><div class="monitor-ai-message-body">${renderAIContent(message.content)}</div></article>`;
      }).join('');
      feed.scrollTop = wasNearBottom ? feed.scrollHeight : previousTop;
    }
    handleAIPresenceSummary(monitor.aiState?.summary);
    updateMemoryUsageLabel();
    renderAIControls();
  }

  async function loadAIState(options = {}) {
    if (typeof window.api !== 'function') return;
    try {
      monitor.aiState = await window.api('/api/monitor/ai/state', { timeoutMs:10000 });
      monitor.lastAISequence = number(monitor.aiState?.summary?.sequence, monitor.lastAISequence);
      renderAIState();
    } catch (err) {
      if (!options.silent && typeof window.showToast === 'function') window.showToast(err?.message || 'Could not load Monitor AI state.', 'warning', 3600);
    }
  }

  function setAIWorking(working, label = 'AI reviewing…') {
    if (!monitor.aiState) monitor.aiState = { summary:{} };
    if (!monitor.aiState.summary) monitor.aiState.summary = {};
    monitor.aiState.summary.busy = !!working;
    const status = document.getElementById('monitorAIStatus');
    if (status && working) status.textContent = label;
    renderAIControls();
  }

  async function reportNow() {
    if (!aiModelSelected()) {
      if (typeof window.showToast === 'function') window.showToast('Select a Monitor AI model first.', 'warning', 3200);
      return;
    }
    setAIWorking(true,'AI reviewing system…');
    try {
      const response = await window.api('/api/monitor/ai/report', { method:'POST', body:'{}', timeoutMs:600000 });
      if (response?.warning && typeof window.showToast === 'function') window.showToast(response.warning,'warning',4200);
      await Promise.all([loadAIState({ silent:true }), loadState({ reschedule:false })]);
    } catch (err) {
      if (typeof window.showToast === 'function') window.showToast(err?.message || 'Monitor AI report failed.', 'warning', 4600);
      await loadAIState({ silent:true });
    } finally {
      setAIWorking(false);
      scheduleStatePoll();
    }
  }

  async function sendMonitorAIQuestion() {
    const input = document.getElementById('monitorAIInput');
    const prompt = String(input?.value || '').trim();
    if (!prompt) return;
    if (!aiModelSelected()) {
      if (typeof window.showToast === 'function') window.showToast('Select a Monitor AI model first.', 'warning', 3200);
      return;
    }
    if (input) input.value = '';
    setAIWorking(true,'AI answering…');
    try {
      const response = await window.api('/api/monitor/ai/chat', { method:'POST', body:JSON.stringify({ prompt }), timeoutMs:600000 });
      if (response?.warning && typeof window.showToast === 'function') window.showToast(response.warning,'warning',4200);
      await loadAIState({ silent:true });
    } catch (err) {
      if (typeof window.showToast === 'function') window.showToast(err?.message || 'Monitor AI question failed.', 'warning', 4600);
      await loadAIState({ silent:true });
    } finally {
      setAIWorking(false);
    }
  }

  function toggleAIMaximize(force) {
    monitor.aiExpanded = typeof force === 'boolean' ? force : !monitor.aiExpanded;
    const panel = document.getElementById('monitorAIPanel');
    panel?.classList.toggle('is-maximized',monitor.aiExpanded);
    const button = document.getElementById('monitorAIMaximizeBtn');
    if (button) {
      button.textContent = monitor.aiExpanded ? '↙' : '⛶';
      button.title = monitor.aiExpanded ? 'Restore AI Chat & Reports' : 'Maximize AI Chat & Reports';
    }
    if (monitor.aiExpanded) setTimeout(()=>document.getElementById('monitorAIInput')?.focus(),0);
  }

  function setSelectedSchedule(value) {
    monitor.scheduleModal?.querySelectorAll('[data-ai-schedule]').forEach(btn => btn.classList.toggle('is-selected',btn.getAttribute('data-ai-schedule') === value));
  }

  function cronDescription(expr) {
    const parts = String(expr || '').trim().split(/\s+/).filter(Boolean);
    if (parts.length !== 5) return { valid:false, text:'Cron needs 5 fields: minute hour day month weekday.' };
    const [minute,hour,day,month,week] = parts;
    const allowed = /^[0-9*\/,-]+$/;
    if (!parts.every(part => allowed.test(part))) return { valid:false, text:'Use numbers, *, ranges, lists, and /steps only.' };
    if (day === '*' && month === '*' && week === '*') {
      if (/^\*\/\d+$/.test(minute) && hour === '*') return { valid:true, text:`Every ${minute.slice(2)} minutes` };
      if (/^\d+$/.test(minute) && hour === '*') return { valid:true, text:`At minute ${minute} of every hour` };
      if (minute === '0' && /^\*\/\d+$/.test(hour)) return { valid:true, text:`Every ${hour.slice(2)} hours` };
      if (/^\d+$/.test(minute) && /^\d+$/.test(hour)) {
        const d = new Date(); d.setHours(number(hour),number(minute),0,0);
        return { valid:true, text:`Daily at ${d.toLocaleTimeString([], { hour:'numeric', minute:'2-digit' })}` };
      }
    }
    return { valid:true, text:`Custom cron: ${parts.join(' ')}` };
  }

  function updateCronUI() {
    const section = document.getElementById('monitorCronSection');
    const input = document.getElementById('monitorCronInput');
    const readout = document.getElementById('monitorCronReadout');
    const custom = monitor.stagedSchedule === 'custom';
    section?.classList.toggle('is-visible',custom);
    if (input && input.value !== monitor.stagedCron) input.value = monitor.stagedCron;
    if (readout) {
      const result = cronDescription(monitor.stagedCron);
      readout.textContent = custom ? result.text : 'Custom cron is disabled for this preset.';
      readout.classList.toggle('is-error',custom && !result.valid);
    }
    const warning = monitor.scheduleModal?.querySelector('.monitor-paid-warning');
    warning?.classList.toggle('is-frequent',['15m','30m','1h'].includes(monitor.stagedSchedule));
  }

  function openSchedule() {
    const settings = settingsFromState();
    monitor.stagedSchedule = String(settings.ai_report_schedule || 'off');
    monitor.stagedCron = String(settings.ai_report_cron || '');
    setSelectedSchedule(monitor.stagedSchedule);
    const input = document.getElementById('monitorCronInput');
    if (input) input.value = monitor.stagedCron;
    updateCronUI();
    monitor.scheduleModal.classList.add('open');
    monitor.scheduleModal.setAttribute('aria-hidden','false');
  }

  function closeSchedule() {
    if (!monitor.scheduleModal) return;
    monitor.scheduleModal.classList.remove('open');
    monitor.scheduleModal.setAttribute('aria-hidden','true');
  }

  async function saveSchedule() {
    if (monitor.stagedSchedule !== 'off' && !aiModelSelected()) {
      if (typeof window.showToast === 'function') window.showToast('Select a Monitor AI model before enabling scheduled reports.','warning',4200);
      return;
    }
    if (monitor.stagedSchedule === 'custom') {
      const result = cronDescription(monitor.stagedCron);
      if (!result.valid) {
        if (typeof window.showToast === 'function') window.showToast(result.text,'warning',4200);
        return;
      }
    }
    const ok = await saveSettings(currentSettingsPayload({ ai_report_schedule:monitor.stagedSchedule, ai_report_cron:monitor.stagedCron }), { toast:false });
    if (ok) {
      closeSchedule();
      await loadAIState({ silent:true });
      if (typeof window.showToast === 'function') window.showToast(`AI report schedule: ${monitor.stagedSchedule === 'off' ? 'Off' : monitor.aiState?.schedule_description || monitor.stagedSchedule}.`);
    }
  }

  function updateMemoryUsageLabel() {
    const label = document.getElementById('monitorMemoryUsage');
    if (!label) return;
    const bytes = number(monitor.aiState?.memory_bytes,0);
    const maxKB = number(settingsFromState().ai_memory_max_kb,20);
    label.textContent = `${formatBytes(bytes)} stored · ${maxKB} KB max`;
  }

  async function openMemoryViewer() {
    try {
      const result = await window.api('/api/monitor/ai/memory', { timeoutMs:10000 });
      const viewer = document.getElementById('monitorMemoryViewer');
      if (viewer) viewer.value = String(result?.memory || '');
      monitor.memoryModal.classList.add('open');
      monitor.memoryModal.setAttribute('aria-hidden','false');
    } catch (err) {
      if (typeof window.showToast === 'function') window.showToast(err?.message || 'Could not read Monitor AI memory.','warning',3600);
    }
  }

  function closeMemoryViewer() {
    if (!monitor.memoryModal) return;
    monitor.memoryModal.classList.remove('open');
    monitor.memoryModal.setAttribute('aria-hidden','true');
  }

  async function clearAIMemory() {
    let confirmed = false;
    if (typeof window.openConfirmModal === 'function') confirmed = await window.openConfirmModal({ title:'Clear Monitor AI Memory?', message:'This clears only AgentGO Monitor AI memory. Telemetry history, reports, settings, and other AgentGO memory remain unchanged.', confirmText:'Clear Memory' });
    else confirmed = window.confirm('Clear AgentGO Monitor AI memory?');
    if (!confirmed) return;
    try {
      await window.api('/api/monitor/ai/memory', { method:'DELETE', body:'{}', timeoutMs:10000 });
      await loadAIState({ silent:true });
      if (typeof window.showToast === 'function') window.showToast('Monitor AI memory cleared.');
    } catch (err) {
      if (typeof window.showToast === 'function') window.showToast(err?.message || 'Could not clear Monitor AI memory.','warning',3600);
    }
  }

  function selectTab(tab) {
    if (tab === 'cheats' && (monitor.tab !== 'cheats' || monitor.cheatCategory)) {
      monitor.cheatCategory = '';
      monitor.tabFilter.cheats = '';
    }
    monitor.tab = tab;
    monitor.overlay.querySelectorAll('[data-monitor-tab]').forEach(btn => btn.classList.toggle('is-active', btn.getAttribute('data-monitor-tab') === tab));
    renderWorkspace();
  }

  function openOverviewDetail(kind) {
    const target = ({ cpu:'cpu-detail', gpu:'gpu-detail', memory:'memory-detail', disk:'disk-detail', network:'network' })[String(kind || '').toLowerCase()];
    if (!target) return;
    monitor.tab = target;
    monitor.overlay.querySelectorAll('[data-monitor-tab]').forEach(btn => btn.classList.remove('is-active'));
    renderWorkspace();
    requestAnimationFrame(() => document.getElementById('monitorWorkspace')?.scrollIntoView({ behavior:'smooth', block:'start' }));
  }

  function workspaceHead(title, options = {}) {
    const updated = monitor.state?.snapshot?.deep_captured_at || monitor.state?.snapshot?.timestamp || '';
    const search = options.search ? `<input class="monitor-process-search monitor-tab-search" data-monitor-tab-search type="search" value="${escapeHtml(monitor.tabFilter[monitor.tab] || '')}" placeholder="${escapeHtml(options.placeholder || 'Search…')}"/>` : '';
    const hoverDetail = options.hoverDetail ? '<div class="monitor-workspace-hover-detail" id="monitorDiskHoverReadout"></div>' : '';
    return `<div class="monitor-workspace-head"><div class="monitor-workspace-title">${escapeHtml(title)}</div>${hoverDetail}${search}<div class="monitor-last-update">${updated ? `Updated ${escapeHtml(localTime(updated))}` : 'Waiting for data'}</div></div>`;
  }

  function currentTabFilter() {
    return String(monitor.tabFilter[monitor.tab] || '').trim().toLowerCase();
  }

  function rowMatchesFilter(values) {
    const q = currentTabFilter();
    if (!q) return true;
    return values.filter(v => v !== null && v !== undefined).join(' ').toLowerCase().includes(q);
  }

  function tableSortHeader(table, key, label, style = '') {
    const state = monitor.tableSort[table];
    const active = state?.key === key;
    const arrow = active ? (state.direction === 'desc' ? '▼' : '▲') : '';
    const sortLabel = active ? `${state.direction === 'desc' ? 'descending' : 'ascending'}` : 'not sorted';
    return `<th class="monitor-sort-header${active?' is-sorted':''}" data-monitor-sort-table="${escapeHtml(table)}" data-monitor-sort-key="${escapeHtml(key)}" role="button" tabindex="0" aria-sort="${active ? (state.direction === 'desc' ? 'descending' : 'ascending') : 'none'}" title="Sort by ${escapeHtml(label)} (${sortLabel})"${style ? ` style="${escapeHtml(style)}"` : ''}><span>${escapeHtml(label)}</span><span class="monitor-sort-arrow">${arrow}</span></th>`;
  }

  function compareTableValues(a, b, direction = 'asc') {
    const aMissing = a === null || a === undefined || a === '' || (typeof a === 'number' && !Number.isFinite(a));
    const bMissing = b === null || b === undefined || b === '' || (typeof b === 'number' && !Number.isFinite(b));
    if (aMissing || bMissing) return aMissing === bMissing ? 0 : aMissing ? 1 : -1;
    let result = 0;
    if (typeof a === 'number' && typeof b === 'number') result = a - b;
    else result = String(a).localeCompare(String(b), undefined, { numeric:true, sensitivity:'base' });
    return direction === 'desc' ? -result : result;
  }

  function applyTableSort(rows, table, valueFor, fallbackCompare = null) {
    const sorted = [...rows];
    const state = monitor.tableSort[table];
    if (!state) {
      if (fallbackCompare) sorted.sort(fallbackCompare);
      return sorted;
    }
    sorted.sort((a,b) => compareTableValues(valueFor(a,state.key), valueFor(b,state.key), state.direction));
    return sorted;
  }

  function historyField(field) {
    const history = Array.isArray(monitor.state?.history) ? monitor.state.history : [];
    return history.map(item => nullableNumber(item?.[field])).filter(value => value !== null);
  }

  function historyChart(values, cls = '') {
    const normalized = values.map(value => Math.max(0, Math.min(100, value)));
    return `<svg class="monitor-history-chart ${cls}" viewBox="0 0 100 34" preserveAspectRatio="none"><polyline points="${polyline(normalized,34)}"></polyline></svg>`;
  }

  function latestHistoryStat(field, fallback) {
    const values = historyField(field);
    if (!values.length) return nullableNumber(fallback);
    return values[values.length - 1];
  }

  function renderPerformanceWorkspace(host) {
    const snapshot = monitor.state?.snapshot || {};
    const history = Array.isArray(monitor.state?.history) ? monitor.state.history : [];
    const cpuAvg = latestHistoryStat('cpu_average', snapshot.cpu_percent);
    const cpuPeak = latestHistoryStat('cpu_peak', snapshot.cpu_percent);
    const memoryAvg = latestHistoryStat('memory_average', snapshot.memory_percent);
    const memoryPeak = latestHistoryStat('memory_peak', snapshot.memory_percent);
    const tempAvg = latestHistoryStat('cpu_temperature_average', snapshot.cpu_temperature_c);
    const tempPeak = latestHistoryStat('cpu_temperature_peak', snapshot.cpu_temperature_c);
    const gpuAvg = latestHistoryStat('gpu_average', snapshot.gpu_percent);
    const gpuPeak = latestHistoryStat('gpu_peak', snapshot.gpu_percent);
    const rows = history.slice(-18).reverse().map(item => `<tr><td>${escapeHtml(localTime(item.start))}</td><td>${escapeHtml(localTime(item.end))}</td><td>${formatPercent(item.cpu_average,1)}</td><td>${formatPercent(item.cpu_peak,1)}</td><td>${formatPercent(item.memory_average,1)}</td><td>${formatTemp(item.cpu_temperature_peak)}</td><td>${number(item.samples)}</td></tr>`).join('');
    host.innerHTML = `${workspaceHead('Performance')}
      <div class="monitor-performance-scroll">
        <div class="monitor-performance-grid">
          <div class="monitor-performance-card"><div class="monitor-performance-title">CPU</div><div class="monitor-performance-value">${formatPercent(snapshot.cpu_percent,1)}</div><div class="monitor-performance-meta">5m avg ${formatPercent(cpuAvg,1)} · peak ${formatPercent(cpuPeak,1)}</div>${historyChart(historyField('cpu_average'),'cpu')}</div>
          <div class="monitor-performance-card"><div class="monitor-performance-title">GPU</div><div class="monitor-performance-value">${formatPercent(snapshot.gpu_percent,1)}</div><div class="monitor-performance-meta">5m avg ${formatPercent(gpuAvg,1)} · peak ${formatPercent(gpuPeak,1)}</div>${historyChart(historyField('gpu_average'),'gpu')}</div>
          <div class="monitor-performance-card"><div class="monitor-performance-title">Memory</div><div class="monitor-performance-value">${formatPercent(snapshot.memory_percent,1)}</div><div class="monitor-performance-meta">5m avg ${formatPercent(memoryAvg,1)} · peak ${formatPercent(memoryPeak,1)}</div>${historyChart(historyField('memory_average'),'memory')}</div>
          <div class="monitor-performance-card"><div class="monitor-performance-title">CPU Thermal</div><div class="monitor-performance-value" style="color:${tempColor(snapshot.cpu_temperature_c)}">${formatTemp(snapshot.cpu_temperature_c)}</div><div class="monitor-performance-meta">5m avg ${formatTemp(tempAvg)} · peak ${formatTemp(tempPeak)}</div>${historyChart(historyField('cpu_temperature_average'),'thermal')}</div>
        </div>
        <div class="monitor-history-caption">Retained telemetry is compacted into five-minute summaries. Gaps represent periods when Monitor was paused or disconnected.</div>
        <div class="monitor-generic-table-wrap"><table class="monitor-generic-table"><thead><tr><th>Start</th><th>End</th><th>CPU Avg</th><th>CPU Peak</th><th>RAM Avg</th><th>CPU Temp Peak</th><th>Samples</th></tr></thead><tbody>${rows || '<tr><td colspan="7"><div class="monitor-empty">History summaries will appear after the first telemetry bucket is completed or Monitor pauses.</div></td></tr>'}</tbody></table></div>
      </div>`;
  }


  function processItemRef(pid) {
    const list = Array.isArray(monitor.state?.snapshot?.processes) ? monitor.state.snapshot.processes : [];
    const item = list.find(proc => number(proc.pid) === number(pid));
    if (!item) return null;
    return { kind:'process', pid:number(item.pid), name:String(item.name || ''), path:String(item.path || '') };
  }

  function startupItemRef(index) {
    const list = Array.isArray(monitor.state?.snapshot?.startup_items) ? monitor.state.snapshot.startup_items : [];
    const item = list[index];
    if (!item) return null;
    return { kind:'startup', name:String(item.name || ''), path:String(item.path || ''), command:String(item.command || ''), source:String(item.source || '') };
  }

  function serviceItemRef(index) {
    const list = Array.isArray(monitor.state?.snapshot?.services) ? monitor.state.snapshot.services : [];
    const item = list[index];
    if (!item) return null;
    return { kind:'service', name:String(item.name || ''), pid:number(item.pid), command:String(item.command || '') };
  }

  function networkItemRef(index) {
    const list = Array.isArray(monitor.state?.snapshot?.connections) ? monitor.state.snapshot.connections : [];
    const item = list[index];
    if (!item) return null;
    return { kind:'network', pid:number(item.pid), name:String(item.process || ''), local_address:String(item.local_address || ''), local_port:number(item.local_port), remote_address:String(item.remote_address || ''), remote_port:number(item.remote_port) };
  }

  function securityItemRef(index) {
    const list = Array.isArray(monitor.state?.snapshot?.security) ? monitor.state.snapshot.security : [];
    const item = list[index];
    if (!item) return null;
    return { kind:'security', path:String(item.path || ''), source:String(item.source || ''), event_id:String(item.event_id || '') };
  }

  function itemRefKey(ref) {
    if (!ref) return '';
    if (ref.kind === 'process') return `process:${number(ref.pid)}`;
    if (ref.kind === 'startup') return `startup:${String(ref.name)}:${String(ref.source)}:${String(ref.command)}:${String(ref.path)}`;
    if (ref.kind === 'service') return `service:${String(ref.name)}:${number(ref.pid)}`;
    if (ref.kind === 'network') return `network:${number(ref.pid)}:${String(ref.local_address)}:${number(ref.local_port)}:${String(ref.remote_address)}:${number(ref.remote_port)}`;
    if (ref.kind === 'security') return `security:${String(ref.source)}:${String(ref.event_id)}:${String(ref.path)}`;
    return JSON.stringify(ref);
  }

  function selectedRefIs(ref) { return !!ref && itemRefKey(ref) === itemRefKey(monitor.selectedItemRef); }

  function reviewStateLabel(value) {
    const labels = { user_known:'User Known', previously_reviewed:'Previously Reviewed', changed_since_review:'Changed Since Review', needs_review:'Needs Review', no_file:'No Local File', file_unavailable:'File Unavailable' };
    return labels[String(value || '').toLowerCase()] || String(value || 'Needs Review').replaceAll('_',' ');
  }

  function itemActionBarHTML(options = {}) {
    const selected = !!monitor.selectedItemRef && (!options.kind || monitor.selectedItemRef.kind === options.kind);
    const disabled = selected ? '' : 'disabled';
    const aiDisabled = selected && aiModelSelected() ? '' : 'disabled';
    const advanced = options.advanced ? `<button class="monitor-action-btn monitor-advanced-btn" id="monitorAdvancedProcessBtn" type="button" ${selected?'':'disabled'}>${monitor.detailsOpen ? 'Hide Details' : 'Advanced'}</button>` : `<button class="monitor-action-btn" type="button" data-monitor-item-action="details" ${disabled}>Details</button>`;
    return `<div class="monitor-process-actions monitor-item-actions"><button class="monitor-action-btn" type="button" data-monitor-item-action="ai" ${aiDisabled}>Ask AI</button><button class="monitor-action-btn" type="button" data-monitor-item-action="hash" ${disabled}>Check Hash</button><button class="monitor-action-btn" type="button" data-monitor-item-action="reveal" ${disabled}>Show in Folder</button><button class="monitor-action-btn" type="button" data-monitor-item-action="virustotal" ${disabled}>Open VirusTotal</button><button class="monitor-action-btn" type="button" data-monitor-item-action="known" ${disabled}>Mark Known</button>${advanced}</div>`;
  }

  async function loadSelectedItemDetail(options = {}) {
    if (!monitor.selectedItemRef || typeof window.api !== 'function') return null;
    const refKey = itemRefKey(monitor.selectedItemRef);
    try {
      const detail = await window.api('/api/monitor/item/details', { method:'POST', body:JSON.stringify(monitor.selectedItemRef), timeoutMs:10000 });
      if (refKey !== itemRefKey(monitor.selectedItemRef)) return null;
      monitor.itemDetail = detail;
      if (options.rerender && monitor.tab === 'processes' && monitor.detailsOpen) renderProcessDetails();
      if (monitor.itemModal?.classList.contains('open')) renderSelectedItemModal();
      return detail;
    } catch (err) {
      if (!options.silent && typeof window.showToast === 'function') window.showToast(err?.message || 'Could not load selected item details.','warning',3600);
      return null;
    }
  }

  function detailValue(label, value, cls = '') {
    return `<div class="monitor-item-detail-row ${cls}"><span>${escapeHtml(label)}</span><strong>${escapeHtml(value || 'Unavailable')}</strong></div>`;
  }

  function renderSelectedItemModal() {
    const host = document.getElementById('monitorItemModalBody');
    const detail = monitor.itemDetail;
    if (!host) return;
    if (!monitor.selectedItemRef) { host.innerHTML = '<div class="monitor-empty">Select an item to inspect.</div>'; return; }
    if (!detail) { host.innerHTML = '<div class="monitor-item-loading"><span class="monitor-item-spinner"></span>Loading current item details…</div>'; return; }
    const connections = Array.isArray(detail.connections) ? detail.connections : [];
    const security = Array.isArray(detail.security_evidence) ? detail.security_evidence : [];
    host.innerHTML = `
      <div class="monitor-item-title-row"><div><div class="monitor-item-name">${escapeHtml(detail.name || 'Selected item')}</div><div class="monitor-item-path">${escapeHtml(detail.path || detail.command || 'No local executable path resolved')}</div></div><span class="monitor-review-badge" data-state="${escapeHtml(detail.review_state || 'needs_review')}">${escapeHtml(reviewStateLabel(detail.review_state))}</span></div>
      <div class="monitor-item-detail-grid">
        ${detailValue('Kind', detail.kind)}${detailValue('PID', detail.pid ? String(detail.pid) : '—')}${detailValue('Parent PID', detail.parent_pid ? String(detail.parent_pid) : '—')}${detailValue('File size', detail.file_size_bytes ? formatBytes(detail.file_size_bytes) : 'Unavailable')}
        ${detailValue('Publisher', detail.publisher)}${detailValue('Signature', detail.signature)}${detailValue('Version', detail.version)}${detailValue('User', detail.user)}
        ${detailValue('CPU', formatPercent(detail.cpu_percent,1))}${detailValue('GPU', formatPercent(detail.gpu_percent,1))}${detailValue('RAM', detail.memory_bytes ? formatBytes(detail.memory_bytes) : '—')}${detailValue('Started', detail.start_time ? localTime(detail.start_time) : '—')}
      </div>
      <div class="monitor-item-hash-block"><span>SHA-256</span><code>${escapeHtml(detail.sha256 || 'Not calculated yet')}</code>${detail.previous_sha256 ? `<div class="monitor-item-changed">Previous reviewed SHA-256: <code>${escapeHtml(detail.previous_sha256)}</code></div>` : ''}</div>
      ${connections.length ? `<div class="monitor-item-evidence"><strong>Related Network</strong>${connections.slice(0,12).map(c=>`<div>${escapeHtml(String(c.protocol || '').toUpperCase())} ${escapeHtml(endpointText(c.local_address,c.local_port))} → ${escapeHtml(endpointText(c.remote_address,c.remote_port))} ${escapeHtml(c.state || '')}</div>`).join('')}</div>` : ''}
      ${security.length ? `<div class="monitor-item-evidence warning"><strong>Related Security Evidence</strong>${security.slice(0,12).map(e=>`<div>${escapeHtml(e.source || 'Security')} · ${escapeHtml(e.message || e.type || '')}</div>`).join('')}</div>` : ''}`;
    const title = document.getElementById('monitorItemTitle');
    if (title) title.textContent = detail.name || 'Selected Item';
    const aiBtn = document.getElementById('monitorItemAIBtn'); if (aiBtn) aiBtn.disabled = !aiModelSelected();
    const checkBtn = document.getElementById('monitorItemHashBtn'); if (checkBtn) checkBtn.disabled = !detail.file_exists;
    const knownBtn = document.getElementById('monitorItemKnownBtn'); if (knownBtn) knownBtn.disabled = !detail.file_exists;
    const pathBtn = document.getElementById('monitorItemCopyPathBtn'); if (pathBtn) pathBtn.disabled = !detail.path;
    const hashBtn = document.getElementById('monitorItemCopyHashBtn'); if (hashBtn) hashBtn.disabled = !detail.sha256;
    const revealBtn = document.getElementById('monitorItemRevealBtn'); if (revealBtn) revealBtn.disabled = !detail.file_exists;
  }

  async function openSelectedItemModal() {
    if (!monitor.selectedItemRef) return;
    monitor.itemModal?.classList.add('open');
    monitor.itemModal?.setAttribute('aria-hidden','false');
    renderSelectedItemModal();
    await loadSelectedItemDetail({ silent:true });
  }

  function closeSelectedItemModal() {
    monitor.itemModal?.classList.remove('open');
    monitor.itemModal?.setAttribute('aria-hidden','true');
  }

  async function askAIAboutSelectedItem() {
    if (!monitor.selectedItemRef) return;
    if (!aiModelSelected()) { if (typeof window.showToast === 'function') window.showToast('Select a Monitor AI model first.','warning',3200); return; }
    if (monitor.itemBusy) return;
    monitor.itemBusy = true;
    try {
      const result = await window.api('/api/monitor/item/ai', { method:'POST', body:JSON.stringify(monitor.selectedItemRef), timeoutMs:180000 });
      if (result?.warning && typeof window.showToast === 'function') window.showToast(result.warning,'warning',4200);
      await loadAIState({ silent:true });
      await loadSelectedItemDetail({ silent:true, rerender:true });
      if (typeof window.showToast === 'function') window.showToast('Selected-item AI review added to Monitor chat.');
    } catch (err) {
      if (typeof window.showToast === 'function') window.showToast(err?.message || 'Selected-item AI review failed.','warning',4200);
    } finally { monitor.itemBusy = false; }
  }

  async function checkSelectedItemHash() {
    if (!monitor.selectedItemRef || monitor.itemBusy) return;
    monitor.itemBusy = true;
    try {
      const result = await window.api('/api/monitor/item/hash', { method:'POST', body:JSON.stringify(monitor.selectedItemRef), timeoutMs:120000 });
      monitor.itemDetail = result?.item || monitor.itemDetail;
      renderSelectedItemModal();
      if (monitor.detailsOpen) renderProcessDetails();
      const rep = result?.reputation || {};
      const message = rep.configured ? (rep.message || 'Hash reputation lookup completed.') : `SHA-256 calculated locally. ${rep.message || ''}`;
      if (typeof window.showToast === 'function') window.showToast(message, rep.malicious || rep.suspicious ? 'warning' : undefined, 5200);
      if (!monitor.itemModal?.classList.contains('open')) await openSelectedItemModal();
    } catch (err) {
      if (typeof window.showToast === 'function') window.showToast(err?.message || 'Could not calculate file hash.','warning',4200);
    } finally { monitor.itemBusy = false; }
  }

  async function showSelectedItemInFolder() {
    if (!monitor.selectedItemRef) return;
    try { await window.api('/api/monitor/item/reveal', { method:'POST', body:JSON.stringify(monitor.selectedItemRef), timeoutMs:10000 }); }
    catch (err) { if (typeof window.showToast === 'function') window.showToast(err?.message || 'Could not reveal the selected file.','warning',4200); }
  }

  async function openVirusTotalForSelected() {
    if (!monitor.selectedItemRef) return;
    const popup = window.open('about:blank','_blank');
    try {
      const result = await window.api('/api/monitor/item/virustotal', { method:'POST', body:JSON.stringify(monitor.selectedItemRef), timeoutMs:120000 });
      monitor.itemDetail = { ...(monitor.itemDetail || {}), path:result.path || monitor.itemDetail?.path, sha256:result.sha256 || monitor.itemDetail?.sha256, file_exists:true };
      renderSelectedItemModal();
      if (!monitor.itemModal?.classList.contains('open')) await openSelectedItemModal();
      if (popup) { popup.opener = null; popup.location = result.website_url || 'https://www.virustotal.com/gui/home/upload'; }
      else if (typeof window.showToast === 'function') window.showToast('Browser blocked the VirusTotal tab. Allow pop-ups and try again.','warning',4500);
      if (typeof window.showToast === 'function') window.showToast('AgentGO did not upload the file. Use the displayed path to choose it yourself on VirusTotal.', undefined, 5200);
    } catch (err) {
      try { popup?.close(); } catch (_) {}
      if (typeof window.showToast === 'function') window.showToast(err?.message || 'Could not prepare VirusTotal helper.','warning',4200);
    }
  }

  async function copyText(value, label) {
    const text = String(value || '').trim();
    if (!text) return;
    try { await navigator.clipboard.writeText(text); if (typeof window.showToast === 'function') window.showToast(`${label} copied.`); }
    catch (_) { if (typeof window.showToast === 'function') window.showToast(`Could not copy ${label.toLowerCase()}.`,'warning',3200); }
  }

  async function copySelectedItemPath() { if (!monitor.itemDetail) await loadSelectedItemDetail({silent:true}); return copyText(monitor.itemDetail?.path,'Path'); }
  async function copySelectedItemHash() { if (!monitor.itemDetail?.sha256) await checkSelectedItemHash(); return copyText(monitor.itemDetail?.sha256,'SHA-256'); }

  async function markSelectedItemKnown() {
    if (!monitor.selectedItemRef) return;
    let confirmed = true;
    if (typeof window.openConfirmModal === 'function') confirmed = await window.openConfirmModal({ title:'Mark this binary as known?', message:'AgentGO will remember the current file hash as user-known. If the binary changes later, Monitor will flag it for re-review.', confirmText:'Mark Known' });
    if (!confirmed) return;
    try {
      const result = await window.api('/api/monitor/item/known', { method:'POST', body:JSON.stringify(monitor.selectedItemRef), timeoutMs:120000 });
      monitor.itemDetail = result?.item || monitor.itemDetail;
      renderSelectedItemModal();
      if (monitor.detailsOpen) renderProcessDetails();
      await loadAIState({ silent:true });
      if (typeof window.showToast === 'function') window.showToast('Current binary marked as user-known.');
    } catch (err) { if (typeof window.showToast === 'function') window.showToast(err?.message || 'Could not mark the selected item known.','warning',4200); }
  }

  function renderStartupWorkspace(host) {
    const items = Array.isArray(monitor.state?.snapshot?.startup_items) ? monitor.state.snapshot.startup_items : [];
    let filtered = items.map((item,index)=>({item,index})).filter(({item}) => rowMatchesFilter([item.name,item.command,item.path,item.source,item.publisher]));
    filtered = applyTableSort(filtered,'startup-apps',({item},key) => ({
      name:item.name,
      state:item.enabled === false ? 'Disabled' : item.enabled === true ? 'Enabled' : '',
      source:item.source,
      command:item.command || item.path,
      publisher:item.publisher,
    })[key]);
    const rows = filtered.map(({item,index}) => {
      const ref = startupItemRef(index); const selected = selectedRefIs(ref);
      return `<tr class="${selected?'is-selected':''}" data-monitor-startup-index="${index}"><td><span class="monitor-proc-radio${selected?' is-selected':''}"></span></td><td class="monitor-proc-name">${escapeHtml(item.name || '—')}</td><td>${item.enabled === false ? 'Disabled' : item.enabled === true ? 'Enabled' : '—'}</td><td>${escapeHtml(item.source || '—')}</td><td title="${escapeHtml(item.command || item.path || '')}">${escapeHtml(item.command || item.path || '—')}</td><td>${escapeHtml(item.publisher || '—')}</td></tr>`;
    }).join('');
    host.innerHTML = `${workspaceHead('Startup Apps',{search:true,placeholder:'Search startup item, path, or source…'})}<div class="monitor-generic-table-wrap"><table class="monitor-generic-table"><thead><tr><th style="width:34px"></th>${tableSortHeader('startup-apps','name','Name')}${tableSortHeader('startup-apps','state','State')}${tableSortHeader('startup-apps','source','Source')}${tableSortHeader('startup-apps','command','Command / Path')}${tableSortHeader('startup-apps','publisher','Publisher')}</tr></thead><tbody>${rows || '<tr><td colspan="6"><div class="monitor-empty">No startup items are available in this view.</div></td></tr>'}</tbody></table></div>${itemActionBarHTML({kind:'startup'})}`;
  }

  function renderServicesWorkspace(host) {
    const items = Array.isArray(monitor.state?.snapshot?.services) ? monitor.state.snapshot.services : [];
    let filtered = items.map((item,index)=>({item,index})).filter(({item}) => rowMatchesFilter([item.name,item.state,item.startup_type,item.command,item.user,item.description,item.pid]));
    filtered = applyTableSort(filtered,'services',({item},key) => ({
      name:item.name,
      state:item.state,
      startup:item.startup_type,
      pid:number(item.pid) || null,
      user:item.user,
      description:item.description || item.command,
    })[key]);
    const rows = filtered.map(({item,index}) => {
      const ref = serviceItemRef(index); const selected = selectedRefIs(ref);
      return `<tr class="${selected?'is-selected':''}" data-monitor-service-index="${index}"><td><span class="monitor-proc-radio${selected?' is-selected':''}"></span></td><td class="monitor-proc-name">${escapeHtml(item.name || '—')}</td><td>${escapeHtml(item.state || '—')}</td><td>${escapeHtml(item.startup_type || '—')}</td><td>${item.pid || '—'}</td><td>${escapeHtml(item.user || '—')}</td><td title="${escapeHtml(item.description || item.command || '')}">${escapeHtml(item.description || item.command || '—')}</td></tr>`;
    }).join('');
    host.innerHTML = `${workspaceHead('Services / Daemons',{search:true,placeholder:'Search service, state, user, or command…'})}<div class="monitor-generic-table-wrap"><table class="monitor-generic-table"><thead><tr><th style="width:34px"></th>${tableSortHeader('services','name','Name')}${tableSortHeader('services','state','State')}${tableSortHeader('services','startup','Startup')}${tableSortHeader('services','pid','PID')}${tableSortHeader('services','user','User')}${tableSortHeader('services','description','Description / Command')}</tr></thead><tbody>${rows || '<tr><td colspan="7"><div class="monitor-empty">No service inventory is available on this platform/session.</div></td></tr>'}</tbody></table></div>${itemActionBarHTML({kind:'service'})}`;
  }

  function endpointText(address, port) {
    const host = String(address || '').trim();
    if (!host && !number(port)) return '—';
    return `${host || '*'}${number(port) ? `:${number(port)}` : ''}`;
  }


  function readableEndpoint(address, port) {
    const host = String(address || '').trim() || '*';
    const value = number(port);
    return value > 0 ? `${host} : ${value}` : host;
  }

  function renderNetworkWorkspace(host) {
    const items = Array.isArray(monitor.state?.snapshot?.connections) ? monitor.state.snapshot.connections : [];
    let filtered = items.map((item,index)=>({item,index})).filter(({item}) => rowMatchesFilter([item.protocol,item.local_address,item.local_port,item.remote_address,item.remote_port,item.state,item.process,item.pid]));
    filtered = applyTableSort(filtered,'network',({item},key) => ({
      protocol:String(item.protocol || '').toUpperCase(),
      local:`${item.local_address || ''}:${number(item.local_port)}`,
      remote:`${item.remote_address || ''}:${number(item.remote_port)}`,
      state:item.state,
      process:item.process,
      pid:number(item.pid) || null,
    })[key],(a,b) => networkConnectionPriority(a.item) - networkConnectionPriority(b.item) || String(a.item.process || '').localeCompare(String(b.item.process || '')));
    const rows = filtered.map(({item,index}) => {
      const stateClass = isEstablishedConnectionJS(item) ? 'is-established' : isListeningConnectionJS(item) ? 'is-listener' : '';
      return `<tr class="${stateClass}${item.pid?' is-investigable':''}" data-monitor-network-index="${index}" title="${item.pid?'Click to jump to the owning process':''}"><td>${escapeHtml(String(item.protocol || '—').toUpperCase())}</td><td>${escapeHtml(endpointText(item.local_address,item.local_port))}</td><td>${escapeHtml(endpointText(item.remote_address,item.remote_port))}</td><td>${escapeHtml(item.state || '—')}</td><td class="monitor-proc-name">${escapeHtml(item.process || '—')}</td><td>${item.pid || '—'}</td></tr>`;
    }).join('');
    host.innerHTML = `${workspaceHead('Network & Ports',{search:true,placeholder:'Search port, address, process, PID, or protocol…'})}<div class="monitor-network-note">Established connections are highlighted in soft yellow and shown first; listening endpoints remain green. Click a row with a PID to jump to its owning process and investigate it.</div><div class="monitor-generic-table-wrap"><table class="monitor-generic-table"><thead><tr>${tableSortHeader('network','protocol','Proto')}${tableSortHeader('network','local','Local')}${tableSortHeader('network','remote','Remote')}${tableSortHeader('network','state','State')}${tableSortHeader('network','process','Process')}${tableSortHeader('network','pid','PID')}</tr></thead><tbody>${rows || '<tr><td colspan="6"><div class="monitor-empty">No connection inventory is available yet.</div></td></tr>'}</tbody></table></div>`;
  }

  function isEstablishedConnectionJS(item) {
    const state = String(item?.state || '').trim().toLowerCase();
    return state === 'estab' || state === 'established' || state.includes('established');
  }

  function isListeningConnectionJS(item) {
    const state = String(item?.state || '').toLowerCase();
    return state.includes('listen') || (String(item?.protocol || '').toLowerCase() === 'udp' && !number(item?.remote_port));
  }

  function networkConnectionPriority(item) {
    if (isEstablishedConnectionJS(item)) return 0;
    if (isListeningConnectionJS(item)) return 1;
    return 2;
  }

  function formatMHz(value) {
    const n = nullableNumber(value);
    if (n === null || n <= 0) return '—';
    if (n >= 1000) return `${(n / 1000).toFixed(n >= 10000 ? 1 : 2)} GHz`;
    return `${n.toFixed(0)} MHz`;
  }

  function detailUnavailable(title, message, hint = '') {
    return `<div class="monitor-detail-unavailable"><strong>${escapeHtml(title)}</strong><p>${escapeHtml(message)}</p>${hint ? `<p class="monitor-detail-hint">${escapeHtml(hint)}</p>` : ''}</div>`;
  }

  function detailSummaryItem(label, value, meta = '') {
    return `<div class="monitor-detail-summary-item"><span>${escapeHtml(label)}</span><strong>${escapeHtml(value || '—')}</strong>${meta ? `<small>${escapeHtml(meta)}</small>` : ''}</div>`;
  }

  function renderCPUWorkspace(host) {
    const snapshot = monitor.state?.snapshot || {};
    const system = snapshot.system || {};
    const detail = snapshot.cpu_detail || {};
    const cores = Array.isArray(detail.cores) ? detail.cores : [];
    const model = String(detail.model || system.cpu_model || '').trim();
    const physical = number(detail.physical_cores || system.physical_cpu_cores);
    const logical = number(detail.logical_cpus || system.logical_cpus || cores.length);
    if (!model && !physical && !logical && nullableNumber(snapshot.cpu_percent) === null) {
      host.innerHTML = `${workspaceHead('CPU Details')}${detailUnavailable('CPU details unavailable','The current platform collector did not expose CPU topology or utilization data.','AgentGO uses operating-system interfaces directly; no optional CPU collector is configured for this platform.')}`;
      return;
    }
    const rows = cores.map(core => {
      const usage = nullableNumber(core.usage_percent);
      const width = usage === null ? 0 : Math.max(0,Math.min(100,usage));
      const label = core.label || `CPU ${number(core.index)}`;
      return `<div class="monitor-core-row"><div class="monitor-core-name"><strong>${escapeHtml(label)}</strong>${core.kind ? `<span>${escapeHtml(core.kind)}</span>` : ''}</div><div class="monitor-core-bar"><span style="width:${width}%"></span></div><div class="monitor-core-value">${usage === null ? '—' : `${usage.toFixed(1)}%`}</div><div class="monitor-core-clock">${formatMHz(core.frequency_mhz)}${nullableNumber(core.max_frequency_mhz)!==null ? ` / ${formatMHz(core.max_frequency_mhz)}` : ''}</div></div>`;
    }).join('');
    const hasCoreUsage = cores.some(core => nullableNumber(core.usage_percent) !== null);
    host.innerHTML = `${workspaceHead('CPU Details')}<div class="monitor-detail-scroll">
      <div class="monitor-detail-summary-grid">
        ${detailSummaryItem('Current Load',formatPercent(snapshot.cpu_percent,1),'overall CPU')}
        ${detailSummaryItem('Physical Cores',physical ? String(physical) : '—')}
        ${detailSummaryItem('Logical CPUs',logical ? String(logical) : '—')}
        ${detailSummaryItem('Current Clock',formatMHz(detail.current_frequency_mhz))}
        ${detailSummaryItem('Max Clock',formatMHz(detail.max_frequency_mhz))}
        ${detailSummaryItem('Temperature',formatTemp(snapshot.cpu_temperature_c))}
      </div>
      <div class="monitor-detail-section-head"><strong>${escapeHtml(model || 'CPU')}</strong><span>${escapeHtml(detail.architecture || system.architecture || '')}</span></div>
      ${rows ? `<div class="monitor-core-list">${rows}</div>${!hasCoreUsage ? '<div class="monitor-detail-note">Per-core utilization is not exposed by the current platform collector; topology is shown where available.</div>' : ''}` : '<div class="monitor-detail-note">Per-core utilization/topology is not exposed by the current platform collector. Overall CPU load is still available above.</div>'}
    </div>`;
  }

  function renderGPUWorkspace(host) {
    const snapshot = monitor.state?.snapshot || {};
    let gpus = Array.isArray(snapshot.gpu_details) ? [...snapshot.gpu_details] : [];
    if (!gpus.length && String(snapshot.system?.gpu_model || '').trim()) {
      gpus = [{ index:0, name:snapshot.system.gpu_model, usage_percent:snapshot.gpu_percent, temperature_c:snapshot.gpu_temperature_c }];
    }
    if (!gpus.length) {
      const os = String(snapshot.system?.os || '').toLowerCase();
      const hint = os.includes('linux') || os.includes('windows') ? 'This is normal in many virtual machines and headless systems. On NVIDIA Windows/Linux hosts, AgentGO can also read richer telemetry when nvidia-smi is available from the installed NVIDIA driver.' : 'This is normal in virtual machines, headless systems, or platforms that do not expose GPU telemetry to AgentGO.';
      host.innerHTML = `${workspaceHead('GPU Details')}${detailUnavailable('GPU telemetry unavailable','No supported GPU telemetry source was detected on this system.',hint)}`;
      return;
    }
    const cards = gpus.map((gpu,index) => {
      const total = number(gpu.vram_total_bytes);
      const used = number(gpu.vram_used_bytes);
      const free = number(gpu.vram_free_bytes || (total >= used ? total-used : 0));
      const vram = total ? `${formatBytes(used)} used · ${formatBytes(free)} free · ${formatBytes(total)} total` : 'VRAM telemetry not exposed';
      const fan = nullableNumber(gpu.fan_rpm) !== null ? `${number(gpu.fan_rpm).toFixed(0)} RPM` : nullableNumber(gpu.fan_percent) !== null ? `${number(gpu.fan_percent).toFixed(0)}%` : '—';
      return `<section class="monitor-gpu-card"><div class="monitor-gpu-card-head"><div><strong>${escapeHtml(gpu.name || `GPU ${index}`)}</strong><span>${escapeHtml([gpu.vendor,gpu.driver].filter(Boolean).join(' · '))}</span></div><div class="monitor-gpu-load">${formatPercent(gpu.usage_percent,1)}</div></div><div class="monitor-detail-summary-grid compact">${detailSummaryItem('VRAM',vram)}${detailSummaryItem('Temperature',formatTemp(gpu.temperature_c))}${detailSummaryItem('Core Clock',formatMHz(gpu.core_clock_mhz))}${detailSummaryItem('Memory Clock',formatMHz(gpu.memory_clock_mhz))}${detailSummaryItem('Fan',fan)}</div></section>`;
    }).join('');
    host.innerHTML = `${workspaceHead('GPU Details')}<div class="monitor-detail-scroll"><div class="monitor-detail-note">${gpus.length} GPU${gpus.length===1?'':'s'} detected. Some vendors/platforms expose identity and VRAM capacity but not live utilization or sensor data.</div>${cards}</div>`;
  }

  function memoryMeter(label, value, total, tone = 'used', meta = '') {
    const bytes = Math.max(0, number(value));
    const capacity = Math.max(0, number(total));
    const pct = capacity ? Math.max(0, Math.min(100, bytes * 100 / capacity)) : 0;
    return `<div class="monitor-memory-meter tone-${escapeHtml(tone)}"><div class="monitor-memory-meter-head"><strong>${escapeHtml(label)}</strong><div><span>${escapeHtml(formatBytes(bytes))}</span><em>${pct.toFixed(1)}%</em></div></div>${meta ? `<div class="monitor-memory-meter-meta">${escapeHtml(meta)}</div>` : ''}<div class="monitor-memory-meter-track"><span style="width:${pct}%"></span></div></div>`;
  }

  function renderMemoryWorkspace(host) {
    const snapshot = monitor.state?.snapshot || {};
    const detail = snapshot.memory_detail || {};
    const total = number(detail.total_bytes || snapshot.memory_total_bytes || snapshot.system?.total_memory_bytes);
    const used = number(detail.used_bytes || snapshot.memory_used_bytes);
    const available = number(detail.available_bytes || Math.max(0,total-used));
    if (!total) {
      host.innerHTML = `${workspaceHead('Memory Details')}${detailUnavailable('Memory details unavailable','The current operating-system collector did not expose memory accounting data.')}`;
      return;
    }
    const meters = [
      ['Used',used,'used'],
      ['Available',available,'available'],
      ['Active',detail.active_bytes,'active'],
      ['Cached',detail.cached_bytes,'cached'],
      ['Inactive',detail.inactive_bytes,'inactive'],
      ['Buffers',detail.buffers_bytes,'buffers'],
      ['Wired',detail.wired_bytes,'wired'],
      ['Compressed',detail.compressed_bytes,'compressed'],
    ].filter(([label,value],index) => index < 2 || number(value) > 0);
    const swapTotal = number(detail.swap_total_bytes);
    const swapUsed = number(detail.swap_used_bytes);
    host.innerHTML = `${workspaceHead('Memory Details')}<div class="monitor-detail-scroll">
      <div class="monitor-memory-capacity"><span>Total Memory</span><strong>${formatBytes(total)}</strong></div>
      <div class="monitor-memory-meter-list">${meters.map(([label,value,tone]) => memoryMeter(label,value,total,tone)).join('')}</div>
      ${swapTotal > 0 ? `<div class="monitor-memory-swap"><div class="monitor-detail-section-head"><strong>Swap</strong><span>${formatBytes(swapTotal)} total</span></div>${memoryMeter('Swap Used',swapUsed,swapTotal,'swap',`${formatBytes(Math.max(0,swapTotal-swapUsed))} free`)}</div>` : ''}
    </div>`;
  }

  function diskGroupInfo(disk) {
    const explicit = String(disk?.group || '').trim();
    const explicitKind = String(disk?.group_kind || '').trim().toLowerCase();
    if (explicit) return { key:explicit, label:explicit, kind:explicitKind || 'physical' };
    const fs = String(disk?.file_system || disk?.kind || '').trim().toLowerCase();
    const device = String(disk?.device || '').trim();
    const base = device.split(/[\\/]/).filter(Boolean).pop() || String(disk?.name || '').trim();
    if (['vboxsf','9p','cifs','smbfs','nfs','nfs4','afpfs','webdav'].includes(fs) || fs.includes('sshfs')) return { key:'Shared / Network', label:'Shared / Network', kind:'shared' };
    if (/^loop\d+/i.test(base) || fs === 'squashfs') return { key:'Virtual / Loop', label:'Virtual / Loop', kind:'loop' };
    let match = base.match(/^(nvme\d+n\d+)(?:p\d+)?$/i) || base.match(/^(mmcblk\d+)(?:p\d+)?$/i) || base.match(/^(disk\d+)(?:s\d+.*)?$/i) || base.match(/^((?:sd|vd|xvd)[a-z]+)\d*$/i);
    if (match) return { key:match[1], label:match[1], kind:'physical' };
    if (/^[A-Za-z]:$/.test(device)) return { key:device.toUpperCase(), label:device.toUpperCase(), kind:String(disk?.kind || '').toLowerCase() === 'network' ? 'shared' : 'physical' };
    if (device.startsWith('/dev/') && base) return { key:base, label:base, kind:'physical' };
    return { key:'Virtual / Other', label:'Virtual / Other', kind:'virtual' };
  }

  function diskGroupPriority(info) {
    return ({ physical:0, shared:1, loop:2, virtual:3 })[info?.kind] ?? 4;
  }

  function diskGroupHeading(info) {
    if (info.kind === 'physical') return `Drive · ${info.label}`;
    return info.label;
  }

  function diskRateForGroup(snapshot, groupKey, groups) {
    if (groupKey === 'all') return { read:nullableNumber(snapshot.disk_read_bytes_per_sec), write:nullableNumber(snapshot.disk_write_bytes_per_sec) };
    const group = groups.get(groupKey);
    if (!group || group.info.kind !== 'physical') return { read:null, write:null };
    const io = Array.isArray(snapshot.disk_io) ? snapshot.disk_io : [];
    const devices = groupKey.split('+').map(value => value.trim()).filter(Boolean);
    const matching = io.filter(item => devices.includes(String(item?.device || '').trim()));
    if (matching.length) {
      let read = 0, write = 0, hasRead = false, hasWrite = false;
      matching.forEach(item => {
        const r = nullableNumber(item.read_bytes_per_sec); const w = nullableNumber(item.write_bytes_per_sec);
        if (r !== null) { read += r; hasRead = true; }
        if (w !== null) { write += w; hasWrite = true; }
      });
      return { read:hasRead ? read : null, write:hasWrite ? write : null };
    }
    const physicalGroups = [...groups.values()].filter(entry => entry.info.kind === 'physical');
    if (physicalGroups.length === 1) return { read:nullableNumber(snapshot.disk_read_bytes_per_sec), write:nullableNumber(snapshot.disk_write_bytes_per_sec) };
    return { read:null, write:null };
  }

  function diskSortValue(disk, key) {
    return ({
      name:disk.name || disk.device,
      device:disk.device,
      mount:disk.mount_point,
      fs:disk.file_system || disk.kind,
      total:number(disk.total_bytes),
      used:number(disk.used_bytes),
      free:number(disk.free_bytes),
      usage:nullableNumber(disk.usage_percent),
    })[key];
  }

  function diskCell(label, display, raw = display, cls = '') {
    return `<td${cls ? ` class="${escapeHtml(cls)}"` : ''} data-disk-hover-label="${escapeHtml(label)}" data-disk-hover-value="${escapeHtml(raw)}" title="${escapeHtml(raw)}">${escapeHtml(display)}</td>`;
  }

  function renderDiskWorkspace(host) {
    const snapshot = monitor.state?.snapshot || {};
    let disks = Array.isArray(snapshot.disk_details) ? [...snapshot.disk_details] : [];
    if (!disks.length) {
      const total = number(snapshot.disk_total_bytes || snapshot.system?.storage_total_bytes);
      const used = number(snapshot.disk_used_bytes || Math.max(0,total-number(snapshot.system?.storage_free_bytes)));
      if (total) disks = [{name:'Primary storage',mount_point:'/',total_bytes:total,used_bytes:used,free_bytes:Math.max(0,total-used),usage_percent:total?used*100/total:null,group:'Primary storage',group_kind:'physical'}];
    }
    if (!disks.length) {
      host.innerHTML = `${workspaceHead('Disk Details')}${detailUnavailable('Disk details unavailable','No mounted disk or volume inventory is available from the current platform collector.')}`;
      return;
    }

    const groups = new Map();
    disks.forEach(disk => {
      const info = diskGroupInfo(disk);
      if (!groups.has(info.key)) groups.set(info.key,{ info, disks:[] });
      groups.get(info.key).disks.push(disk);
    });
    const orderedGroups = [...groups.values()].sort((a,b) => diskGroupPriority(a.info)-diskGroupPriority(b.info) || a.info.label.localeCompare(b.info.label,undefined,{numeric:true,sensitivity:'base'}));
    if (monitor.diskGroup !== 'all' && !groups.has(monitor.diskGroup)) monitor.diskGroup = 'all';
    const selectedGroup = monitor.diskGroup === 'all' ? null : groups.get(monitor.diskGroup);
    const visible = selectedGroup ? selectedGroup.disks : disks;
    const capacityRows = monitor.diskGroup === 'all' ? disks.filter(disk => diskGroupInfo(disk).kind === 'physical') : visible;
    const capacity = capacityRows.reduce((sum,disk) => sum + number(disk.total_bytes),0);
    const rates = diskRateForGroup(snapshot,monitor.diskGroup,groups);

    const selector = `<div class="monitor-disk-selector"><button type="button" class="${monitor.diskGroup==='all'?'is-selected':''}" data-monitor-disk-group="all">All Drives</button>${orderedGroups.map(group => `<button type="button" class="${monitor.diskGroup===group.info.key?'is-selected':''} kind-${escapeHtml(group.info.kind)}" data-monitor-disk-group="${escapeHtml(group.info.key)}">${escapeHtml(group.info.label)}</button>`).join('')}</div>`;

    const rowHTML = disk => {
      const info = diskGroupInfo(disk);
      const total = number(disk.total_bytes);
      const rowClass = info.kind === 'shared' ? 'is-shared-volume' : info.kind === 'loop' ? 'is-loop-volume' : info.kind === 'physical' && total > 8 * 1024 * 1024 * 1024 ? 'is-important-volume' : '';
      return `<tr class="${rowClass}">${diskCell('Name',disk.name || disk.device || 'Disk')}${diskCell('Device',disk.device || '—')}${diskCell('Mount',disk.mount_point || '—')}${diskCell('FS / Type',disk.file_system || disk.kind || '—')}${diskCell('Total',formatBytes(disk.total_bytes))}${diskCell('Used',formatBytes(disk.used_bytes))}${diskCell('Free',formatBytes(disk.free_bytes))}${diskCell('Usage',formatPercent(disk.usage_percent,1))}</tr>`;
    };

    let rows = '';
    if (selectedGroup) {
      rows = applyTableSort(selectedGroup.disks,'disk-details',diskSortValue,(a,b) => (a.mount_point === '/' ? -1 : b.mount_point === '/' ? 1 : String(a.mount_point||'').localeCompare(String(b.mount_point||''),undefined,{numeric:true,sensitivity:'base'}))).map(rowHTML).join('');
    } else {
      rows = orderedGroups.map(group => {
        const sorted = applyTableSort(group.disks,'disk-details',diskSortValue,(a,b) => (a.mount_point === '/' ? -1 : b.mount_point === '/' ? 1 : String(a.mount_point||'').localeCompare(String(b.mount_point||''),undefined,{numeric:true,sensitivity:'base'})));
        return `<tr class="monitor-disk-group-row kind-${escapeHtml(group.info.kind)}"><td colspan="8">${escapeHtml(diskGroupHeading(group.info))}</td></tr>${sorted.map(rowHTML).join('')}`;
      }).join('');
    }

    host.innerHTML = `${workspaceHead('Disk Details',{hoverDetail:true})}<div class="monitor-detail-scroll">${selector}<div class="monitor-detail-summary-grid compact monitor-disk-summary">${detailSummaryItem('Volumes',String(visible.length))}${detailSummaryItem('Capacity',capacity ? formatBytes(capacity) : '—')}${detailSummaryItem('Read Rate',formatRate(rates.read))}${detailSummaryItem('Write Rate',formatRate(rates.write))}</div><div class="monitor-generic-table-wrap monitor-disk-table-wrap"><table class="monitor-generic-table"><thead><tr>${tableSortHeader('disk-details','name','Name')}${tableSortHeader('disk-details','device','Device')}${tableSortHeader('disk-details','mount','Mount')}${tableSortHeader('disk-details','fs','FS / Type')}${tableSortHeader('disk-details','total','Total')}${tableSortHeader('disk-details','used','Used')}${tableSortHeader('disk-details','free','Free')}${tableSortHeader('disk-details','usage','Usage')}</tr></thead><tbody>${rows}</tbody></table></div></div>`;
  }


  function cheatItem(title, description, copy = '', risk = 'normal', tags = '') {
    return { title, description, copy, risk, tags };
  }

  const MONITOR_CHEATS = {
    windows: {
      label: 'Windows',
      items: [
        cheatItem('Win + Shift + S','Open the Windows snipping overlay to capture a region, window, or screen.','','normal','screenshot capture snip'),
        cheatItem('Win + V','Open Clipboard History after it has been enabled. Useful for recovering earlier copied text.','','normal','clipboard history'),
        cheatItem('Ctrl + Shift + Esc','Open Task Manager directly.','','normal','task manager processes'),
        cheatItem('Win + X','Open the power-user menu for Terminal, Device Manager, Disk Management, and other admin tools.','','normal','admin power menu'),
        cheatItem('Win + R','Open the Run dialog.','','normal','run dialog'),
        cheatItem('Alt + Tab','Switch between open applications.','','normal','window switch'),
        cheatItem('Win + Ctrl + D','Create a new virtual desktop.','','normal','virtual desktop'),
        cheatItem('Win + Ctrl + Left / Right','Move between virtual desktops.','','normal','virtual desktop switch'),
        cheatItem('ipconfig /all','Show adapter addresses, DNS servers, DHCP information, and other network configuration.','ipconfig /all'),
        cheatItem('ipconfig /flushdns','Clear the Windows DNS resolver cache.','ipconfig /flushdns','caution','dns cache'),
        cheatItem('netstat -ano','Show active/listening connections with owning PIDs.','netstat -ano'),
        cheatItem('tasklist','List running processes from Command Prompt.','tasklist'),
        cheatItem('taskkill /PID 1234 /F','Force-stop a process by PID. Unsaved work in that process can be lost.','taskkill /PID 1234 /F','danger','kill process force'),
        cheatItem('where.exe python','Show which executable Windows resolves for a command.','where.exe python'),
        cheatItem('Get-Process','List processes in PowerShell.','Get-Process'),
        cheatItem('Get-Service','List Windows services and their current states.','Get-Service'),
        cheatItem('Get-NetTCPConnection','Show TCP listeners and established connections in PowerShell.','Get-NetTCPConnection'),
        cheatItem('Test-NetConnection example.com -Port 443','Test DNS/network reachability to a host and TCP port.','Test-NetConnection example.com -Port 443'),
        cheatItem('Get-FileHash .\\file.exe -Algorithm SHA256','Calculate a SHA-256 file hash.','Get-FileHash .\\file.exe -Algorithm SHA256'),
        cheatItem('Get-ChildItem -Recurse -Filter "*.go"','Recursively find files matching a name/filter.','Get-ChildItem -Recurse -Filter "*.go"'),
        cheatItem('winget search git','Search Windows Package Manager for a package.','winget search git'),
        cheatItem('winget upgrade --all','Upgrade all packages Winget considers upgradeable. Review the list first.','winget upgrade --all','caution','packages update')
      ]
    },
    mac: {
      label: 'Mac',
      items: [
        cheatItem('Cmd + Shift + 3','Capture the entire screen.','','normal','screenshot'),
        cheatItem('Cmd + Shift + 4','Capture a selected screen region. Press Space afterward to capture a window.','','normal','screenshot region window'),
        cheatItem('Cmd + Shift + 5','Open macOS screenshot and screen-recording controls.','','normal','screenshot record'),
        cheatItem('Cmd + Space','Open Spotlight search.','','normal','spotlight search'),
        cheatItem('Cmd + Option + Esc','Open Force Quit Applications.','','caution','force quit'),
        cheatItem('open .','Open the current Terminal directory in Finder.','open .'),
        cheatItem('pwd','Print the current working directory.','pwd'),
        cheatItem('ls -lah','List files, including hidden files, with human-readable sizes.','ls -lah'),
        cheatItem('pbcopy < file.txt','Copy a file\'s text into the macOS clipboard.','pbcopy < file.txt'),
        cheatItem('pbpaste > file.txt','Write current clipboard text into a file. Existing file contents are replaced.','pbpaste > file.txt','caution','clipboard file overwrite'),
        cheatItem('lsof -nP -iTCP:3000 -sTCP:LISTEN','Find the process listening on TCP port 3000.','lsof -nP -iTCP:3000 -sTCP:LISTEN'),
        cheatItem('ps aux','List running processes with users and resource information.','ps aux'),
        cheatItem('kill 1234','Ask a process to terminate normally by PID.','kill 1234','caution'),
        cheatItem('sudo kill -9 1234','Force-kill a process immediately. It cannot perform normal cleanup.','sudo kill -9 1234','danger'),
        cheatItem('df -h','Show mounted filesystem space in human-readable units.','df -h'),
        cheatItem('du -sh *','Show approximate size of items in the current directory.','du -sh *'),
        cheatItem('networkQuality','Run Apple\'s built-in network responsiveness/upload/download test.','networkQuality'),
        cheatItem('scutil --dns','Display the active macOS DNS resolver configuration.','scutil --dns'),
        cheatItem('xattr -l ./file','List extended attributes, including quarantine metadata.','xattr -l ./file'),
        cheatItem('codesign -dv --verbose=4 ./App.app','Display code-signing details for an application.','codesign -dv --verbose=4 ./App.app'),
        cheatItem('spctl --assess --verbose ./App.app','Ask Gatekeeper whether an application passes policy assessment.','spctl --assess --verbose ./App.app'),
        cheatItem('brew update && brew outdated','Refresh Homebrew metadata and show packages with available updates.','brew update && brew outdated'),
        cheatItem('brew upgrade','Upgrade installed Homebrew packages. Review outdated packages first.','brew upgrade','caution')
      ]
    },
    linux: {
      label: 'Linux',
      items: [
        cheatItem('pwd','Print the current working directory.','pwd'),
        cheatItem('ls -lah','List files, including hidden files, with human-readable sizes.','ls -lah'),
        cheatItem('find . -type f -name "*.go"','Recursively find Go source files under the current directory.','find . -type f -name "*.go"'),
        cheatItem('grep -RIn "needle" .','Recursively search text files and show matching line numbers.','grep -RIn "needle" .'),
        cheatItem('rg "needle" .','Fast recursive text search when ripgrep is installed.','rg "needle" .'),
        cheatItem('cp -a source/ destination/','Copy a directory while preserving metadata.','cp -a source/ destination/'),
        cheatItem('rsync -avh source/ destination/','Synchronize files with progress-friendly, metadata-preserving behavior.','rsync -avh source/ destination/'),
        cheatItem('mv oldname newname','Move or rename a file/directory.','mv oldname newname','caution'),
        cheatItem('rm file.txt','Delete a file immediately; normal shell deletion does not use a recycle bin.','rm file.txt','caution'),
        cheatItem('rm -rf ./directory','Recursively force-delete a directory tree. Verify the path before running.','rm -rf ./directory','danger','delete recursive force'),
        cheatItem('chmod +x script.sh','Make a script executable.','chmod +x script.sh'),
        cheatItem('chmod -R 755 ./directory','Recursively change permissions. This can unintentionally alter many files.','chmod -R 755 ./directory','caution'),
        cheatItem('sudo chown -R user:group ./directory','Recursively change ownership. Verify the target path carefully.','sudo chown -R user:group ./directory','caution'),
        cheatItem('ps aux --sort=-%cpu | head','Show the highest-CPU processes.','ps aux --sort=-%cpu | head'),
        cheatItem('free -h','Show RAM and swap use in human-readable units.','free -h'),
        cheatItem('df -h','Show filesystem capacity/free space.','df -h'),
        cheatItem('du -sh * | sort -h','Show and sort the size of items in the current directory.','du -sh * | sort -h'),
        cheatItem('ss -tulpn','Show TCP/UDP listeners and owning processes when permissions allow.','ss -tulpn'),
        cheatItem('journalctl -u nginx --since "1 hour ago"','Show recent systemd journal entries for one service.','journalctl -u nginx --since "1 hour ago"'),
        cheatItem('systemctl status nginx','Show a systemd service\'s state and recent logs.','systemctl status nginx'),
        cheatItem('sudo systemctl restart nginx','Restart a service; connected users may see an interruption.','sudo systemctl restart nginx','caution'),
        cheatItem('tail -f app.log','Follow new lines appended to a log file.','tail -f app.log'),
        cheatItem('tar -czf archive.tar.gz directory/','Create a gzip-compressed tar archive.','tar -czf archive.tar.gz directory/'),
        cheatItem('tar -xzf archive.tar.gz','Extract a gzip-compressed tar archive into the current directory.','tar -xzf archive.tar.gz')
      ]
    },
    git: {
      label: 'Git',
      items: [
        cheatItem('git status','Show changed, staged, and untracked files.','git status'),
        cheatItem('git log --oneline --graph --decorate --all','Show a compact branch/commit graph.','git log --oneline --graph --decorate --all'),
        cheatItem('git diff','Show unstaged changes.','git diff'),
        cheatItem('git diff --staged','Show changes already staged for the next commit.','git diff --staged'),
        cheatItem('git add -p','Interactively choose individual change hunks to stage.','git add -p'),
        cheatItem('git add .','Stage all changes under the current directory.','git add .','caution'),
        cheatItem('git commit -m "message"','Create a commit from staged changes.','git commit -m "message"'),
        cheatItem('git pull --rebase','Fetch remote work and replay your local commits on top.','git pull --rebase','caution'),
        cheatItem('git push','Push local commits to the configured upstream.','git push'),
        cheatItem('git switch -c feature/name','Create and switch to a new branch.','git switch -c feature/name'),
        cheatItem('git switch main','Switch to an existing branch.','git switch main'),
        cheatItem('git branch -a','List local and remote-tracking branches.','git branch -a'),
        cheatItem('git stash push -m "wip"','Temporarily save uncommitted changes.','git stash push -m "wip"'),
        cheatItem('git stash pop','Reapply the newest stash and remove it from the stash list. Conflicts are possible.','git stash pop','caution'),
        cheatItem('git restore path/to/file','Discard unstaged changes in a file and restore the index version.','git restore path/to/file','danger','discard changes'),
        cheatItem('git restore --staged path/to/file','Unstage a file without discarding its working-tree changes.','git restore --staged path/to/file'),
        cheatItem('git reset --soft HEAD~1','Move HEAD back one commit but keep all changes staged.','git reset --soft HEAD~1','caution','undo commit'),
        cheatItem('git reset HEAD~1','Move HEAD back one commit and keep changes unstaged in the working tree.','git reset HEAD~1','caution','undo commit mixed'),
        cheatItem('git reset --hard HEAD','Discard tracked working-tree/index changes and reset to HEAD.','git reset --hard HEAD','danger','discard changes reset'),
        cheatItem('git clean -fd','Permanently delete untracked files and directories.','git clean -fd','danger','delete untracked'),
        cheatItem('git remote -v','Show configured remote names and URLs.','git remote -v'),
        cheatItem('git remote set-url origin https://github.com/user/repo.git','Change the URL used by the origin remote.','git remote set-url origin https://github.com/user/repo.git','caution','reset origin remote url'),
        cheatItem('git remote remove origin','Remove the origin remote configuration; local commits remain.','git remote remove origin','caution','delete reset origin'),
        cheatItem('git remote add origin https://github.com/user/repo.git','Add a remote named origin.','git remote add origin https://github.com/user/repo.git'),
        cheatItem('git fetch --all --prune','Fetch all remotes and remove stale remote-tracking references.','git fetch --all --prune'),
        cheatItem('git rebase main','Replay the current branch on top of main. This rewrites local commit IDs.','git rebase main','caution'),
        cheatItem('git rebase --abort','Cancel an in-progress rebase and restore its starting state.','git rebase --abort'),
        cheatItem('git cherry-pick <commit>','Apply one existing commit onto the current branch.','git cherry-pick <commit>','caution'),
        cheatItem('git reflog','Show recent HEAD movements; invaluable for recovering lost-looking commits.','git reflog'),
        cheatItem('git reset --hard HEAD@{1}','Reset to the previous reflog position. Verify reflog first because working changes are discarded.','git reset --hard HEAD@{1}','danger','recover head reset'),
        cheatItem('git push --force-with-lease','Force-update the remote only if it has not moved unexpectedly. Still rewrites shared history.','git push --force-with-lease','danger','force push'),
        cheatItem('git tag -a v1.0.0 -m "v1.0.0"','Create an annotated release tag.','git tag -a v1.0.0 -m "v1.0.0"')
      ]
    },
    ssh: {
      label: 'SSH',
      items: [
        cheatItem('ssh user@example.com','Connect to a remote SSH server on the default port.','ssh user@example.com'),
        cheatItem('ssh -p 2222 user@example.com','Connect to SSH on a non-default port.','ssh -p 2222 user@example.com'),
        cheatItem('ssh -i ~/.ssh/id_ed25519 user@example.com','Connect using a specific private key.','ssh -i ~/.ssh/id_ed25519 user@example.com'),
        cheatItem('ssh -v user@example.com','Enable verbose SSH diagnostics for authentication/connection troubleshooting.','ssh -v user@example.com'),
        cheatItem('ssh-keygen -t ed25519 -C "you@example.com"','Create a modern Ed25519 SSH key pair. Do not overwrite an existing key unless intended.','ssh-keygen -t ed25519 -C "you@example.com"','caution'),
        cheatItem('ssh-copy-id user@example.com','Install your public key into the remote account\'s authorized_keys file when supported.','ssh-copy-id user@example.com','caution'),
        cheatItem('ssh-add ~/.ssh/id_ed25519','Add a private key to the current SSH agent.','ssh-add ~/.ssh/id_ed25519'),
        cheatItem('ssh-add -l','List keys currently loaded in the SSH agent.','ssh-add -l'),
        cheatItem('scp ./file.zip user@example.com:/remote/path/','Copy a local file to a remote host.','scp ./file.zip user@example.com:/remote/path/'),
        cheatItem('scp user@example.com:/remote/file.zip ./','Copy a remote file to the current local directory.','scp user@example.com:/remote/file.zip ./'),
        cheatItem('scp -r ./folder user@example.com:/remote/path/','Recursively copy a local directory to a remote host.','scp -r ./folder user@example.com:/remote/path/'),
        cheatItem('scp -r user@example.com:/remote/folder ./','Recursively copy a remote directory to the local machine.','scp -r user@example.com:/remote/folder ./'),
        cheatItem('rsync -avz -e ssh ./folder/ user@example.com:/remote/folder/','Efficiently synchronize a directory over SSH.','rsync -avz -e ssh ./folder/ user@example.com:/remote/folder/'),
        cheatItem('ssh -L 8080:127.0.0.1:80 user@example.com','Forward local port 8080 through SSH to port 80 on the remote side.','ssh -L 8080:127.0.0.1:80 user@example.com'),
        cheatItem('ssh -R 9000:127.0.0.1:3000 user@example.com','Expose a local service through a remote-side port. Server policy may restrict this.','ssh -R 9000:127.0.0.1:3000 user@example.com','caution'),
        cheatItem('ssh -D 1080 user@example.com','Create a local SOCKS proxy through the SSH connection.','ssh -D 1080 user@example.com'),
        cheatItem('ssh -J jumpuser@jump.example.com user@target.internal','Connect through a jump/bastion host.','ssh -J jumpuser@jump.example.com user@target.internal'),
        cheatItem('ssh-keygen -R example.com','Remove the saved host key for a hostname. Only do this after understanding why its host key changed.','ssh-keygen -R example.com','caution','known hosts fingerprint'),
        cheatItem('ssh-keygen -lf ~/.ssh/id_ed25519.pub','Show the fingerprint of an SSH public key.','ssh-keygen -lf ~/.ssh/id_ed25519.pub'),
        cheatItem('sftp user@example.com','Open an interactive SFTP session over SSH.','sftp user@example.com'),
        cheatItem('ssh -A user@example.com','Forward your local SSH agent to the remote host. Use only with hosts you trust.','ssh -A user@example.com','caution','agent forwarding')
      ]
    },
    gparted: {
      label: 'GParted',
      items: [
        cheatItem('Confirm the target device','Check model, capacity, and device path before changing partitions. A wrong-device operation can destroy unrelated data.'),
        cheatItem('Refresh Devices','Use GParted → Refresh Devices after attaching/removing storage so the list reflects current hardware.'),
        cheatItem('Unmount before changing','Most filesystem resize/move/format operations require the partition to be unmounted first.','','caution'),
        cheatItem('Resize / Move','Shrink or grow a partition within available adjacent space. Shrinking/moving carries more risk than growing.','','caution'),
        cheatItem('Create New Partition','Create a partition in unallocated space and choose its filesystem/label.','','caution'),
        cheatItem('Delete Partition','Removes the partition definition and makes its space unallocated. Data becomes inaccessible and may be overwritten.','','danger'),
        cheatItem('Format Partition','Create a new filesystem on a partition. Existing filesystem data is destroyed.','','danger'),
        cheatItem('Set Label','Give a filesystem/partition a human-readable label; useful for identifying disks later.'),
        cheatItem('Manage Flags','Set flags such as boot, esp, bios_grub, or lvm only when the target boot/storage design requires them.','','caution'),
        cheatItem('Check / Repair','Ask filesystem tools to check/repair a selected filesystem. Back up important data first.','','caution'),
        cheatItem('Apply Pending Operations','GParted queues changes until Apply. Review the complete queue before committing it.','','danger'),
        cheatItem('Cancel / Undo queued operation','Before Apply, undo or clear queued changes if the preview is not exactly what you intended.'),
        cheatItem('Copy / Paste Partition','Clone a partition into suitable unallocated space. The copied filesystem may carry the same identifiers until changed.','','caution'),
        cheatItem('lsblk -f','List Linux block devices, filesystems, labels, UUIDs, and mount points.','lsblk -f'),
        cheatItem('blkid','Display filesystem types and UUIDs known to Linux.','blkid'),
        cheatItem('df -hT','Show mounted filesystem type, capacity, use, and mount point.','df -hT'),
        cheatItem('sudo umount /dev/sdb1','Unmount a filesystem before editing it. Active files/processes can prevent unmounting.','sudo umount /dev/sdb1','caution'),
        cheatItem('sudo swapoff /dev/sdb2','Disable a swap partition before modifying it. Ensure enough RAM/other swap is available.','sudo swapoff /dev/sdb2','caution')
      ]
    },
    docker: {
      label: 'Docker',
      items: [
        cheatItem('docker ps','List running containers.','docker ps'),
        cheatItem('docker ps -a','List running and stopped containers.','docker ps -a'),
        cheatItem('docker images','List locally stored images.','docker images'),
        cheatItem('docker pull nginx:latest','Download an image/tag from a registry.','docker pull nginx:latest'),
        cheatItem('docker build -t myapp:dev .','Build an image from the Dockerfile in the current directory.','docker build -t myapp:dev .'),
        cheatItem('docker run --rm myapp:dev','Run a container and remove it automatically when it exits.','docker run --rm myapp:dev'),
        cheatItem('docker run --rm -p 8080:80 nginx','Run a container and map host port 8080 to container port 80.','docker run --rm -p 8080:80 nginx'),
        cheatItem('docker exec -it <container> sh','Open an interactive shell inside a running container.','docker exec -it <container> sh'),
        cheatItem('docker logs -f <container>','Follow a container\'s stdout/stderr logs.','docker logs -f <container>'),
        cheatItem('docker inspect <container>','Display detailed container/image configuration as JSON.','docker inspect <container>'),
        cheatItem('docker stats','Show live container CPU, memory, and network usage.','docker stats'),
        cheatItem('docker stop <container>','Ask a running container to stop gracefully.','docker stop <container>','caution'),
        cheatItem('docker restart <container>','Restart a container, causing a temporary service interruption.','docker restart <container>','caution'),
        cheatItem('docker rm <container>','Remove a stopped container.','docker rm <container>','caution'),
        cheatItem('docker rm -f <container>','Force-stop and remove a container. Unsaved container-layer data is lost.','docker rm -f <container>','danger'),
        cheatItem('docker rmi <image>','Remove a local image if no dependent container prevents it.','docker rmi <image>','caution'),
        cheatItem('docker compose up -d','Create/start a Compose stack in the background.','docker compose up -d'),
        cheatItem('docker compose down','Stop and remove the Compose stack\'s containers/networks. Named volumes are kept unless -v is added.','docker compose down','caution'),
        cheatItem('docker compose logs -f','Follow logs from services in a Compose project.','docker compose logs -f'),
        cheatItem('docker compose build --no-cache','Rebuild Compose images without using cached build layers.','docker compose build --no-cache','caution'),
        cheatItem('docker volume ls','List Docker volumes.','docker volume ls'),
        cheatItem('docker system df','Show disk space used by Docker images, containers, and volumes.','docker system df'),
        cheatItem('docker system prune -a','Delete unused containers, networks, build cache, and unused images. Review before running.','docker system prune -a','danger'),
        cheatItem('docker volume prune','Delete unused volumes. Volumes can contain important persistent application data.','docker volume prune','danger')
      ]
    },
    http: {
      label: 'HTTP / API',
      items: [
        cheatItem('GET','Retrieve a representation/resource without intentionally changing it.','curl -i https://api.example.com/items'),
        cheatItem('HEAD','Request response headers without the normal response body.','curl -I https://api.example.com/items'),
        cheatItem('POST JSON','Create/submit data using a JSON request body.','curl -i -X POST https://api.example.com/items -H "Content-Type: application/json" -d \'{"name":"example"}\''),
        cheatItem('PUT JSON','Replace/update a resource at a known URL. PUT is generally intended to be idempotent.','curl -i -X PUT https://api.example.com/items/123 -H "Content-Type: application/json" -d \'{"name":"replacement"}\'','caution'),
        cheatItem('PATCH JSON','Apply a partial update to an existing resource.','curl -i -X PATCH https://api.example.com/items/123 -H "Content-Type: application/json" -d \'{"name":"changed"}\'','caution'),
        cheatItem('DELETE','Request deletion of a resource. Verify the endpoint/environment first.','curl -i -X DELETE https://api.example.com/items/123','danger'),
        cheatItem('OPTIONS','Ask the server which request methods/options are available; useful for CORS diagnostics.','curl -i -X OPTIONS https://api.example.com/items'),
        cheatItem('Bearer token','Send an Authorization: Bearer header. Replace the example token rather than committing real credentials to scripts/history.','curl -i https://api.example.com/me -H "Authorization: Bearer YOUR_TOKEN"','caution','auth token'),
        cheatItem('Basic authentication','Send HTTP Basic credentials. Prefer HTTPS and avoid exposing real passwords in shared shell history.','curl -i -u username:password https://api.example.com/me','caution','auth password'),
        cheatItem('Custom header','Add a custom request header.','curl -i https://api.example.com/items -H "X-Request-ID: test-123"'),
        cheatItem('Query parameters','Send URL query parameters with safe URL encoding.','curl -G https://api.example.com/search --data-urlencode "q=hello world"'),
        cheatItem('Form fields','Submit URL-encoded form fields.','curl -i -X POST https://api.example.com/login --data-urlencode "user=demo" --data-urlencode "password=example"','caution'),
        cheatItem('Multipart file upload','Upload a file using multipart/form-data.','curl -i https://api.example.com/upload -F "file=@./report.zip"','caution'),
        cheatItem('Download to file','Save the response body to a chosen filename. Existing files with that name are replaced.','curl -L https://example.com/file.zip -o file.zip','caution'),
        cheatItem('Follow redirects','Follow 3xx redirect responses.','curl -L https://example.com'),
        cheatItem('Verbose request','Show connection/TLS/request/response-header diagnostics. Be careful: verbose output may reveal auth headers.','curl -v https://api.example.com/items','caution'),
        cheatItem('Request timeout','Stop waiting after a maximum number of seconds.','curl --max-time 15 https://api.example.com/items'),
        cheatItem('Status code only','Discard the body and print just the HTTP status code.','curl -sS -o /dev/null -w "%{http_code}\\n" https://api.example.com/health'),
        cheatItem('Save response headers','Write response headers to a file while keeping the body on stdout.','curl -D headers.txt https://api.example.com/items','caution'),
        cheatItem('JSON response pretty-print with jq','Pipe a JSON API response through jq when installed.','curl -sS https://api.example.com/items | jq .')
      ]
    },
    sql: {
      label: 'SQL',
      items: [
        cheatItem('SELECT columns','Read selected columns from a table.','SELECT id, name FROM users;'),
        cheatItem('SELECT with WHERE','Filter rows by a condition.','SELECT id, name FROM users WHERE active = 1;'),
        cheatItem('ORDER BY + LIMIT','Sort results and return only the first rows.','SELECT id, name FROM users ORDER BY created_at DESC LIMIT 20;'),
        cheatItem('COUNT + GROUP BY','Aggregate rows by a category.','SELECT status, COUNT(*) FROM jobs GROUP BY status;'),
        cheatItem('INNER JOIN','Combine matching rows from related tables.','SELECT u.name, o.id FROM users u JOIN orders o ON o.user_id = u.id;'),
        cheatItem('LEFT JOIN','Keep every row from the left table even when no related row exists.','SELECT u.name, o.id FROM users u LEFT JOIN orders o ON o.user_id = u.id;'),
        cheatItem('INSERT','Insert a new row.','INSERT INTO users (name, active) VALUES (\'Alice\', 1);','caution'),
        cheatItem('UPDATE with WHERE','Change selected rows. Confirm the WHERE clause before execution.','UPDATE users SET active = 0 WHERE id = 123;','caution'),
        cheatItem('DELETE with WHERE','Delete selected rows. Confirm the WHERE clause and backups/transaction plan.','DELETE FROM users WHERE id = 123;','danger'),
        cheatItem('BEGIN transaction','Start an explicit transaction so related changes can be committed or rolled back together.','BEGIN;'),
        cheatItem('COMMIT','Permanently commit the current transaction.','COMMIT;','caution'),
        cheatItem('ROLLBACK','Undo changes in the current uncommitted transaction.','ROLLBACK;'),
        cheatItem('EXPLAIN','Ask the database to show its query plan rather than simply guessing why a query is slow.','EXPLAIN SELECT * FROM users WHERE email = \'a@example.com\';'),
        cheatItem('CREATE INDEX','Create an index that can speed matching/sorting but adds storage/write overhead.','CREATE INDEX idx_users_email ON users(email);','caution'),
        cheatItem('CREATE TABLE','Create a new table/schema object.','CREATE TABLE example (id INTEGER PRIMARY KEY, name VARCHAR(255));','caution'),
        cheatItem('ALTER TABLE','Change table structure. Syntax/locking behavior differs by database.','ALTER TABLE users ADD COLUMN last_seen TIMESTAMP;','caution'),
        cheatItem('TRUNCATE TABLE','Remove all rows very quickly. Transaction/identity behavior varies by database.','TRUNCATE TABLE users;','danger'),
        cheatItem('DROP TABLE','Delete a table definition and its data.','DROP TABLE users;','danger'),
        cheatItem('DELETE without WHERE','Deletes every row in the table. This is intentionally shown as a danger example.','DELETE FROM users;','danger'),
        cheatItem('UPDATE without WHERE','Updates every row in the table. This is intentionally shown as a danger example.','UPDATE users SET active = 0;','danger'),
        cheatItem('Find table columns (information_schema)','Portable-ish starting point for inspecting column metadata.','SELECT column_name, data_type FROM information_schema.columns WHERE table_name = \'users\';')
      ]
    },
    regex: {
      label: 'Regex',
      items: [
        cheatItem('^text$','Match an entire line/string exactly (subject to multiline mode).','^text$'),
        cheatItem('\\d+','Match one or more digits.','\\d+'),
        cheatItem('\\w+','Match one or more word characters in regex engines that support \\w.','\\w+'),
        cheatItem('\\s+','Match one or more whitespace characters.','\\s+'),
        cheatItem('[A-Za-z0-9_]','Match one character from an explicit alphanumeric/underscore class.','[A-Za-z0-9_]'),
        cheatItem('[^,]+','Match one or more characters that are not commas.','[^,]+'),
        cheatItem('(foo|bar)','Capture either foo or bar.','(foo|bar)'),
        cheatItem('(?:foo|bar)','Group alternatives without creating a numbered capture in PCRE-style engines.','(?:foo|bar)'),
        cheatItem('foo.*bar','Match foo followed later by bar; .* is greedy.','foo.*bar'),
        cheatItem('foo.*?bar','Lazy/non-greedy version in engines that support lazy quantifiers.','foo.*?bar'),
        cheatItem('\\bword\\b','Match word at word boundaries.','\\bword\\b'),
        cheatItem('colou?r','Match both color and colour; ? makes the preceding token optional.','colou?r'),
        cheatItem('a{2,5}','Match between two and five consecutive a characters.','a{2,5}'),
        cheatItem('^(?!.*debug).*$', 'Match a line only if it does not contain debug, using a negative lookahead in engines that support lookarounds.','^(?!.*debug).*$'),
        cheatItem('(?=suffix)','Positive lookahead: require suffix next without consuming it.','(?=suffix)'),
        cheatItem('(?<=prefix)text','Positive lookbehind example; support varies by regex engine.','(?<=prefix)text'),
        cheatItem('^[0-9a-fA-F]{8}$','Match exactly eight hexadecimal characters.','^[0-9a-fA-F]{8}$'),
        cheatItem('^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$','Match a canonical UUID with a standard version/variant nibble.','^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$'),
        cheatItem('^\\d{4}-\\d{2}-\\d{2}$','Match an ISO-shaped YYYY-MM-DD date string (format only, not calendar validity).','^\\d{4}-\\d{2}-\\d{2}$'),
        cheatItem('^v?\\d+\\.\\d+\\.\\d+(?:[-+][0-9A-Za-z.-]+)?$','Useful simplified semantic-version shape.','^v?\\d+\\.\\d+\\.\\d+(?:[-+][0-9A-Za-z.-]+)?$'),
        cheatItem('^[^@\\s]+@[^@\\s]+\\.[^@\\s]+$','Simple email-shaped validation pattern; real email syntax is more complex.','^[^@\\s]+@[^@\\s]+\\.[^@\\s]+$'),
        cheatItem('https?://[^\\s]+','Find simple HTTP/HTTPS URL-shaped text.','https?://[^\\s]+'),
        cheatItem('(?m)^ERROR.*$','PCRE-style multiline example that selects lines beginning with ERROR.','(?m)^ERROR.*$')
      ]
    }
  };

  function cheatLandingWorkspace(host) {
    const categories = Object.entries(MONITOR_CHEATS);
    host.innerHTML = `<div class="monitor-workspace-head"><div class="monitor-workspace-title">Cheat Sheets</div><div class="monitor-last-update">Developer quick reference</div></div><div class="monitor-cheat-landing"><p>Choose a quick-reference category. Commands are curated for common developer/admin work rather than exhaustive manuals.</p><div class="monitor-cheat-categories">${categories.map(([key,category])=>`<button type="button" data-cheat-category="${escapeHtml(key)}">${escapeHtml(category.label)}</button>`).join('')}</div><div class="monitor-cheat-legend"><span class="normal">Normal</span><span class="caution">Caution</span><span class="danger">Destructive / high risk</span></div></div>`;
  }

  function renderCheatsWorkspace(host) {
    const category = MONITOR_CHEATS[monitor.cheatCategory];
    if (!category) return cheatLandingWorkspace(host);
    const q = currentTabFilter();
    const items = category.items.filter(item => !q || `${item.title} ${item.description} ${item.copy} ${item.tags || ''}`.toLowerCase().includes(q));
    const cards = items.map(item => {
      const risk = ['danger','caution'].includes(item.risk) ? item.risk : 'normal';
      const copyable = String(item.copy || '').trim();
      const badge = risk === 'danger' ? 'DANGER' : risk === 'caution' ? 'CAUTION' : copyable ? 'COPY' : 'REFERENCE';
      return `<article class="monitor-cheat-entry risk-${risk}${copyable ? ' is-copyable' : ''}" ${copyable ? `data-cheat-copy="${escapeHtml(copyable)}" tabindex="0" role="button" title="Click to copy the example"` : ''}><div class="monitor-cheat-command"><code>${escapeHtml(item.title)}</code><span>${badge}</span></div><p>${escapeHtml(item.description)}</p>${copyable && copyable !== item.title ? `<div class="monitor-cheat-example">${escapeHtml(copyable)}</div>` : ''}</article>`;
    }).join('');
    host.innerHTML = `<div class="monitor-workspace-head monitor-cheat-head"><div class="monitor-workspace-title">Cheat Sheets · ${escapeHtml(category.label)}</div><input class="monitor-process-search monitor-tab-search" data-monitor-tab-search type="search" value="${escapeHtml(monitor.tabFilter.cheats || '')}" placeholder="Search command or description…"/><button class="monitor-action-btn" type="button" data-cheat-back>Back</button></div><div class="monitor-cheat-scroll">${cards || '<div class="monitor-empty">No cheat entries match this search.</div>'}</div>`;
  }

  function renderSystemInfoWorkspace(host) {
    const snapshot = monitor.state?.snapshot || {};
    const system = snapshot.system || {};
    const details = Array.isArray(snapshot.system_details) ? snapshot.system_details : [];
    const rows = [
      ['OS',[system.os,system.os_version].filter(Boolean).join(' · ')],['Architecture',system.architecture],['Device',system.hostname],['CPU',system.cpu_model],['GPU',system.gpu_model],['RAM',system.total_memory_bytes?formatBytes(system.total_memory_bytes):''],['Storage',system.storage_total_bytes?`${formatBytes(system.storage_total_bytes)} total · ${formatBytes(system.storage_free_bytes)} free`:''],['Uptime',system.uptime_seconds?formatUptime(system.uptime_seconds):'']
    ].concat(details.map(item => [item.label,item.value])).filter(row => String(row[1] || '').trim());
    const caps = snapshot.capabilities || {};
    const capRows = [['CPU usage',caps.cpu_usage],['GPU usage',caps.gpu_usage],['Temperatures',caps.temperatures],['Fans',caps.fans],['Processes',caps.processes],['Startup items',caps.startup_items],['Services',caps.services],['Connections',caps.connections],['System logs',caps.system_logs],['Security events',caps.security_events]];
    host.innerHTML = `${workspaceHead('System Info')}<div class="monitor-system-detail-scroll"><div class="monitor-system-detail-grid">${rows.map(([label,value])=>`<div class="monitor-system-detail-item"><span>${escapeHtml(label)}</span><strong>${escapeHtml(value)}</strong></div>`).join('')}</div><div class="monitor-capability-title">Collector Capabilities</div><div class="monitor-capability-grid">${capRows.map(([label,available])=>`<div class="monitor-capability ${available?'is-available':''}"><span>${available?'●':'○'}</span>${escapeHtml(label)}</div>`).join('')}</div></div>`;
  }

  function renderLogsWorkspace(host) {
    const items = Array.isArray(monitor.state?.snapshot?.logs) ? monitor.state.snapshot.logs : [];
    const filtered = items.filter(item => rowMatchesFilter([item.time,item.level,item.source,item.event_id,item.message]));
    const rows = filtered.map(item => `<tr><td>${escapeHtml(item.time ? localTime(item.time) : '—')}</td><td class="monitor-level-${escapeHtml(String(item.level || 'info').toLowerCase())}">${escapeHtml(item.level || 'Info')}</td><td>${escapeHtml(item.source || '—')}</td><td>${escapeHtml(item.event_id || '—')}</td><td title="${escapeHtml(item.message || '')}">${escapeHtml(item.message || '—')}</td></tr>`).join('');
    host.innerHTML = `${workspaceHead('Logs',{search:true,placeholder:'Search log source, level, event, or message…'})}<div class="monitor-generic-table-wrap"><table class="monitor-generic-table"><thead><tr><th>Time</th><th>Level</th><th>Source</th><th>Event</th><th>Message</th></tr></thead><tbody>${rows || '<tr><td colspan="5"><div class="monitor-empty">No relevant operating-system log records are available yet.</div></td></tr>'}</tbody></table></div>`;
  }

  function renderSecurityWorkspace(host) {
    const items = Array.isArray(monitor.state?.snapshot?.security) ? monitor.state.snapshot.security : [];
    const filtered = items.map((item,index)=>({item,index})).filter(({item}) => rowMatchesFilter([item.time,item.severity,item.source,item.type,item.event_id,item.path,item.message]));
    const rows = filtered.map(({item,index}) => `<tr><td>${escapeHtml(item.time ? localTime(item.time) : '—')}</td><td class="monitor-level-${escapeHtml(String(item.severity || 'notice').toLowerCase())}">${escapeHtml(item.severity || 'Notice')}</td><td>${escapeHtml(item.source || '—')}</td><td>${escapeHtml(item.type || '—')}</td><td>${escapeHtml(item.event_id || '—')}</td><td title="${escapeHtml([item.message,item.path].filter(Boolean).join(' · '))}">${escapeHtml(item.message || item.path || '—')}</td><td>${item.path ? `<button class="monitor-inline-inspect" type="button" data-monitor-security-index="${index}">Inspect</button>` : '—'}</td></tr>`).join('');
    host.innerHTML = `${workspaceHead('Security Evidence',{search:true,placeholder:'Search security source, event, path, or message…'})}<div class="monitor-security-note">Objective evidence only. AgentGO does not label software as malware on its own; Inspect lets you investigate a resolvable file.</div><div class="monitor-generic-table-wrap"><table class="monitor-generic-table"><thead><tr><th>Time</th><th>Severity</th><th>Source</th><th>Type</th><th>Event</th><th>Evidence</th><th></th></tr></thead><tbody>${rows || '<tr><td colspan="7"><div class="monitor-empty">No security-tool evidence was returned by the current platform collector.</div></td></tr>'}</tbody></table></div>`;
  }

  function renderWorkspace() {
    const host = document.getElementById('monitorWorkspace');
    if (!host) return;
    host.dataset.monitorView = monitor.tab;
    if (monitor.tab === 'processes') {
      host.innerHTML = `
        <div class="monitor-workspace-head"><div class="monitor-workspace-title">Processes</div><input class="monitor-process-search" id="monitorProcessSearch" type="search" value="${escapeHtml(monitor.processFilter)}" placeholder="Search process, path, user, publisher, or PID…"/><div class="monitor-last-update" id="monitorProcessLastUpdate">${monitor.state?.snapshot?.timestamp ? `Updated ${localTime(monitor.state.snapshot.timestamp)}` : 'Waiting for data'}</div></div>
        <div class="monitor-process-table-wrap"><table class="monitor-process-table"><thead><tr><th style="width:34px"></th>${tableSortHeader('processes','name','Name','width:220px')}${tableSortHeader('processes','pid','PID','width:72px')}${tableSortHeader('processes','file_size','File Size','width:88px')}${tableSortHeader('processes','cpu','CPU','width:74px')}${tableSortHeader('processes','memory','Memory','width:92px')}${tableSortHeader('processes','gpu','GPU','width:68px')}${tableSortHeader('processes','status','Status','width:82px')}</tr></thead><tbody id="monitorProcessBody"></tbody></table></div>
        ${itemActionBarHTML({advanced:true,kind:'process'})}
        <div class="monitor-process-details${monitor.detailsOpen && monitor.selectedPID ? ' open' : ''}" id="monitorProcessDetails"></div>`;
      renderProcessRows();
      renderProcessDetails();
      return;
    }
    if (monitor.tab === 'performance') return renderPerformanceWorkspace(host);
    if (monitor.tab === 'startup-apps') return renderStartupWorkspace(host);
    if (monitor.tab === 'services') return renderServicesWorkspace(host);
    if (monitor.tab === 'cpu-detail') return renderCPUWorkspace(host);
    if (monitor.tab === 'gpu-detail') return renderGPUWorkspace(host);
    if (monitor.tab === 'memory-detail') return renderMemoryWorkspace(host);
    if (monitor.tab === 'disk-detail') return renderDiskWorkspace(host);
    if (monitor.tab === 'network') return renderNetworkWorkspace(host);
    if (monitor.tab === 'system-info') return renderSystemInfoWorkspace(host);
    if (monitor.tab === 'logs') return renderLogsWorkspace(host);
    if (monitor.tab === 'security') return renderSecurityWorkspace(host);
    if (monitor.tab === 'cheats') return renderCheatsWorkspace(host);
    host.innerHTML = `${workspaceHead('Monitor Tool')}<div class="monitor-empty">This Monitor tool is not available.</div>`;
  }

  function filteredProcesses() {
    const list = Array.isArray(monitor.state?.snapshot?.processes) ? monitor.state.snapshot.processes : [];
    const q = monitor.processFilter;
    if (!q) return list;
    return list.filter(proc => `${proc.name || ''} ${proc.path || ''} ${proc.user || ''} ${proc.publisher || ''} ${proc.signature || ''} ${proc.version || ''} ${proc.pid || ''}`.toLowerCase().includes(q));
  }

  function renderProcessRows() {
    const body = document.getElementById('monitorProcessBody');
    if (!body) return;
    const settings = settingsFromState();
    const list = applyTableSort(filteredProcesses(),'processes',(proc,key) => ({
      name:proc.name,
      pid:number(proc.pid),
      file_size:number(proc.file_size_bytes) || null,
      cpu:nullableNumber(proc.cpu_percent),
      memory:number(proc.memory_bytes) || null,
      gpu:nullableNumber(proc.gpu_percent),
      status:proc.status,
    })[key]);
    if (!settings.enabled && !list.length) {
      body.innerHTML = '<tr><td colspan="8"><div class="monitor-empty">Turn Monitor ON to begin live process sampling.</div></td></tr>';
      return;
    }
    if (!list.length) {
      body.innerHTML = '<tr><td colspan="8"><div class="monitor-empty">No processes match this view yet.</div></td></tr>';
      return;
    }
    if (monitor.selectedPID && !list.some(proc => number(proc.pid) === monitor.selectedPID) && !monitor.processFilter) {
      monitor.selectedPID = 0;
      if (monitor.selectedItemRef?.kind === 'process') monitor.selectedItemRef = null;
      monitor.itemDetail = null;
      monitor.detailsOpen = false;
    }
    body.innerHTML = list.map(proc => {
      const selected = number(proc.pid) === monitor.selectedPID;
      const cpu = nullableNumber(proc.cpu_percent);
      const gpu = nullableNumber(proc.gpu_percent);
      const cpuHot = cpu !== null && cpu >= 25;
      const gpuHot = gpu !== null && gpu >= 35;
      return `<tr class="${selected?'is-selected':''}" data-monitor-process-pid="${number(proc.pid)}" title="${escapeHtml(proc.path || proc.name || '')}"><td><span class="monitor-proc-radio${selected?' is-selected':''}"></span></td><td class="monitor-proc-name">${escapeHtml(proc.name || `PID ${proc.pid}`)}</td><td>${number(proc.pid)}</td><td>${proc.file_size_bytes ? formatBytes(proc.file_size_bytes) : '—'}</td><td class="${cpuHot?'monitor-proc-hot':''}">${cpu === null ? '—' : `${cpu.toFixed(cpu<10?1:0)}%`}</td><td>${proc.memory_bytes ? formatBytes(proc.memory_bytes) : '—'}</td><td class="${gpuHot?'monitor-proc-hot':''}">${gpu === null ? '—' : `${gpu.toFixed(gpu<10?1:0)}%`}</td><td>${escapeHtml(proc.status || '—')}</td></tr>`;
    }).join('');
  }

  function selectedProcess() {
    const list = Array.isArray(monitor.state?.snapshot?.processes) ? monitor.state.snapshot.processes : [];
    return list.find(proc => number(proc.pid) === monitor.selectedPID) || null;
  }

  function renderSelectedProcessHistory(pid) {
    const samples = monitor.processHistory[String(number(pid))] || [];
    if (!samples.length) return '<div class="monitor-process-history"><span>Recent resource history will build while this process is selected.</span></div>';
    const cpu = samples.map(s=>s.cpu).filter(v=>v!==null); const gpu = samples.map(s=>s.gpu).filter(v=>v!==null); const ram = samples.map(s=>s.ram).filter(v=>v>0);
    const avg = values => values.length ? values.reduce((a,b)=>a+b,0)/values.length : null;
    const peak = values => values.length ? Math.max(...values) : null;
    return `<div class="monitor-process-history"><span>Recent selected-process history (${samples.length} samples)</span><strong>CPU avg ${formatPercent(avg(cpu),1)} · peak ${formatPercent(peak(cpu),1)}</strong><strong>GPU avg ${formatPercent(avg(gpu),1)} · peak ${formatPercent(peak(gpu),1)}</strong><strong>RAM avg ${ram.length?formatBytes(avg(ram)):'—'} · peak ${ram.length?formatBytes(peak(ram)):'—'}</strong></div>`;
  }

  function renderProcessDetails() {
    const host = document.getElementById('monitorProcessDetails');
    if (!host || !monitor.detailsOpen) return;
    const proc = selectedProcess();
    if (!proc) { host.innerHTML = ''; return; }
    const detail = monitor.itemDetail;
    if (!detail || detail.pid !== number(proc.pid)) {
      host.innerHTML = '<div class="monitor-item-loading"><span class="monitor-item-spinner"></span>Loading current process evidence…</div>';
      loadSelectedItemDetail({ silent:true, rerender:true });
      return;
    }
    const connections = Array.isArray(detail.connections) ? detail.connections : [];
    const security = Array.isArray(detail.security_evidence) ? detail.security_evidence : [];
    host.innerHTML = `<div class="monitor-process-detail-head"><span class="monitor-review-badge" data-state="${escapeHtml(detail.review_state || 'needs_review')}">${escapeHtml(reviewStateLabel(detail.review_state))}</span>${detail.previous_sha256 ? '<span class="monitor-changed-callout">Binary changed since prior review</span>' : ''}</div>
      <div class="monitor-process-detail-grid">
        <div><strong>Executable:</strong> ${escapeHtml(detail.name || proc.name || '—')}</div><div><strong>PID:</strong> ${number(detail.pid)} · <strong>Parent:</strong> ${number(detail.parent_pid) || '—'}</div>
        <div class="wide"><strong>Path:</strong> ${escapeHtml(detail.path || 'Unavailable')}</div><div><strong>File size:</strong> ${detail.file_size_bytes ? formatBytes(detail.file_size_bytes) : 'Unavailable'}</div><div><strong>User:</strong> ${escapeHtml(detail.user || 'Unavailable')}</div>
        <div><strong>Started:</strong> ${escapeHtml(detail.start_time ? localTime(detail.start_time) : 'Unavailable')}</div><div><strong>Version:</strong> ${escapeHtml(detail.version || 'Unavailable')}</div><div><strong>Publisher:</strong> ${escapeHtml(detail.publisher || 'Unavailable')}</div><div><strong>Signature:</strong> ${escapeHtml(detail.signature || 'Unavailable')}</div>
        <div class="wide"><strong>SHA-256:</strong> <code>${escapeHtml(detail.sha256 || 'Not calculated')}</code></div>
        ${detail.previous_sha256 ? `<div class="wide"><strong>Previous reviewed hash:</strong> <code>${escapeHtml(detail.previous_sha256)}</code></div>` : ''}
      </div>
      ${renderSelectedProcessHistory(detail.pid)}
      <div class="monitor-process-evidence-row"><span>${connections.length} related network connection${connections.length===1?'':'s'}</span><span>${security.length} related security event${security.length===1?'':'s'}</span><button class="monitor-inline-inspect" type="button" data-monitor-item-action="details">Open Full Details</button></div>`;
  }

  function updateHistory(snapshot) {
    const values = {
      cpu: nullableNumber(snapshot.cpu_percent),
      gpu: nullableNumber(snapshot.gpu_percent),
      memory: nullableNumber(snapshot.memory_percent),
      disk: nullableNumber(snapshot.disk_percent),
      network: (() => {
        const rx = nullableNumber(snapshot.network_rx_bytes_per_sec) || 0;
        const tx = nullableNumber(snapshot.network_tx_bytes_per_sec) || 0;
        if (snapshot.network_rx_bytes_per_sec == null && snapshot.network_tx_bytes_per_sec == null) return null;
        // Logarithmic visual scale keeps common desktop throughput readable.
        return Math.min(100, Math.log10(1 + rx + tx) * 13);
      })(),
    };
    Object.entries(values).forEach(([key,value]) => {
      if (value === null) return;
      const list = monitor.history[key];
      list.push(Math.max(0, Math.min(100, value)));
      while (list.length > 40) list.shift();
    });
    if (monitor.selectedPID) {
      const proc = (Array.isArray(snapshot.processes) ? snapshot.processes : []).find(item => number(item.pid) === monitor.selectedPID);
      if (proc) {
        const key = String(monitor.selectedPID);
        const list = monitor.processHistory[key] || [];
        list.push({ cpu:nullableNumber(proc.cpu_percent), gpu:nullableNumber(proc.gpu_percent), ram:number(proc.memory_bytes), at:Date.now() });
        while (list.length > 40) list.shift();
        monitor.processHistory[key] = list;
        const keys = Object.keys(monitor.processHistory);
        if (keys.length > 12) keys.slice(0, keys.length - 12).forEach(old => delete monitor.processHistory[old]);
      }
    }
  }

  function polyline(values, height = 20) {
    if (!Array.isArray(values) || values.length < 2) return `0,${height-2} 100,${height-2}`;
    const last = values.slice(-40);
    const step = 100 / Math.max(1,last.length-1);
    return last.map((v,i)=>`${(i*step).toFixed(1)},${(height-2-(Math.max(0,Math.min(100,v))/100)*(height-5)).toFixed(1)}`).join(' ');
  }

  function sparkArea(values, height = 20) {
    const baseline = height - 2;
    return `0,${baseline} ${polyline(values,height)} 100,${baseline}`;
  }

  function updateSpark(svgID, values, height) {
    const root = document.getElementById(svgID);
    if (!root) return;
    const line = root.querySelector('polyline');
    const area = root.querySelector('.monitor-spark-area');
    if (line) line.setAttribute('points',polyline(values,height));
    if (area) area.setAttribute('points',sparkArea(values,height));
  }

  function setGauge(kind, value, meta, color) {
    const gauge = document.getElementById(`monitorGauge-${kind}`);
    const label = document.getElementById(`monitorGaugeValue-${kind}`);
    const metaEl = document.getElementById(`monitorMetricMeta-${kind}`);
    const n = nullableNumber(value);
    if (gauge) {
      gauge.style.setProperty('--gauge-value', n === null ? 0 : Math.max(0,Math.min(100,n)));
      if (color) gauge.style.setProperty('--gauge-color', color);
    }
    if (label) label.textContent = n === null ? '—' : `${Math.round(n)}%`;
    if (metaEl) metaEl.innerHTML = meta;
    updateSpark(`monitorSpark-${kind}`, monitor.history[kind], 20);
  }

  function tempColor(temp) {
    const v = nullableNumber(temp);
    if (v === null) return '#45cfff';
    if (v >= 90) return '#ff5d7d';
    if (v >= 78) return '#ef78c7';
    if (v >= 62) return '#f2c15f';
    if (v >= 45) return '#55dfa3';
    return '#4fcfff';
  }

  function renderOverview(snapshot) {
    const cpuTemp = nullableNumber(snapshot.cpu_temperature_c);
    const gpuTemp = nullableNumber(snapshot.gpu_temperature_c);
    setGauge('cpu', snapshot.cpu_percent, cpuTemp === null ? '<span class="monitor-na">Temp unavailable</span>' : `<strong style="color:${tempColor(cpuTemp)}">${formatTemp(cpuTemp)}</strong>`, tempColor(cpuTemp));
    setGauge('gpu', snapshot.gpu_percent, gpuTemp === null ? '<span class="monitor-na">Usage/temp varies by OS</span>' : `<strong style="color:${tempColor(gpuTemp)}">${formatTemp(gpuTemp)}</strong>`, tempColor(gpuTemp));
    setGauge('memory', snapshot.memory_percent, `<span class="monitor-metric-detail">${formatBytes(snapshot.memory_used_bytes)} / ${formatBytes(snapshot.memory_total_bytes)}</span>`, '#c778ee');
    setGauge('disk', snapshot.disk_percent, `<span class="monitor-metric-detail">R ${formatRate(snapshot.disk_read_bytes_per_sec)}<br>W ${formatRate(snapshot.disk_write_bytes_per_sec)}</span>`, '#4baeff');
    const rx = nullableNumber(snapshot.network_rx_bytes_per_sec);
    const tx = nullableNumber(snapshot.network_tx_bytes_per_sec);
    const networkScale = rx === null && tx === null ? null : Math.min(100, Math.log10(1 + (rx||0)+(tx||0))*13);
    setGauge('network', networkScale, `<span class="monitor-metric-detail">↓ ${formatRate(rx)}<br>↑ ${formatRate(tx)}</span>`, '#48e2ae');
  }

  function renderSystemInfo(snapshot) {
    const host = document.getElementById('monitorSystemInfo');
    if (!host) return;
    const system = snapshot.system || {};
    const rows = [
      ['OS', [system.os,system.os_version].filter(Boolean).join(' · ') || 'Unavailable'],
      ['CPU', system.cpu_model || 'Unavailable'],
      ['GPU', system.gpu_model || 'Unavailable'],
      ['RAM', system.total_memory_bytes ? formatBytes(system.total_memory_bytes) : 'Unavailable'],
      ['Storage', system.storage_total_bytes ? `${formatBytes(system.storage_total_bytes)} total · ${formatBytes(system.storage_free_bytes)} free` : 'Unavailable'],
      ['Arch', system.architecture || 'Unavailable'],
      ['Device', system.hostname || 'Unavailable'],
      ['Uptime', system.uptime_seconds ? formatUptime(system.uptime_seconds) : 'Unavailable'],
    ];
    host.innerHTML = rows.map(([key,value])=>`<div class="monitor-info-row"><div class="monitor-info-key">${escapeHtml(key)}</div><div class="monitor-info-value">${escapeHtml(value)}</div></div>`).join('');
  }

  function quickCycleHTML(slot) {
    return `<span class="monitor-card-cycle" aria-label="Change card view"><button type="button" data-quick-slot="${escapeHtml(slot)}" data-quick-cycle="-1" title="Previous view">↑</button><button type="button" data-quick-slot="${escapeHtml(slot)}" data-quick-cycle="1" title="Next view">↓</button></span>`;
  }

  function quickCardHeader(title, subtitle, slot) {
    return `<div class="monitor-quick-title"><span>${escapeHtml(title)}</span><span class="monitor-quick-title-tools"><small>${escapeHtml(subtitle || '')}</small>${quickCycleHTML(slot)}</span></div>`;
  }

  function miniChartHTML(id, cls = '') {
    return `<svg class="monitor-mini-chart ${escapeHtml(cls)}" id="${escapeHtml(id)}" viewBox="0 0 100 30" preserveAspectRatio="none"><polygon class="monitor-spark-area" points="0,28 100,28"></polygon><line class="monitor-zero-line" x1="0" y1="28" x2="100" y2="28"></line><polyline points="0,28 100,28"></polyline></svg>`;
  }

  function thermalSensorKind(sensor) {
    return `${sensor?.kind || ''} ${sensor?.name || ''}`.toLowerCase();
  }

  function availableThermalViews(snapshot) {
    const sensors = Array.isArray(snapshot.thermals) ? snapshot.thermals : [];
    const fans = Array.isArray(snapshot.fans) ? snapshot.fans : [];
    const views = ['overview'];
    if (nullableNumber(snapshot.cpu_temperature_c) !== null || sensors.some(sensor => /cpu|package|core/.test(thermalSensorKind(sensor)))) views.push('cpu');
    if (nullableNumber(snapshot.gpu_temperature_c) !== null || sensors.some(sensor => /gpu|graphics/.test(thermalSensorKind(sensor)))) views.push('gpu');
    if (sensors.some(sensor => /ssd|nvme|storage|disk|drive/.test(thermalSensorKind(sensor)))) views.push('storage');
    if (fans.length) views.push('fans');
    if (String(snapshot.thermal_state || '').trim()) views.push('thermal-state');
    return views;
  }

  function thermalViewLabel(view) {
    return ({overview:'Overview',cpu:'CPU',gpu:'GPU',storage:'Storage',fans:'Fans', 'thermal-state':'OS State'})[view] || 'Overview';
  }

  function effectiveThermalView(snapshot) {
    const available = availableThermalViews(snapshot);
    const wanted = String(settingsFromState().thermal_card_view || 'overview');
    return available.includes(wanted) ? wanted : available[0];
  }

  function renderThermalCard(snapshot) {
    const host = document.getElementById('monitorQuickThermalCard');
    if (!host) return;
    const view = effectiveThermalView(snapshot);
    const sensors = Array.isArray(snapshot.thermals) ? snapshot.thermals : [];
    const fans = Array.isArray(snapshot.fans) ? snapshot.fans : [];
    let body = '';
    if (view === 'thermal-state') {
      const state = String(snapshot.thermal_state || '').trim();
      body = `<div class="monitor-quick-big">${escapeHtml(state || 'Unavailable')}</div><div class="monitor-quick-sub">Operating-system thermal condition</div>`;
    } else if (view === 'fans') {
      body = fans.length ? `<div class="monitor-thermal-list">${fans.slice(0,5).map(fan=>`<div class="monitor-card-stat-row"><span>${escapeHtml(shortSensorName(fan.name || 'Fan'))}</span><strong>${number(fan.rpm).toFixed(0)} RPM</strong></div>`).join('')}</div>` : '<div class="monitor-empty">No fan sensors exposed by this platform.</div>';
    } else {
      let filtered = sensors;
      if (view === 'cpu') filtered = sensors.filter(sensor => /cpu|package|core/.test(thermalSensorKind(sensor)));
      if (view === 'gpu') filtered = sensors.filter(sensor => /gpu|graphics/.test(thermalSensorKind(sensor)));
      if (view === 'storage') filtered = sensors.filter(sensor => /ssd|nvme|storage|disk|drive/.test(thermalSensorKind(sensor)));
      const synthetic = [];
      if (view === 'cpu' && nullableNumber(snapshot.cpu_temperature_c) !== null && !filtered.length) synthetic.push({name:'CPU',temperature_c:snapshot.cpu_temperature_c});
      if (view === 'gpu' && nullableNumber(snapshot.gpu_temperature_c) !== null && !filtered.length) synthetic.push({name:'GPU',temperature_c:snapshot.gpu_temperature_c});
      if (view === 'overview') {
        if (nullableNumber(snapshot.cpu_temperature_c) !== null && !filtered.some(sensor=>/cpu/.test(thermalSensorKind(sensor)))) synthetic.push({name:'CPU',temperature_c:snapshot.cpu_temperature_c});
        if (nullableNumber(snapshot.gpu_temperature_c) !== null && !filtered.some(sensor=>/gpu/.test(thermalSensorKind(sensor)))) synthetic.push({name:'GPU',temperature_c:snapshot.gpu_temperature_c});
      }
      const items = [...synthetic, ...filtered];
      body = items.length ? `<div class="monitor-thermal-list">${items.slice(0,5).map(sensor => {
        const value = nullableNumber(sensor.temperature_c);
        const width = value === null ? 0 : Math.max(3,Math.min(100,value));
        const cls = value !== null && value >= 85 ? ' is-hot' : value !== null && value >= 70 ? ' is-warm' : '';
        return `<div class="monitor-thermal-row" title="${escapeHtml(sensor.name || 'Sensor')}"><span>${escapeHtml(shortSensorName(sensor.name || 'Sensor'))}</span><div class="monitor-temp-bar"><div class="monitor-temp-fill${cls}" style="width:${width}%"></div></div><span class="monitor-temp-value">${formatTemp(value)}</span></div>`;
      }).join('')}</div>` : `<div class="monitor-empty">${escapeHtml(snapshot.thermal_state ? `Thermal state: ${snapshot.thermal_state}` : 'No temperature sensor exposed by this platform.')}</div>`;
    }
    host.innerHTML = `${quickCardHeader('Thermals', thermalViewLabel(view), 'thermal')}${body}`;
  }

  function shortSensorName(name) {
    const value = String(name || '').trim();
    if (value.length <= 13) return value;
    return value.slice(0,12) + '…';
  }

  function availableGeneralQuickCards(snapshot) {
    const caps = snapshot.capabilities || {};
    const available = ['top-load','security'];
    if (caps.connections || (snapshot.connections || []).length) available.push('network-ports');
    if (snapshot.disk_total_bytes || snapshot.system?.storage_total_bytes) available.push('storage-health');
    if (snapshot.system?.gpu_model || nullableNumber(snapshot.gpu_percent) !== null || nullableNumber(snapshot.gpu_temperature_c) !== null) available.push('gpu-vram');
    available.push('system-uptime');
    return [...new Set(available)];
  }

  function generalCardLabel(type) {
    return ({'top-load':'Top Load','security':'Security','network-ports':'Network','storage-health':'Storage Health','gpu-vram':'GPU / VRAM','system-uptime':'Uptime'})[type] || 'System';
  }

  function effectiveQuickCardTypes(snapshot) {
    const settings = settingsFromState();
    const available = availableGeneralQuickCards(snapshot);
    const requested = [settings.quick_card_2 || 'top-load', settings.quick_card_3 || 'network-ports', settings.quick_card_4 || 'security'];
    const defaults = ['top-load','network-ports','security','storage-health','gpu-vram','system-uptime'];
    const used = new Set();
    return requested.map(wanted => {
      let chosen = available.includes(wanted) && !used.has(wanted) ? wanted : '';
      if (!chosen) chosen = defaults.find(candidate => available.includes(candidate) && !used.has(candidate)) || available.find(candidate => !used.has(candidate)) || 'system-uptime';
      used.add(chosen);
      return chosen;
    });
  }

  function topProcessBy(processes, field) {
    return processes.reduce((best, proc) => {
      const value = field === 'memory_bytes' ? number(proc?.[field]) : nullableNumber(proc?.[field]);
      const bestValue = best ? (field === 'memory_bytes' ? number(best[field]) : nullableNumber(best[field])) : null;
      if (value === null || value === undefined) return best;
      if (!best || bestValue === null || value > bestValue) return proc;
      return best;
    }, null);
  }

  function renderGeneralQuickCard(host, slot, type, snapshot) {
    if (!host) return;
    const processes = Array.isArray(snapshot.processes) ? snapshot.processes : [];
    const security = Array.isArray(snapshot.security) ? snapshot.security : [];
    const connections = Array.isArray(snapshot.connections) ? snapshot.connections : [];
    let subtitle = '';
    let body = '';
    let chart = null;
    if (type === 'top-load') {
      subtitle = `${processes.length} processes`;
      const cpu = topProcessBy(processes,'cpu_percent');
      const gpu = topProcessBy(processes,'gpu_percent');
      const ram = topProcessBy(processes,'memory_bytes');
      const row = (label, proc, value) => `<div class="monitor-card-stat-row"><span>${label} · ${escapeHtml(proc?.name || 'Unavailable')}</span><strong>${escapeHtml(value)}</strong></div>`;
      body = `<div class="monitor-card-stat-list">${row('CPU',cpu,cpu ? formatPercent(cpu.cpu_percent,1) : '—')}${row('GPU',gpu,gpu ? formatPercent(gpu.gpu_percent,1) : '—')}${row('RAM',ram,ram ? formatBytes(ram.memory_bytes) : '—')}</div>`;
    } else if (type === 'security') {
      const aiSummary = monitor.aiState?.summary || monitor.state?.ai || {};
      const lastAI = String(aiSummary.last_report_status || '').trim().toLowerCase();
      const activeFindings = Math.max(0, number(aiSummary.active_finding_count));
      const warningWithoutKey = ['warning','critical'].includes(lastAI) && activeFindings === 0 ? 1 : 0;
      const needsAttention = activeFindings + warningWithoutKey;
      subtitle = security.length ? `${security.length} recent event${security.length === 1 ? '' : 's'}` : 'No recent events';
      const aiLabel = lastAI ? lastAI.toUpperCase() : 'NOT YET RUN';
      const aiGood = !lastAI || ['normal','notice'].includes(lastAI);
      body = `<div class="monitor-quick-big${needsAttention?' monitor-security-attention':''}">${needsAttention}</div><div class="monitor-quick-sub">Needs Attention</div><div class="monitor-card-note${security.length ? '' : ' is-good'}">${security.length} recent security event${security.length === 1 ? '' : 's'}</div><div class="monitor-card-note${aiGood ? ' is-good' : ''}">AI Review: ${escapeHtml(aiLabel)}</div>`;
    } else if (type === 'network-ports') {
      const listeners = connections.filter(isListeningConnectionJS);
      const active = connections.filter(item => !isListeningConnectionJS(item));
      subtitle = `${connections.length} connections`;
      const featured = active.filter(isEstablishedConnectionJS).find(item => {
        const process = String(item.process || '').trim();
        const hasOwner = (process && process !== '—') || number(item.pid) > 0;
        const hasLocal = String(item.local_address || '').trim() || number(item.local_port) > 0;
        const hasRemote = String(item.remote_address || '').trim() || number(item.remote_port) > 0;
        return hasOwner && hasLocal && hasRemote;
      });
      let footer = '';
      if (featured) {
        const process = String(featured.process || '').trim();
        const owner = process && process !== '—' ? process : `PID ${number(featured.pid)}`;
        const protocol = String(featured.protocol || 'TCP').trim().toUpperCase();
        const state = String(featured.state || 'ESTABLISHED').trim().toUpperCase().replace(/^ESTAB$/, 'ESTABLISHED');
        const local = readableEndpoint(featured.local_address, featured.local_port);
        const remote = readableEndpoint(featured.remote_address, featured.remote_port);
        footer = `<div class="monitor-network-feature"><div class="monitor-network-feature-title">Top Active:</div><div class="monitor-network-feature-owner">${escapeHtml(owner)} → ${escapeHtml(protocol)} · ${escapeHtml(state)}</div><div class="monitor-network-feature-row"><span>L:</span><strong>${escapeHtml(local)}</strong></div><div class="monitor-network-feature-row"><span>R:</span><strong>${escapeHtml(remote)}</strong></div></div>`;
      }
      body = `<div class="monitor-card-split"><div><strong>${listeners.length}</strong><span>Listening</span></div><div><strong>${active.length}</strong><span>Active</span></div></div>${footer}`;
    } else if (type === 'storage-health') {
      const total = number(snapshot.system?.storage_total_bytes || snapshot.disk_total_bytes);
      const free = number(snapshot.system?.storage_free_bytes || Math.max(0,total-number(snapshot.disk_used_bytes)));
      const freePct = total ? free * 100 / total : null;
      subtitle = 'Primary storage';
      body = `<div class="monitor-quick-big">${freePct === null ? '—' : `${freePct.toFixed(0)}% free`}</div><div class="monitor-quick-sub">${total ? `${formatBytes(free)} free of ${formatBytes(total)}` : 'Capacity unavailable'}</div><div class="monitor-card-note">I/O · R ${formatRate(snapshot.disk_read_bytes_per_sec)} · W ${formatRate(snapshot.disk_write_bytes_per_sec)} · SMART ${snapshot.capabilities?.storage_health ? 'available' : 'not exposed'}</div>${miniChartHTML(`monitorQuickChart-${slot}`,'is-storage')}`;
      chart = 'disk';
    } else if (type === 'gpu-vram') {
      subtitle = snapshot.system?.gpu_model || 'GPU telemetry';
      body = `<div class="monitor-quick-big">${formatPercent(snapshot.gpu_percent)}</div><div class="monitor-quick-sub">GPU load${nullableNumber(snapshot.gpu_temperature_c)!==null ? ` · ${formatTemp(snapshot.gpu_temperature_c)}` : ''}</div><div class="monitor-card-note">VRAM: unavailable unless exposed by the platform collector.</div>${miniChartHTML(`monitorQuickChart-${slot}`,'is-gpu')}`;
      chart = 'gpu';
    } else {
      subtitle = snapshot.system?.architecture || 'System';
      body = `<div class="monitor-quick-big">${formatUptime(snapshot.system?.uptime_seconds)}</div><div class="monitor-card-stat-row"><span>Processes</span><strong>${processes.length}</strong></div><div class="monitor-card-stat-row"><span>Services</span><strong>${Array.isArray(snapshot.services)?snapshot.services.length:'—'}</strong></div><div class="monitor-card-stat-row"><span>CPU now</span><strong>${formatPercent(snapshot.cpu_percent,1)}</strong></div>`;
    }
    host.dataset.card = type;
    host.innerHTML = `${quickCardHeader(generalCardLabel(type), subtitle, slot)}${body}`;
    if (chart) updateSpark(`monitorQuickChart-${slot}`, monitor.history[chart], 30);
  }

  function renderQuickCards(snapshot) {
    renderThermalCard(snapshot);
    const types = effectiveQuickCardTypes(snapshot);
    types.forEach((type,index) => renderGeneralQuickCard(document.getElementById(`monitorQuickCard${index+2}`), String(index+2), type, snapshot));
  }

  async function cycleQuickCard(slot, direction) {
    const snapshot = monitor.state?.snapshot || {};
    const settings = settingsFromState();
    if (slot === 'thermal') {
      const available = availableThermalViews(snapshot);
      const current = effectiveThermalView(snapshot);
      const index = Math.max(0, available.indexOf(current));
      const next = available[(index + direction + available.length) % available.length];
      await saveSettings(currentSettingsPayload({ thermal_card_view: next }), { toast:false });
      return;
    }
    const slotNumber = number(slot);
    if (slotNumber < 2 || slotNumber > 4) return;
    const available = availableGeneralQuickCards(snapshot);
    const effective = effectiveQuickCardTypes(snapshot);
    const current = effective[slotNumber - 2];
    const occupied = new Set(effective.filter((_,index)=>index !== slotNumber-2));
    const candidates = available.filter(type => !occupied.has(type));
    if (!candidates.length) return;
    const index = Math.max(0,candidates.indexOf(current));
    const next = candidates[(index + direction + candidates.length) % candidates.length];
    const overrides = {};
    overrides[`quick_card_${slotNumber}`] = next;
    await saveSettings(currentSettingsPayload(overrides), { toast:false });
  }

  function renderEvents() {
    const host = document.getElementById('monitorEventList');
    if (!host) return;
    const filter = document.getElementById('monitorEventFilter')?.value || 'all';
    let events = Array.isArray(monitor.state?.events) ? [...monitor.state.events] : [];
    if (filter === 'warning') events = events.filter(event => ['warning','error'].includes(String(event.level || '').toLowerCase()));
    else if (filter !== 'all') events = events.filter(event => String(event.kind || '').toLowerCase() === filter || String(event.level || '').toLowerCase() === filter);
    events.reverse();
    if (!events.length) { host.innerHTML = '<div class="monitor-empty">No Monitor events in this view.</div>'; return; }
    host.innerHTML = events.slice(0,120).map(event=>`<div class="monitor-event-row" data-level="${escapeHtml(String(event.level || 'info').toLowerCase())}"><span class="monitor-event-dot"></span><span class="monitor-event-time">${escapeHtml(localTime(event.time))}</span><span class="monitor-event-kind">${escapeHtml(event.kind || event.level || 'Info')}</span><span class="monitor-event-msg">${escapeHtml(event.message || '')}</span></div>`).join('');
  }

  function captureMonitorScrollPositions() {
    const selectors = [
      '#monitorEventList', '.monitor-process-table-wrap', '.monitor-generic-table-wrap',
      '.monitor-performance-scroll', '.monitor-system-detail-scroll', '.monitor-process-details',
      '.monitor-detail-scroll', '.monitor-cheat-scroll', '#monitorAIFeed'
    ];
    const positions = [];
    selectors.forEach(selector => {
      const elements = monitor.overlay ? [...monitor.overlay.querySelectorAll(selector)] : [];
      elements.forEach((element,index) => positions.push({ selector, index, top:element.scrollTop, left:element.scrollLeft }));
    });
    return positions;
  }

  function restoreMonitorScrollPositions(positions) {
    if (!Array.isArray(positions) || !monitor.overlay) return;
    positions.forEach(position => {
      const elements = [...monitor.overlay.querySelectorAll(position.selector)];
      const element = elements[position.index];
      if (!element) return;
      element.scrollTop = position.top;
      element.scrollLeft = position.left;
    });
  }

  function renderRuntime() {
    const settings = settingsFromState();
    const runtime = monitor.state?.runtime || {};
    const toggle = document.getElementById('monitorTopToggle');
    const label = document.getElementById('monitorTopToggleText');
    const dot = document.getElementById('monitorStateDot');
    toggle?.classList.toggle('is-on', !!settings.enabled);
    toggle?.setAttribute('aria-pressed',settings.enabled?'true':'false');
    if (label) label.textContent = `Monitor: ${settings.enabled ? 'ON' : 'OFF'}`;
    dot?.classList.toggle('is-on', !!settings.enabled && runtime.mode !== 'disconnected');
    const mode = document.getElementById('monitorRuntimeMode');
    if (mode) {
      const text = !settings.enabled ? 'Monitor off' : runtime.mode === 'active' ? `Active · ${settings.active_poll_seconds}s` : runtime.mode === 'passive' ? `Passive · ${settings.passive_poll_seconds}s` : 'Paused · browser disconnected';
      mode.textContent = text;
      mode.classList.toggle('monitor-status-offline', !settings.enabled || runtime.mode === 'disconnected');
    }
  }

  function renderState() {
    if (!monitor.state) return;
    const scrollPositions = captureMonitorScrollPositions();
    const snapshot = monitor.state.snapshot || {};
    if (snapshot.timestamp && snapshot.timestamp !== monitor.lastStateAt) {
      monitor.lastStateAt = snapshot.timestamp;
      updateHistory(snapshot);
    }
    renderRuntime();
    handleAIPresenceSummary(monitor.state?.ai);
    const modelSelect = document.getElementById('monitorAIModelSelect');
    if (modelSelect && modelSelect.value !== String(settingsFromState().ai_model_id || '')) modelSelect.value = String(settingsFromState().ai_model_id || '');
    renderAIControls();
    renderOverview(snapshot);
    renderSystemInfo(snapshot);
    renderQuickCards(snapshot);
    renderEvents();
    if (monitor.tab === 'processes') {
      renderProcessRows();
      renderProcessDetails();
      const update = document.getElementById('monitorProcessLastUpdate');
      if (update) update.textContent = snapshot.timestamp ? `Updated ${localTime(snapshot.timestamp)}` : 'Waiting for data';
    } else {
      const active = document.activeElement;
      if (!active || !active.matches?.('[data-monitor-tab-search]')) renderWorkspace();
    }
    restoreMonitorScrollPositions(scrollPositions);
  }

  async function loadState(options = {}) {
    if (typeof window.api !== 'function') return;
    try {
      monitor.state = await window.api('/api/monitor/state', { timeoutMs: 10000 });
      renderState();
      if (options.reschedule !== false) scheduleStatePoll();
    } catch (err) {
      const mode = document.getElementById('monitorRuntimeMode');
      if (mode) mode.textContent = 'Monitor unavailable';
      if (options.toast && typeof window.showToast === 'function') window.showToast(err?.message || 'Could not load AgentGO Monitor.', 'warning', 3600);
    }
  }

  function clearStateTimer() {
    if (monitor.stateTimer) clearTimeout(monitor.stateTimer);
    monitor.stateTimer = null;
  }

  function scheduleStatePoll() {
    clearStateTimer();
    if (!monitor.open || document.visibilityState === 'hidden') return;
    const settings = settingsFromState();
    if (!settings.enabled) return;
    const seconds = number(settings.active_poll_seconds,2);
    monitor.stateTimer = setTimeout(async () => {
      await loadState({ reschedule:false });
      scheduleStatePoll();
    }, Math.max(1000,seconds*1000));
  }

  async function openMonitor() {
    buildUI();
    if (typeof window.closeYetiMenu === 'function') window.closeYetiMenu();
    const workOverlay = document.getElementById('workModeOverlay');
    if (typeof window.hideWorkModeOverlay === 'function' && workOverlay?.classList.contains('open')) window.hideWorkModeOverlay();
    monitor.open = true;
    monitor.overlay.classList.add('open');
    monitor.overlay.setAttribute('aria-hidden','false');
    await sendPresence();
    await loadState({ toast:true });
    await loadAIModels();
    await loadAIState({ silent:true });
    scheduleStatePoll();
  }

  function closeMonitor() {
    if (!monitor.overlay) return;
    monitor.open = false;
    monitor.overlay.classList.remove('open');
    monitor.overlay.setAttribute('aria-hidden','true');
    closeSettings();
    closeSchedule();
    closeMemoryViewer();
    if (monitor.aiExpanded) toggleAIMaximize(false);
    clearStateTimer();
    sendPresence();
  }

  async function clearEvents() {
    try {
      await window.api('/api/monitor/events/clear', { method:'POST', body:'{}', timeoutMs:5000 });
      await loadState({ reschedule:false });
    } catch (err) {
      if (typeof window.showToast === 'function') window.showToast(err?.message || 'Could not clear Monitor events.', 'warning', 3200);
    }
  }

  function installYetiEntry() {
    document.addEventListener('click', event => {
      if (event.target.closest('#yetiStartMonitorBtn')) {
        event.preventDefault();
        openMonitor();
      }
    });
  }

  function init() {
    buildUI();
    installYetiEntry();
    clientID();
    sendPresence();
    monitor.heartbeatTimer = setInterval(sendPresence, 30000);
    document.addEventListener('visibilitychange', () => {
      sendPresence();
      if (document.visibilityState === 'hidden') clearStateTimer();
      else if (monitor.open && settingsFromState().enabled) { loadState({ reschedule:false }); scheduleStatePoll(); }
    });
    window.addEventListener('pagehide', sendDisconnect);
    document.addEventListener('keydown', event => {
      if (event.key !== 'Escape') return;
      if (monitor.memoryModal?.classList.contains('open')) closeMemoryViewer();
      else if (monitor.scheduleModal?.classList.contains('open')) closeSchedule();
      else if (monitor.settingsModal?.classList.contains('open')) closeSettings();
      else if (monitor.aiExpanded) toggleAIMaximize(false);
      else if (monitor.open) closeMonitor();
    });
    window.AgentGOMonitor = { open: openMonitor, close: closeMonitor, refresh: loadState };
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init, { once:true });
  else init();
})();
