(() => {
  'use strict';

  const mailUI = {
    overlay: null,
    state: null,
    open: false,
    selectedAccounts: new Set(),
    priority: 'all',
    designation: 'all',
    security: 'all',
    readState: 'all',
    search: '',
    senderFilter: '',
    dateFrom: '',
    dateTo: '',
    sortColumn: 'time',
    sortDirection: 'desc',
    openMessageID: '',
    bodyCache: new Map(),
    aiTimer: null,
    checking: false,
    rescanning: false,
    canceling: false,
    cancelRequested: false,
    stagedBatch: 50,
    stagedCacheDays: 7,
    stagedReplyTone: 'neutral',
    stagedReplyLength: 'standard',
    rescanMode: 'last_sync',
    lastOperation: null,
    stagedBlacklist: [],
    stagedRules: [],
    stagedDecorateSubjects: false,
    accountEditID: '',
    selectedMessageIDs: new Set(),
    analyzingIDs: new Set(),
    loading: false,
    loadError: '',
    visibleLimit: 100,
    pageSize: 100,
    replyMessageID: '',
    replyResult: null,
    modalReturnFocus: null,
    newMailCount: null,
    newMailCountLoading: false,
    newMailCountError: '',
    newMailCountTimer: null,
  };

  const designationLabels = [
    'Personal', 'User Question', 'Work / Project', 'Service / Billing', 'Security',
    'Newsletter', 'Sales / Promotion', 'Order / Shipping', 'Account / Notification', 'Other'
  ];
  const designationDisplayLabels = {
    'Personal': 'Personal',
    'User Question': 'Question',
    'Work / Project': 'Work',
    'Service / Billing': 'Service',
    'Security': 'Security',
    'Newsletter': 'Newsletter',
    'Sales / Promotion': 'Sales',
    'Order / Shipping': 'Orders',
    'Account / Notification': 'Account',
    'Other': 'Other',
    'Pending AI': 'Pending AI',
    'Blacklisted': 'Blacklisted',
    'Index Error': 'Index Error'
  };
  const priorityLabels = ['URGENT','IMPORTANT','NORMAL','LOW'];
  const securityLabels = ['NORMAL','CAUTION','POTENTIAL PHISHING','UNSCANNED'];
  const ruleActionLabels = {
    always_important: 'Always Important',
    usually_low: 'Usually Low Priority',
    known_newsletter: 'Known Newsletter',
    known_service: 'Known Service',
    never_auto_analyze: 'Never Auto-Analyze'
  };

  function escapeHtml(value) {
    return String(value ?? '')
      .replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('>','&gt;')
      .replaceAll('"','&quot;').replaceAll("'",'&#039;');
  }

  function accountByID(id) {
    return (mailUI.state?.settings?.accounts || []).find(item => String(item.id) === String(id)) || null;
  }

  function accountLabel(id) {
    const account = accountByID(id);
    return account ? (account.name || account.email || 'Mail Account') : 'Unknown Account';
  }

  function firstBadgeLetter(value) {
    const match = String(value || '').toUpperCase().match(/[A-Z0-9]/);
    return match ? match[0] : 'A';
  }

  function accountBadgeMap() {
    const accounts = mailUI.state?.settings?.accounts || [];
    const baseCounts = new Map();
    const out = new Map();
    for (const account of accounts) {
      const address = String(account.email || account.username || '');
      const at = address.lastIndexOf('@');
      const local = at > 0 ? address.slice(0, at) : address;
      const domain = at >= 0 ? address.slice(at + 1) : String(account.host || '');
      const name = String(account.name || local || 'Account');
      const base = `${firstBadgeLetter(name)}${firstBadgeLetter(domain)}`;
      const n = (baseCounts.get(base) || 0) + 1;
      baseCounts.set(base, n);
      const key = String(account.id);
      let hash = 0;
      for (const ch of `${key}|${address}`) hash = ((hash * 31) + ch.charCodeAt(0)) >>> 0;
      out.set(key, {label: `${base}${n > 1 ? n : ''}`, hue: hash % 360, title: account.email || account.username || account.name || 'Mail Account'});
    }
    return out;
  }

  function accountBadgeHTML(accountID, compact=false) {
    const meta = accountBadgeMap().get(String(accountID));
    if (!meta) return '';
    return `<span class="mail-account-badge${compact?' compact':''}" style="--mail-account-hue:${meta.hue}" title="${escapeHtml(meta.title)}" aria-label="Account ${escapeHtml(meta.title)}">${escapeHtml(meta.label)}</span>`;
  }

  function aiStatusMeta(message) {
    const indexError = String(message?.index_error || '');
    const aiError = String(message?.ai_error || '');
    const deepAIError = String(message?.deep_ai_error || '');
    const ruleActions = Array.isArray(message?.rule_actions) ? message.rule_actions : [];
    if (indexError) return {cls:'error', label:'Index error'};
    if (aiError || deepAIError) return {cls:'retry', label:'AI retry needed'};
    if (message?.deep_analyzed) return {cls:'deep', label:'Deep AI analysis complete'};
    if (message?.analyzed) return {cls:'processed', label:'AI processed'};
    if (message?.blacklisted) return {cls:'skipped', label:'Blacklisted · automatic AI skipped'};
    if (ruleActions.includes('never_auto_analyze')) return {cls:'skipped', label:'Rule skip · automatic AI disabled'};
    return {cls:'pending', label:'Pending AI'};
  }

  function securityStatusMeta(message) {
    const status = mailSecurityStatus(message);
    if (status === 'POTENTIAL PHISHING') return {cls:'phishing', label:'Security: Potential Phishing'};
    if (status === 'CAUTION') return {cls:'caution', label:'Security: Caution'};
    if (status === 'NORMAL') return {cls:'normal', label:'Security: Normal'};
    return {cls:'unscanned', label:'Security: Unscanned'};
  }

  function priorityStatusMeta(priority) {
    const value = String(priority || 'PENDING').toUpperCase();
    if (value === 'URGENT') return {cls:'urgent', label:'Priority: Urgent'};
    if (value === 'IMPORTANT') return {cls:'important', label:'Priority: Important'};
    if (value === 'NORMAL') return {cls:'normal', label:'Priority: Normal'};
    if (value === 'LOW') return {cls:'low', label:'Priority: Low'};
    return {cls:'pending', label:'Priority: Not yet classified'};
  }

  function statusClusterHTML(message, priority) {
    const security = securityStatusMeta(message);
    const ai = aiStatusMeta(message);
    const readLabel = message?.local_seen ? 'Read' : 'Unread';
    const readClass = message?.local_seen ? 'read' : 'unread';
    const priorityMeta = priorityStatusMeta(priority);
    const aiSymbol = ai.cls === 'processed' || ai.cls === 'deep' ? '✓' : (ai.cls === 'retry' || ai.cls === 'error' ? '!' : '');
    return `<span class="mail-status-cluster" aria-label="${escapeHtml(`${security.label}; ${ai.label}; ${readLabel}; ${priorityMeta.label}`)}">
      <span class="mail-security-dot ${security.cls}" title="${escapeHtml(security.label)}" aria-label="${escapeHtml(security.label)}"></span>
      <span class="mail-ai-state ${ai.cls}" title="${escapeHtml(ai.label)}" aria-label="${escapeHtml(ai.label)}">${escapeHtml(aiSymbol)}</span>
      <span class="mail-read-state ${readClass}" title="${escapeHtml(readLabel)}" aria-label="${escapeHtml(readLabel)}"></span>
      <span class="mail-priority-square ${priorityMeta.cls}" title="${escapeHtml(priorityMeta.label)}" aria-label="${escapeHtml(priorityMeta.label)}"></span>
    </span>`;
  }

  function formatDate(value) {
    if (!value) return '—';
    const d = new Date(value);
    if (Number.isNaN(d.getTime())) return String(value);
    const today = new Date();
    if (d.toDateString() === today.toDateString()) return d.toLocaleTimeString([], {hour:'2-digit',minute:'2-digit'});
    return d.toLocaleDateString([], {month:'short',day:'numeric'});
  }

  function syncForAccount(id) {
    const sync = mailUI.state?.sync || {};
    return sync[String(id)] || null;
  }

  function formatSyncLine(account) {
    const sync = syncForAccount(account?.id);
    if (!sync) return 'Not checked yet';
    if (sync.last_error) return `Sync error · ${String(sync.last_error)}`;
    if (!sync.last_successful_sync) return 'Not checked yet';
    const when = new Date(sync.last_successful_sync);
    const label = Number.isNaN(when.getTime()) ? 'Last sync recorded' : `Last sync ${when.toLocaleString([], {month:'short',day:'numeric',hour:'numeric',minute:'2-digit'})}`;
    const added = Number(sync.last_new_count || 0);
    return `${label} · ${added} new`;
  }

  function operationSummary(results, verb='Checked') {
    const list = Array.isArray(results) ? results : [];
    if (!list.length) return 'No enabled inboxes were available.';
    return list.map(item => {
      const name = item.name || accountLabel(item.account_id);
      if (item.error) return `${name}: failed`;
      const extras = [];
      const added = Number(item.added || 0);
      extras.push(added ? `${added} new` : 'up to date');
      if (Number(item.reconciled || 0)) extras.push(`${Number(item.reconciled)} reconciled`);
      if (item.uid_validity_reset) extras.push('UID reset recovered');
      if (Number(item.index_errors || 0)) extras.push(`${Number(item.index_errors)} parse warning${Number(item.index_errors)===1?'':'s'}`);
      return `${name}: ${extras.join(' · ')}`;
    }).join('   |   ');
  }

  function buildUI() {
    if (document.getElementById('agentGOMailOverlay')) {
      mailUI.overlay = document.getElementById('agentGOMailOverlay');
      return;
    }
    const releaseText = (document.querySelector('.build-tag')?.textContent || 'AgentGO r492').trim();
    const overlay = document.createElement('div');
    overlay.id = 'agentGOMailOverlay';
    overlay.className = 'agentgo-mail-overlay';
    overlay.setAttribute('aria-hidden','true');
    overlay.innerHTML = `
      <canvas class="yeti-stars-canvas" data-yeti-stars aria-hidden="true"></canvas>
      <div class="mail-shell">
        <aside class="mail-sidebar mail-glass" aria-label="AgentGO E-Mail accounts">
          <div class="mail-brand"><div class="mail-brand-title">AgentGO E-Mail</div><div class="mail-brand-sub">AI Inbox Intelligence</div></div>
          <button class="mail-side-btn is-active" id="mailUnifiedBtn" type="button"><span>✦</span><span>Unified Account</span><span class="mail-side-count" id="mailUnifiedCount">0</span></button>
          <div class="mail-side-section-title"><span>Connected Inboxes</span><button id="mailAddAccountBtn" type="button" aria-label="Add e-mail account">+</button></div>
          <div id="mailAccountList"></div>
          <div class="mail-side-spacer"></div>
          <button class="mail-side-btn" id="mailDesktopBtn" type="button"><span>▣</span><span>Desktop</span></button>
          <button class="mail-side-btn" id="mailRescanBtn" type="button" title="Search earlier INBOX mail and add messages not currently indexed by AgentGO. Existing messages are not duplicated and your mail server is not modified."><span>↶</span><span>Rescan Older Mail…</span></button>
          <button class="mail-side-btn" id="mailAccountsBtn" type="button"><span>⚙</span><span>Accounts & Settings</span></button>
          <div class="mail-side-footer"><strong>${escapeHtml(releaseText)}</strong><br>INBOX only · read-only upstream</div>
        </aside>
        <main class="mail-main">
          <header class="mail-topbar">
            <div class="mail-heading"><h2>AgentGO E-Mail</h2><p>Check · Rank · Reply Smarter</p></div>
            <div class="mail-top-actions">
              <label class="mail-pill mail-model-shell" for="mailAIModelSelect"><span>AI Model</span><select id="mailAIModelSelect" aria-label="E-Mail AI model"><option value="">None</option></select></label>
              <button class="mail-top-btn primary" id="mailAICheckBtn" type="button">AI Analyze (0)</button>
              <button class="mail-top-btn" id="mailCheckBtn" type="button">Download New</button>
              <button class="mail-top-btn mail-refresh-count-btn" id="mailNewCountRefreshBtn" type="button" aria-label="Refresh available new e-mail count" title="Refresh new-mail count">↻</button>
              <button class="mail-top-btn mail-close-btn" id="mailCloseBtn" type="button" aria-label="Close AgentGO E-Mail" title="Back to Desktop">×</button>
            </div>
          </header>
          <section class="mail-status-bar mail-glass" role="status" aria-live="polite" aria-atomic="true">
            <div><div class="mail-status-main" id="mailStatusMain">Ready. Download New to retrieve new INBOX messages.</div><div class="mail-status-sub" id="mailStatusSub">AI Analyze only processes messages already downloaded into AgentGO.</div></div>
            <div class="mail-status-progress" hidden><button class="mail-cancel-btn" id="mailCancelBtn" type="button" hidden>Cancel</button><div class="mail-progress-track"><div class="mail-progress-fill" id="mailProgressFill"></div></div><div class="mail-progress-label" id="mailProgressLabel">0 pending</div></div>
          </section>
          <section class="mail-filter-row mail-glass" id="mailFilterRow" aria-label="E-Mail filters">
            <label class="mail-filter-control"><span>Priority</span><select id="mailPriorityFilter" aria-label="Filter by priority"><option value="all">All Priority</option>${priorityLabels.map(p=>`<option value="${p}">${p}</option>`).join('')}</select></label>
            <label class="mail-filter-control"><span>Type</span><select id="mailDesignationFilter" aria-label="Filter by e-mail type"><option value="all">All Types</option>${designationLabels.map(d=>`<option value="${escapeHtml(d)}">${escapeHtml(d)}</option>`).join('')}</select></label>
            <label class="mail-filter-control"><span>Security</span><select id="mailSecurityFilter" aria-label="Filter by security status"><option value="all">All Security</option>${securityLabels.map(d=>`<option value="${escapeHtml(d)}">${escapeHtml(d)}</option>`).join('')}</select></label>
          </section>
          <section class="mail-query-bar mail-glass" aria-label="Search and sort E-Mail">
            <input id="mailSearchInput" type="search" autocomplete="off" placeholder="Search subject, sender, summary…" aria-label="Search e-mail">
            <input id="mailSenderFilter" type="search" autocomplete="off" placeholder="Sender or domain" aria-label="Filter sender or domain">
            <label><span>From</span><input id="mailDateFrom" type="date" aria-label="Filter from date"></label>
            <label><span>To</span><input id="mailDateTo" type="date" aria-label="Filter to date"></label>
            <select id="mailReadFilter" aria-label="Read filter"><option value="all">All Read State</option><option value="unread">Unread</option><option value="read">Read</option></select>
            <button class="mail-list-tool-btn" id="mailClearFiltersBtn" type="button">Clear Filters</button>
          </section>
          <section class="mail-list-panel mail-glass">
            <div class="mail-list-tools">
              <div class="mail-selection-copy"><strong id="mailSelectionCount">0 selected</strong><span>Individual Analyze uses the full message. Selected/date-range re-analysis uses the efficient batch classifier.</span></div>
              <div class="mail-selection-actions"><button class="mail-list-tool-btn" id="mailClearSelectionBtn" type="button" disabled>Clear</button><button class="mail-list-tool-btn danger" id="mailDeleteSelectedBtn" type="button" disabled>Delete Selected</button><button class="mail-list-tool-btn" id="mailReanalyzeSelectedBtn" type="button" disabled>AI Re-analyze</button><button class="mail-list-tool-btn" id="mailReanalyzeDateBtn" type="button">Date Range…</button></div>
            </div>
            <div class="mail-list-head">
              <input class="mail-select-all" id="mailSelectAll" type="checkbox" aria-label="Select all visible e-mails">
              <button class="mail-sort-head" type="button" data-mail-sort-column="type">Type<span class="mail-sort-arrow" aria-hidden="true"></span></button>
              <button class="mail-sort-head" type="button" data-mail-sort-column="sender">Sender<span class="mail-sort-arrow" aria-hidden="true"></span></button>
              <button class="mail-sort-head mail-subject-sort-head" type="button" data-mail-sort-column="subject"><span class="mail-status-head-copy">Status (Security · AI · Read · Priority) — </span>Subject<span class="mail-sort-arrow" aria-hidden="true"></span></button>
              <button class="mail-sort-head" type="button" data-mail-sort-column="time">Time<span class="mail-sort-arrow" aria-hidden="true"></span></button>
            </div>
            <div class="mail-list" id="mailMessageList"><div class="mail-empty">No downloaded e-mail yet.</div></div>
          </section>
        </main>
      </div>`;
    document.body.appendChild(overlay);
    mailUI.overlay = overlay;

    const accountModal = document.createElement('div');
    accountModal.id = 'mailAccountModal';
    accountModal.className = 'mail-modal';
    accountModal.setAttribute('aria-hidden','true');
    accountModal.innerHTML = `
      <div class="mail-modal-card mail-glass" role="dialog" aria-modal="true" aria-labelledby="mailAccountTitle">
        <div class="mail-modal-head"><div><h3 id="mailAccountTitle">Add E-Mail Account</h3><p>AgentGO only reads INBOX. It does not delete, move, flag, or send upstream mail.</p></div><button class="mail-modal-close" data-mail-account-close type="button">×</button></div>
        <div class="mail-modal-body">
          <div class="mail-field-row"><div class="mail-field"><label for="mailAccountName">Account name</label><input id="mailAccountName" autocomplete="off" placeholder="Private Mail"></div><div class="mail-field"><label for="mailAccountEmail">E-mail address</label><input id="mailAccountEmail" autocomplete="email" placeholder="you@example.com"></div></div>
          <div class="mail-field-row"><div class="mail-field"><label for="mailAccountHost">IMAP server</label><input id="mailAccountHost" autocomplete="off" placeholder="imap.example.com"></div><div class="mail-field"><label for="mailAccountPort">Port</label><input id="mailAccountPort" type="number" min="1" max="65535" value="993"></div></div>
          <div class="mail-field-row"><div class="mail-field"><label for="mailAccountUser">Username</label><input id="mailAccountUser" autocomplete="username" placeholder="you@example.com"></div><div class="mail-field"><label for="mailAccountSecurity">Security</label><select id="mailAccountSecurity"><option value="tls">TLS (recommended)</option><option value="starttls">STARTTLS</option><option value="plain">Plain</option></select></div></div>
          <div class="mail-field"><label for="mailAccountPassword">Password / app password</label><input id="mailAccountPassword" type="password" autocomplete="new-password" placeholder="Leave blank when editing to keep saved password"><div class="mail-status-sub">AgentGO currently uses normal IMAP credentials/app passwords. OAuth-capable providers can be added later.</div></div>
          <div class="mail-field"><label for="mailAccountBlacklist">Account subject blacklist · one phrase per line</label><textarea id="mailAccountBlacklist" placeholder="Account-specific junk phrase"></textarea><div class="mail-status-sub">Applies only to this inbox and combines with the global blacklist.</div></div>
          <div class="mail-modal-actions"><button class="mail-modal-btn danger" id="mailAccountDeleteBtn" type="button" hidden>Remove Account</button><button class="mail-modal-btn" id="mailAccountTestBtn" type="button">Test Connection</button><button class="mail-modal-btn" data-mail-account-close type="button">Cancel</button><button class="mail-modal-btn primary" id="mailAccountSaveBtn" type="button">Save Account</button></div>
        </div>
      </div>`;
    document.body.appendChild(accountModal);

    const settingsModal = document.createElement('div');
    settingsModal.id = 'mailSettingsModal';
    settingsModal.className = 'mail-modal';
    settingsModal.setAttribute('aria-hidden','true');
    settingsModal.innerHTML = `
      <div class="mail-modal-card mail-glass" role="dialog" aria-modal="true" aria-labelledby="mailSettingsTitle">
        <div class="mail-modal-head"><div><h3 id="mailSettingsTitle">AgentGO E-Mail Settings</h3><p>Control AI triage, reply assistance, temporary cache, rules, and account health.</p></div><button class="mail-modal-close" data-mail-settings-close type="button" aria-label="Close E-Mail settings">×</button></div>
        <div class="mail-modal-body">
          <div class="mail-field"><div class="mail-field-title">AI E-Mail Batch Size</div><div class="mail-status-sub">Maximum number of indexed messages sent in one AI triage request. Default: 50.</div><div class="mail-segments" id="mailBatchSegments">${[20,50,75,100,150].map(v=>`<button class="mail-segment${v===50?' is-active':''}" type="button" data-mail-batch="${v}">${v}</button>`).join('')}</div></div>
          <div class="mail-field"><div class="mail-field-title">Temporary Message Body Cache</div><div class="mail-status-sub">Full cleaned message bodies are cached only after you open a message or create a reply. Metadata stays in the lightweight index.</div><div class="mail-segments" id="mailCacheSegments">${[3,7,14,30].map(v=>`<button class="mail-segment${v===7?' is-active':''}" type="button" data-mail-cache-days="${v}">${v} days</button>`).join('')}</div></div>
          <div class="mail-field mail-reply-preferences"><div class="mail-field-title">AI Reply Defaults</div><div class="mail-status-sub">Defaults used by Create AI Reply. You can change them again in the draft window before regenerating.</div><div class="mail-reply-pref-grid"><div><span>Tone</span><div class="mail-segments" id="mailReplyToneSegments">${[['neutral','Neutral'],['friendly','Friendly'],['professional','Professional'],['warm','Warm']].map(([v,l])=>`<button class="mail-segment${v==='neutral'?' is-active':''}" type="button" data-mail-reply-tone="${v}">${l}</button>`).join('')}</div></div><div><span>Length</span><div class="mail-segments" id="mailReplyLengthSegments">${[['short','Short'],['standard','Standard'],['detailed','Detailed']].map(([v,l])=>`<button class="mail-segment${v==='standard'?' is-active':''}" type="button" data-mail-reply-length="${v}">${l}</button>`).join('')}</div></div></div></div>
          <div class="mail-field"><label for="mailBlacklistInput">Global subject blacklist · one phrase per line</label><textarea id="mailBlacklistInput" placeholder="***** SPAM\nCrypto Giveaway"></textarea><div class="mail-status-sub">Case-insensitive contains matching across all accounts. Account-specific blacklist phrases can be set while editing an inbox.</div></div>
          <div class="mail-field"><div class="mail-field-title">Sender & Domain Rules</div><div class="mail-status-sub">User-controlled durable rules override display priority/type or skip automatic bulk AI. Individual Analyze remains available.</div>
            <div class="mail-rule-builder"><select id="mailRuleScope"><option value="">All Accounts</option></select><select id="mailRuleMatch"><option value="sender">Sender Address</option><option value="domain">Sender Domain</option></select><input id="mailRulePattern" autocomplete="off" placeholder="person@example.com"><select id="mailRuleAction"><option value="always_important">Always Important</option><option value="usually_low">Usually Low Priority</option><option value="known_newsletter">Known Newsletter</option><option value="known_service">Known Service</option><option value="never_auto_analyze">Never Auto-Analyze</option></select><button class="mail-modal-btn" id="mailRuleAddBtn" type="button">Add Rule</button></div>
            <div id="mailRuleList" class="mail-rule-list"></div>
          </div>
          <label class="mail-setting-toggle"><input id="mailDecorateSubjects" type="checkbox"><span><strong>Prefix subjects with AI tags</strong><small>Show tags such as [IMPORTANT][NEWSLETTER] before the subject in the AgentGO list only.</small></span></label>
          <div class="mail-field mail-danger-zone"><div><div class="mail-field-title">Local Mail Data</div><div class="mail-status-sub">Clear AgentGO's downloaded mail index, AI results, local read state, and cached bodies while keeping connected accounts, saved passwords, and the current sync position. Nothing is deleted from your real mail servers.</div></div><button class="mail-modal-btn danger" id="mailClearLocalDataBtn" type="button">Clear Local Mail Data</button></div>
          <div class="mail-field"><div class="mail-field-title">Account Health</div><div class="mail-status-sub">Local status only. Tests authenticate to INBOX read-only and do not change upstream mail.</div><div id="mailAccountHealth" class="mail-account-health"></div></div>
          <div class="mail-field"><div class="mail-field-title">Connected Accounts</div><div id="mailSettingsAccountList" class="mail-account-list-modal"></div></div>
          <div class="mail-modal-actions"><button class="mail-modal-btn" data-mail-settings-close type="button">Cancel</button><button class="mail-modal-btn primary" id="mailSettingsSaveBtn" type="button">Save Settings</button></div>
        </div>
      </div>`;
    document.body.appendChild(settingsModal);

    const rescanModal = document.createElement('div');
    rescanModal.id = 'mailRescanModal';
    rescanModal.className = 'mail-modal';
    rescanModal.setAttribute('aria-hidden','true');
    rescanModal.innerHTML = `
      <div class="mail-modal-card mail-glass" role="dialog" aria-modal="true" aria-labelledby="mailRescanTitle">
        <div class="mail-modal-head"><div><h3 id="mailRescanTitle">Rescan Mail From…</h3><p>Look backward in every enabled INBOX without duplicating messages already indexed by AgentGO.</p></div><button class="mail-modal-close" data-mail-rescan-close type="button">×</button></div>
        <div class="mail-modal-body">
          <div class="mail-field"><div class="mail-field-title">Backfill range</div><div class="mail-segments" id="mailRescanSegments"><button class="mail-segment is-active" data-mail-rescan-mode="last_sync" type="button">Last Sync</button><button class="mail-segment" data-mail-rescan-mode="24h" type="button">24 Hours</button><button class="mail-segment" data-mail-rescan-mode="7d" type="button">7 Days</button><button class="mail-segment" data-mail-rescan-mode="30d" type="button">30 Days</button><button class="mail-segment" data-mail-rescan-mode="custom" type="button">Custom Date</button></div></div>
          <div class="mail-field" id="mailCustomDateField" hidden><label for="mailCustomDate">Custom start date</label><input id="mailCustomDate" type="date"><div class="mail-status-sub">IMAP date searches are day-based. AgentGO deduplicates anything it already has.</div></div>
          <div class="mail-status-sub">Only accounts with their blue dot turned on are rescanned. Normal Download New remains incremental afterward.</div>
          <div class="mail-modal-actions"><button class="mail-modal-btn" data-mail-rescan-close type="button">Cancel</button><button class="mail-modal-btn primary" id="mailRescanRunBtn" type="button">Rescan Enabled Inboxes</button></div>
        </div>
      </div>`;
    document.body.appendChild(rescanModal);

    const reanalyzeModal = document.createElement('div');
    reanalyzeModal.id = 'mailReanalyzeModal';
    reanalyzeModal.className = 'mail-modal';
    reanalyzeModal.setAttribute('aria-hidden','true');
    reanalyzeModal.innerHTML = `
      <div class="mail-modal-card mail-glass" role="dialog" aria-modal="true" aria-labelledby="mailReanalyzeTitle">
        <div class="mail-modal-head"><div><h3 id="mailReanalyzeTitle">Re-analyze E-Mail Date Range</h3><p>Re-run AI triage on already indexed mail without downloading it again.</p></div><button class="mail-modal-close" data-mail-reanalyze-close type="button">×</button></div>
        <div class="mail-modal-body">
          <div class="mail-field-row mail-date-range-row"><div class="mail-field"><label for="mailReanalyzeStart">Start date</label><input id="mailReanalyzeStart" type="date"></div><div class="mail-field"><label for="mailReanalyzeEnd">End date</label><input id="mailReanalyzeEnd" type="date"></div></div>
          <div class="mail-reanalyze-scope" id="mailReanalyzeScope">Unified Account</div>
          <div class="mail-status-sub" id="mailReanalyzeCount">Choose a date range.</div>
          <div class="mail-status-sub">Blacklisted and index-error messages are skipped. The current E-Mail AI model is used, and Emergency Stop cancels remaining AI calls.</div>
          <div class="mail-modal-actions"><button class="mail-modal-btn" data-mail-reanalyze-close type="button">Cancel</button><button class="mail-modal-btn primary" id="mailReanalyzeRunBtn" type="button">Re-analyze Date Range</button></div>
        </div>
      </div>`;
    document.body.appendChild(reanalyzeModal);

    const replyModal = document.createElement('div');
    replyModal.id = 'mailReplyModal';
    replyModal.className = 'mail-modal';
    replyModal.setAttribute('aria-hidden','true');
    replyModal.innerHTML = `
      <div class="mail-modal-card mail-glass mail-reply-card" role="dialog" aria-modal="true" aria-labelledby="mailReplyTitle">
        <div class="mail-modal-head"><div><h3 id="mailReplyTitle">AI Reply Draft</h3><p>AgentGO never sends. Review, edit, copy, or open the draft in your normal mail app.</p></div><button class="mail-modal-close" data-mail-reply-close type="button" aria-label="Close AI reply draft">×</button></div>
        <div class="mail-modal-body">
          <div class="mail-reply-controls"><label>Tone<select id="mailReplyTone"><option value="neutral">Neutral</option><option value="friendly">Friendly</option><option value="professional">Professional</option><option value="warm">Warm</option></select></label><label>Length<select id="mailReplyLength"><option value="short">Short</option><option value="standard">Standard</option><option value="detailed">Detailed</option></select></label><div class="mail-reply-meta" id="mailReplyMeta">Preparing draft…</div></div>
          <label class="mail-reply-label" for="mailReplyText">Draft reply</label><textarea class="mail-reply-text" id="mailReplyText" spellcheck="true" aria-label="AI reply draft">Generating draft…</textarea>
          <div class="mail-modal-actions mail-reply-actions"><button class="mail-modal-btn" data-mail-reply-close type="button">Close</button><button class="mail-modal-btn" id="mailRegenerateReplyBtn" type="button">Regenerate</button><button class="mail-modal-btn" id="mailOpenReplyBtn" type="button">Open in Mail App</button><button class="mail-modal-btn primary" id="mailCopyReplyBtn" type="button">Copy Reply</button></div>
        </div>
      </div>`;
    document.body.appendChild(replyModal);

    bindUI();
    if (window.AgentGOYetiStars?.refresh) window.AgentGOYetiStars.refresh();
  }

  function bindUI() {
    mailUI.overlay?.addEventListener('click', event => {
      const priority = event.target.closest('[data-mail-priority]');
      if (priority) {
        mailUI.priority = priority.dataset.mailPriority || 'all';
        document.querySelectorAll('[data-mail-priority]').forEach(btn=>btn.classList.toggle('is-active',btn===priority));
        resetVisibleLimit(); renderMessages();
        return;
      }
      const designation = event.target.closest('[data-mail-designation]');
      if (designation) {
        mailUI.designation = designation.dataset.mailDesignation || 'all';
        document.querySelectorAll('[data-mail-designation]').forEach(btn=>btn.classList.toggle('is-active',btn===designation));
        resetVisibleLimit(); renderMessages();
        return;
      }
      const security = event.target.closest('[data-mail-security]');
      if (security) {
        mailUI.security = security.dataset.mailSecurity || 'all';
        document.querySelectorAll('[data-mail-security]').forEach(btn=>btn.classList.toggle('is-active',btn===security));
        resetVisibleLimit(); renderMessages();
        return;
      }
      const sortHead = event.target.closest('[data-mail-sort-column]');
      if (sortHead) {
        setMailSort(sortHead.dataset.mailSortColumn || 'time');
        return;
      }
      const accountRow = event.target.closest('[data-mail-account-select]');
      if (accountRow && !event.target.closest('[data-mail-account-toggle],[data-mail-account-edit]')) {
        selectAccount(accountRow.dataset.mailAccountSelect || 'all');
        return;
      }
      const toggle = event.target.closest('[data-mail-account-toggle]');
      if (toggle) {
        event.preventDefault(); event.stopPropagation();
        toggleAccount(toggle.dataset.mailAccountToggle, toggle.getAttribute('aria-pressed') !== 'true');
        return;
      }
      const edit = event.target.closest('[data-mail-account-edit]');
      if (edit) { event.preventDefault(); event.stopPropagation(); openAccountModal(edit.dataset.mailAccountEdit); return; }
      const selectMessage = event.target.closest('[data-mail-select-message]');
      if (selectMessage) { event.preventDefault(); event.stopPropagation(); toggleMessageSelection(selectMessage.dataset.mailSelectMessage); return; }
      const analyze = event.target.closest('[data-mail-analyze]');
      if (analyze) { event.preventDefault(); event.stopPropagation(); analyzeMessage(analyze.dataset.mailAnalyze); return; }
      const reply = event.target.closest('[data-mail-create-reply]');
      if (reply) { event.preventDefault(); event.stopPropagation(); createReply(reply.dataset.mailCreateReply); return; }
      const seen = event.target.closest('[data-mail-local-seen]');
      if (seen) { event.preventDefault(); event.stopPropagation(); setLocalSeen(seen.dataset.mailLocalSeen, seen.dataset.mailSeenValue === 'true'); return; }
      const unsubscribe = event.target.closest('[data-mail-unsubscribe-url]');
      if (unsubscribe) { event.preventDefault(); event.stopPropagation(); openUnsubscribeLink(unsubscribe.dataset.mailUnsubscribeUrl); return; }
      const loadMore = event.target.closest('[data-mail-load-more]');
      if (loadMore) { event.preventDefault(); mailUI.visibleLimit += mailUI.pageSize; renderMessages(); return; }
      const retryLoad = event.target.closest('[data-mail-retry-load]');
      if (retryLoad) { event.preventDefault(); loadState({toast:true,models:true}); return; }
      const emptyAdd = event.target.closest('[data-mail-empty-add]');
      if (emptyAdd) { event.preventDefault(); openAccountModal(''); return; }
      const summary = event.target.closest('[data-mail-message-id]');
      if (summary) { toggleMessage(summary.dataset.mailMessageId); return; }
    });
    document.getElementById('mailUnifiedBtn')?.addEventListener('click',()=>selectAccount('all'));
    document.getElementById('mailAddAccountBtn')?.addEventListener('click',()=>openAccountModal(''));
    document.getElementById('mailAccountsBtn')?.addEventListener('click',openSettings);
    document.getElementById('mailDesktopBtn')?.addEventListener('click',closeMail);
    document.getElementById('mailCloseBtn')?.addEventListener('click',closeMail);
    document.getElementById('mailCheckBtn')?.addEventListener('click',checkEmail);
    document.getElementById('mailNewCountRefreshBtn')?.addEventListener('click',()=>refreshNewMailCount({toast:true}));
    document.getElementById('mailRescanBtn')?.addEventListener('click',openRescan);
    document.getElementById('mailAICheckBtn')?.addEventListener('click',aiCheckEmails);
    document.getElementById('mailCancelBtn')?.addEventListener('click',cancelActiveMailOperation);
    document.getElementById('mailPriorityFilter')?.addEventListener('change',event=>{mailUI.priority=String(event.target.value||'all');resetVisibleLimit();renderMessages();});
    document.getElementById('mailDesignationFilter')?.addEventListener('change',event=>{mailUI.designation=String(event.target.value||'all');resetVisibleLimit();renderMessages();});
    document.getElementById('mailSecurityFilter')?.addEventListener('change',event=>{mailUI.security=String(event.target.value||'all');resetVisibleLimit();renderMessages();});
    document.getElementById('mailSelectAll')?.addEventListener('change',toggleVisibleSelectionFromMaster);
    document.getElementById('mailClearSelectionBtn')?.addEventListener('click',clearMessageSelection);
    document.getElementById('mailDeleteSelectedBtn')?.addEventListener('click',deleteSelectedMessages);
    document.getElementById('mailReanalyzeSelectedBtn')?.addEventListener('click',reanalyzeSelectedMessages);
    document.getElementById('mailReanalyzeDateBtn')?.addEventListener('click',openReanalyzeModal);
    document.getElementById('mailAIModelSelect')?.addEventListener('change',event=>saveQuickSettings({ai_model_id:event.target.value}));
    document.getElementById('mailSearchInput')?.addEventListener('input',event=>{mailUI.search=String(event.target.value||'');resetVisibleLimit();renderMessages();});
    document.getElementById('mailSenderFilter')?.addEventListener('input',event=>{mailUI.senderFilter=String(event.target.value||'');resetVisibleLimit();renderMessages();});
    document.getElementById('mailDateFrom')?.addEventListener('change',event=>{mailUI.dateFrom=String(event.target.value||'');resetVisibleLimit();renderMessages();});
    document.getElementById('mailDateTo')?.addEventListener('change',event=>{mailUI.dateTo=String(event.target.value||'');resetVisibleLimit();renderMessages();});
    document.getElementById('mailReadFilter')?.addEventListener('change',event=>{mailUI.readState=String(event.target.value||'all');resetVisibleLimit();renderMessages();});
    document.getElementById('mailClearFiltersBtn')?.addEventListener('click',clearMailFilters);
    document.getElementById('mailAccountSaveBtn')?.addEventListener('click',saveAccount);
    document.getElementById('mailAccountTestBtn')?.addEventListener('click',testAccount);
    document.getElementById('mailAccountDeleteBtn')?.addEventListener('click',deleteAccount);
    document.querySelectorAll('[data-mail-account-close]').forEach(btn=>btn.addEventListener('click',closeAccountModal));
    document.querySelectorAll('[data-mail-settings-close]').forEach(btn=>btn.addEventListener('click',closeSettings));
    document.getElementById('mailBatchSegments')?.addEventListener('click',event=>{
      const btn = event.target.closest('[data-mail-batch]'); if (!btn) return;
      mailUI.stagedBatch = Number(btn.dataset.mailBatch || 50);
      document.querySelectorAll('[data-mail-batch]').forEach(item=>item.classList.toggle('is-active',item===btn));
    });
    document.getElementById('mailCacheSegments')?.addEventListener('click',event=>{
      const btn = event.target.closest('[data-mail-cache-days]'); if (!btn) return;
      mailUI.stagedCacheDays = Number(btn.dataset.mailCacheDays || 7);
      document.querySelectorAll('[data-mail-cache-days]').forEach(item=>item.classList.toggle('is-active',item===btn));
    });
    document.getElementById('mailReplyToneSegments')?.addEventListener('click',event=>{
      const btn=event.target.closest('[data-mail-reply-tone]');if(!btn)return;mailUI.stagedReplyTone=String(btn.dataset.mailReplyTone||'neutral');document.querySelectorAll('[data-mail-reply-tone]').forEach(item=>item.classList.toggle('is-active',item===btn));
    });
    document.getElementById('mailReplyLengthSegments')?.addEventListener('click',event=>{
      const btn=event.target.closest('[data-mail-reply-length]');if(!btn)return;mailUI.stagedReplyLength=String(btn.dataset.mailReplyLength||'standard');document.querySelectorAll('[data-mail-reply-length]').forEach(item=>item.classList.toggle('is-active',item===btn));
    });
    document.getElementById('mailRescanSegments')?.addEventListener('click',event=>{
      const btn = event.target.closest('[data-mail-rescan-mode]'); if (!btn) return;
      mailUI.rescanMode = btn.dataset.mailRescanMode || 'last_sync';
      document.querySelectorAll('[data-mail-rescan-mode]').forEach(item=>item.classList.toggle('is-active',item===btn));
      const field=document.getElementById('mailCustomDateField'); if(field) field.hidden=mailUI.rescanMode!=='custom';
    });
    document.getElementById('mailRescanRunBtn')?.addEventListener('click',runRescan);
    document.querySelectorAll('[data-mail-rescan-close]').forEach(btn=>btn.addEventListener('click',closeRescan));
    document.querySelectorAll('[data-mail-reanalyze-close]').forEach(btn=>btn.addEventListener('click',closeReanalyzeModal));
    document.getElementById('mailReanalyzeStart')?.addEventListener('change',updateReanalyzeCount);
    document.getElementById('mailReanalyzeEnd')?.addEventListener('change',updateReanalyzeCount);
    document.getElementById('mailReanalyzeRunBtn')?.addEventListener('click',reanalyzeDateRange);
    document.getElementById('mailRuleAddBtn')?.addEventListener('click',addStagedRule);
    document.getElementById('mailRuleList')?.addEventListener('click',event=>{const btn=event.target.closest('[data-mail-rule-remove]');if(!btn)return;mailUI.stagedRules=mailUI.stagedRules.filter(rule=>String(rule.id)!==String(btn.dataset.mailRuleRemove));renderStagedRules();});
    document.getElementById('mailSettingsSaveBtn')?.addEventListener('click',saveSettings);
    document.getElementById('mailClearLocalDataBtn')?.addEventListener('click',clearLocalMailData);
    document.getElementById('mailAccountHealth')?.addEventListener('click',event=>{const btn=event.target.closest('[data-mail-health-test]');if(btn)testSavedAccount(btn.dataset.mailHealthTest,btn);});
    document.querySelectorAll('[data-mail-reply-close]').forEach(btn=>btn.addEventListener('click',closeReplyModal));
    document.getElementById('mailCopyReplyBtn')?.addEventListener('click',copyReply);
    document.getElementById('mailRegenerateReplyBtn')?.addEventListener('click',regenerateReply);
    document.getElementById('mailOpenReplyBtn')?.addEventListener('click',openReplyInMailApp);
    document.getElementById('mailReplyTone')?.addEventListener('change',event=>saveQuickSettings({reply_tone:event.target.value}));
    document.getElementById('mailReplyLength')?.addEventListener('change',event=>saveQuickSettings({reply_length:event.target.value}));
  }

  async function loadModels() {
    const select = document.getElementById('mailAIModelSelect');
    if (!select || typeof window.api !== 'function') return;
    try {
      const models = await window.api('/api/models', {timeoutMs:10000});
      const eligible = (Array.isArray(models)?models:[]).filter(model=>!model.isVideoGeneration&&!model.isMeshGeneration);
      const selected = String(mailUI.state?.settings?.ai_model_id || '');
      select.innerHTML = '<option value="">None · AI checking disabled</option>' + eligible.map(model=>`<option value="${escapeHtml(model.id)}">${escapeHtml(model.label || `Model ${model.id}`)}</option>`).join('');
      select.value = eligible.some(model=>String(model.id)===selected) ? selected : '';
    } catch (_) {
      select.innerHTML = '<option value="">No model list available</option>';
    }
  }

  function mailAIRenderSignature(messages) {
    const list=Array.isArray(messages)?messages:[];
    return JSON.stringify(list.map(message=>[
      message.id,!!message.analyzed,!!message.deep_analyzed,message.priority||'',message.designation||'',message.summary||'',
      !!message.response_needed,message.why_it_matters||'',message.suggested_action||'',message.security_interpretation||'',
      message.confidence||'',message.analysis_model_id||'',message.analyzed_at||'',message.ai_error||'',message.deep_ai_error||''
    ]));
  }

  async function loadState(options={}) {
    if (typeof window.api !== 'function') return;
    const polling=!!options.poll;
    const previousSignature=polling?mailAIRenderSignature(mailUI.state?.messages):'';
    if(!polling){
      mailUI.loading=true; mailUI.loadError='';
      if(mailUI.open){renderMessages();setStatus('Loading AgentGO E-Mail…','Reading the local mail index and account state.');}
    }
    try {
      const nextState = await window.api('/api/email/state', {timeoutMs:12000});
      mailUI.state = nextState;
      mailUI.loading=false; mailUI.loadError='';
      const knownIDs = new Set((mailUI.state?.messages || []).map(item=>String(item.id)));
      for (const id of [...mailUI.selectedMessageIDs]) if (!knownIDs.has(String(id))) mailUI.selectedMessageIDs.delete(id);
      if(polling){
        // AI progress polling should not blank/rebuild the entire inbox every
        // second. Refresh the status continuously and redraw rows only when a
        // completed batch actually changed message classifications.
        renderStatus();
        const changed=previousSignature!==mailAIRenderSignature(mailUI.state?.messages);
        if(changed){renderFilterCounts();renderMessages();}
      }else{
        renderAll();
      }
      if (options.models) await loadModels();
      manageAIPoll();
    } catch (err) {
      if(!polling){
        mailUI.loading=false; mailUI.loadError=String(err?.message || 'Could not load AgentGO E-Mail.');
        renderMessages();
        setStatus(mailUI.loadError, 'E-Mail state unavailable. Use Retry to try again.');
        if (options.toast && typeof window.showToast === 'function') window.showToast(mailUI.loadError,'warning',3600);
      }
    }
  }

  function renderAll() {
    renderAccounts();
    renderFilterCounts();
    renderMessages();
    renderStatus();
    renderAccountHealth();
    const select = document.getElementById('mailAIModelSelect');
    if (select && select.options.length > 1) select.value = String(mailUI.state?.settings?.ai_model_id || '');
  }

  function selectedAccountIDs() {
    return [...mailUI.selectedAccounts].map(String).filter(Boolean);
  }

  function accountScopedMessages() {
    const messages = Array.isArray(mailUI.state?.messages) ? mailUI.state.messages : [];
    const selected = mailUI.selectedAccounts;
    if (!selected.size) return messages;
    return messages.filter(message=>selected.has(String(message.account_id)));
  }

  function senderDomainText(message) {
    const security = message?.security || {};
    return [message?.from, message?.reply_to, security.from_domain, security.from_registered_domain, security.reply_to_domain]
      .filter(Boolean).join(' ').toLowerCase();
  }

  function messageTimestamp(message) {
    const value = message?.date || message?.indexed_at || '';
    const when = new Date(value);
    return Number.isNaN(when.getTime()) ? 0 : when.getTime();
  }


  function resetVisibleLimit(){mailUI.visibleLimit=mailUI.pageSize;}

  function renderAccountHealth(){
    const host=document.getElementById('mailAccountHealth');if(!host)return;
    const accounts=mailUI.state?.settings?.accounts||[];
    const messages=Array.isArray(mailUI.state?.messages)?mailUI.state.messages:[];
    if(!accounts.length){host.innerHTML='<div class="mail-empty">No connected accounts.</div>';return;}
    host.innerHTML=accounts.map(account=>{
      const sync=syncForAccount(account.id);
      const accountMessages=messages.filter(message=>String(message.account_id)===String(account.id));
      const unread=accountMessages.filter(message=>!message.local_seen).length;
      let state='Not checked',cls='unknown',detail='Run Download New or Test Connection.';
      if(!account.enabled){state='Disabled';cls='disabled';detail='Blue-dot toggle is off; Download New skips this inbox.';}
      else if(sync?.last_error){state='Issue';cls='issue';detail=String(sync.last_error);}
      else if(sync?.last_successful_sync){state='Healthy';cls='healthy';detail=formatSyncLine(account);}
      return `<div class="mail-health-row"><span class="mail-health-dot ${cls}" aria-hidden="true"></span><div><strong>${escapeHtml(account.name||account.email||'Mail Account')}</strong><span>${escapeHtml(state)} · ${unread} unread / ${accountMessages.length} indexed</span><small>${escapeHtml(detail)}</small></div><button class="mail-modal-btn" data-mail-health-test="${escapeHtml(account.id)}" type="button" ${account.enabled?'':'disabled'}>Test</button></div>`;
    }).join('');
  }

  async function testSavedAccount(id,button){
    id=String(id||'');if(!id)return;
    const previous=button?.textContent;if(button){button.disabled=true;button.textContent='Testing…';}
    try{const data=await window.api('/api/email/accounts/test',{method:'POST',body:JSON.stringify({id}),timeoutMs:30000});window.showToast?.(data?.message||'Connection succeeded.');}
    catch(err){window.showToast?.(err?.message||'Connection test failed.','warning',4500);}
    finally{if(button){button.disabled=false;button.textContent=previous||'Test';}}
  }

  function renderFilterCounts() {
    const messages = accountScopedMessages();
    const priorityCounts = new Map(priorityLabels.map(label=>[label,0]));
    const designationCounts = new Map(designationLabels.map(label=>[label,0]));
    const securityCounts = new Map(securityLabels.map(label=>[label,0]));
    for (const message of messages) {
      const priority = String(message.priority || (message.blacklisted?'LOW':'PENDING')).toUpperCase();
      const designation = String(message.designation || (message.blacklisted?'Other':''));
      const security = mailSecurityStatus(message);
      if (priorityCounts.has(priority)) priorityCounts.set(priority,priorityCounts.get(priority)+1);
      if (designationCounts.has(designation)) designationCounts.set(designation,designationCounts.get(designation)+1);
      if (securityCounts.has(security)) securityCounts.set(security,securityCounts.get(security)+1);
    }
    const updateSelect=(id,allLabel,labels,counts,current)=>{
      const select=document.getElementById(id); if(!select)return;
      select.innerHTML=`<option value="all">${escapeHtml(allLabel)} · ${messages.length}</option>`+labels.map(label=>`<option value="${escapeHtml(label)}">${escapeHtml(label)} · ${counts.get(label)||0}</option>`).join('');
      select.value=current;
    };
    updateSelect('mailPriorityFilter','All Priority',priorityLabels,priorityCounts,mailUI.priority);
    updateSelect('mailDesignationFilter','All Types',designationLabels,designationCounts,mailUI.designation);
    updateSelect('mailSecurityFilter','All Security',securityLabels,securityCounts,mailUI.security);
  }

  function renderAccounts() {
    const accounts = mailUI.state?.settings?.accounts || [];
    const validIDs = new Set(accounts.map(account=>String(account.id)));
    for (const id of [...mailUI.selectedAccounts]) if (!validIDs.has(String(id))) mailUI.selectedAccounts.delete(String(id));
    const allMessages = Array.isArray(mailUI.state?.messages) ? mailUI.state.messages : [];
    document.getElementById('mailUnifiedBtn')?.classList.toggle('is-active',mailUI.selectedAccounts.size===0);
    const unifiedCount=document.getElementById('mailUnifiedCount');
    if(unifiedCount){const unread=allMessages.filter(message=>!message.local_seen).length;unifiedCount.textContent=unread?`${unread}/${allMessages.length}`:String(allMessages.length);unifiedCount.title=`${unread} unread · ${allMessages.length} total`;}
    const badges=accountBadgeMap();
    const host = document.getElementById('mailAccountList');
    if (host) host.innerHTML = accounts.length ? accounts.map(account=>{
      const sync=syncForAccount(account.id); const syncLine=formatSyncLine(account); const syncError=!!sync?.last_error;
      const accountMessages=allMessages.filter(message=>String(message.account_id)===String(account.id));
      const unread=accountMessages.filter(message=>!message.local_seen).length;
      const countText=unread?`${unread}/${accountMessages.length}`:String(accountMessages.length);
      const badge=badges.get(String(account.id));
      return `<div class="mail-account-row${mailUI.selectedAccounts.has(String(account.id))?' is-active':''}" data-mail-account-select="${escapeHtml(account.id)}" aria-pressed="${mailUI.selectedAccounts.has(String(account.id))?'true':'false'}">
        <button class="mail-account-dot${account.enabled?' is-on':''}" data-mail-account-toggle="${escapeHtml(account.id)}" type="button" aria-pressed="${account.enabled?'true':'false'}" title="${account.enabled?'Included in Download New and Rescan':'Excluded from Download New and Rescan'}"></button>
        ${badge?`<span class="mail-account-badge" style="--mail-account-hue:${badge.hue}" title="${escapeHtml(badge.title)}">${escapeHtml(badge.label)}</span>`:''}
        <div class="mail-account-copy"><div class="mail-account-name">${escapeHtml(account.name || account.email || 'Mail Account')}</div><div class="mail-account-email">${escapeHtml(account.email || account.username || '')}</div><div class="mail-account-sync${syncError?' is-error':''}" title="${escapeHtml(syncLine)}">${escapeHtml(syncLine)}</div></div>
        <div class="mail-account-actions"><span class="mail-account-count" title="${unread} unread · ${accountMessages.length} total">${escapeHtml(countText)}</span><button class="mail-account-mini" data-mail-account-edit="${escapeHtml(account.id)}" type="button" title="Edit account">✎</button></div>
      </div>`;
    }).join('') : '<div class="mail-empty" style="padding:16px 5px">No accounts yet.</div>';
    const settingsHost = document.getElementById('mailSettingsAccountList');
    if (settingsHost) settingsHost.innerHTML = accounts.length ? accounts.map(account=>`
      <div class="mail-account-list-entry"><span class="mail-account-dot${account.enabled?' is-on':''}"></span>${accountBadgeHTML(account.id,true)}<div><strong>${escapeHtml(account.name)}</strong><div class="mail-status-sub">${escapeHtml(account.email || account.username)} · ${escapeHtml(account.host)}:${Number(account.port||0)}</div><div class="mail-status-sub${syncForAccount(account.id)?.last_error?' mail-sync-error':''}">${escapeHtml(formatSyncLine(account))}</div></div><button class="mail-modal-btn" data-mail-settings-edit="${escapeHtml(account.id)}" type="button">Edit</button></div>`).join('') : '<div class="mail-empty">No connected accounts.</div>';
    settingsHost?.querySelectorAll('[data-mail-settings-edit]').forEach(btn=>btn.addEventListener('click',()=>{closeSettings();openAccountModal(btn.dataset.mailSettingsEdit);}));
  }

  function setMailSort(column) {
    const next = ['type','sender','subject','time'].includes(String(column)) ? String(column) : 'time';
    if (mailUI.sortColumn === next) {
      mailUI.sortDirection = mailUI.sortDirection === 'asc' ? 'desc' : 'asc';
    } else {
      mailUI.sortColumn = next;
      mailUI.sortDirection = next === 'time' ? 'desc' : 'asc';
    }
    resetVisibleLimit();
    renderMessages();
  }

  function renderSortHeaders() {
    document.querySelectorAll('.mail-sort-head[data-mail-sort-column]').forEach(button => {
      const active = button.dataset.mailSortColumn === mailUI.sortColumn;
      button.classList.toggle('is-active', active);
      button.setAttribute('aria-pressed', active ? 'true' : 'false');
      const directionLabel = mailUI.sortDirection === 'asc' ? 'ascending' : 'descending';
      button.title = active ? `Sorted ${directionLabel}. Click to reverse.` : `Sort by ${button.dataset.mailSortColumn}.`;
      const arrow = button.querySelector('.mail-sort-arrow');
      if (arrow) arrow.textContent = active ? (mailUI.sortDirection === 'asc' ? '▲' : '▼') : '';
    });
  }

  function filteredMessages() {
    let messages = Array.isArray(mailUI.state?.messages) ? [...mailUI.state.messages] : [];
    if (mailUI.selectedAccounts.size) messages = messages.filter(m=>mailUI.selectedAccounts.has(String(m.account_id)));
    if (mailUI.priority !== 'all') messages = messages.filter(m=>String(m.priority||'').toUpperCase()===mailUI.priority);
    if (mailUI.designation !== 'all') messages = messages.filter(m=>String(m.designation||'')===mailUI.designation);
    if (mailUI.security !== 'all') messages = messages.filter(m=>mailSecurityStatus(m)===mailUI.security);
    if (mailUI.readState === 'unread') messages = messages.filter(m=>!m.local_seen);
    if (mailUI.readState === 'read') messages = messages.filter(m=>!!m.local_seen);
    const search=String(mailUI.search||'').trim().toLowerCase();
    if(search) messages=messages.filter(message=>[
      message.subject,message.from,message.reply_to,message.summary,message.excerpt,message.priority,message.designation,accountLabel(message.account_id)
    ].filter(Boolean).join(' ').toLowerCase().includes(search));
    const sender=String(mailUI.senderFilter||'').trim().toLowerCase();
    if(sender) messages=messages.filter(message=>senderDomainText(message).includes(sender));
    if(mailUI.dateFrom){const start=new Date(`${mailUI.dateFrom}T00:00:00`);if(!Number.isNaN(start.getTime()))messages=messages.filter(message=>messageTimestamp(message)>=start.getTime());}
    if(mailUI.dateTo){const end=new Date(`${mailUI.dateTo}T23:59:59.999`);if(!Number.isNaN(end.getTime()))messages=messages.filter(message=>messageTimestamp(message)<=end.getTime());}
    const direction = mailUI.sortDirection === 'asc' ? 1 : -1;
    messages.sort((a,b)=>{
      let diff = 0;
      if(mailUI.sortColumn==='type') {
        diff = String(mailDesignationBadgeLabel(a.designation || (a.blacklisted?'Blacklisted':'Pending AI'))).localeCompare(
          String(mailDesignationBadgeLabel(b.designation || (b.blacklisted?'Blacklisted':'Pending AI'))),
          undefined,{sensitivity:'base'}
        );
      } else if(mailUI.sortColumn==='sender') {
        diff = String(a.from||'').localeCompare(String(b.from||''),undefined,{sensitivity:'base'});
      } else if(mailUI.sortColumn==='subject') {
        diff = String(a.subject||'').localeCompare(String(b.subject||''),undefined,{sensitivity:'base'});
      } else {
        diff = messageTimestamp(a)-messageTimestamp(b);
      }
      if(diff===0 && mailUI.sortColumn!=='time') diff = messageTimestamp(a)-messageTimestamp(b);
      return diff * direction;
    });
    return messages;
  }

  function clearMailFilters(){
    mailUI.priority='all';mailUI.designation='all';mailUI.security='all';mailUI.readState='all';mailUI.search='';mailUI.senderFilter='';mailUI.dateFrom='';mailUI.dateTo='';mailUI.sortColumn='time';mailUI.sortDirection='desc';
    const filterValues={mailPriorityFilter:'all',mailDesignationFilter:'all',mailSecurityFilter:'all',mailSearchInput:'',mailSenderFilter:'',mailDateFrom:'',mailDateTo:''};for(const [id,value] of Object.entries(filterValues)){const el=document.getElementById(id);if(el)el.value=value;}
    const read=document.getElementById('mailReadFilter');if(read)read.value='all';
    resetVisibleLimit(); renderMessages();
  }

  function mailSecurityStatus(message) {
    const security = message?.security || {};
    return Number(security.version || 0) > 0 ? String(security.status || 'NORMAL').toUpperCase() : 'UNSCANNED';
  }

  function mailSecurityClass(status) {
    return String(status || 'UNSCANNED').toLowerCase().replaceAll(' ','-');
  }

  function mailSecurityBadgeLabel(status) {
    const value=String(status||'UNSCANNED').toUpperCase();
    return value==='POTENTIAL PHISHING' ? 'PHISHING' : value;
  }

  function mailDesignationBadgeLabel(value) {
    const text=String(value||'Pending AI');
    return designationDisplayLabels[text] || text;
  }

  function securityField(value) {
    const text = String(value || '').trim();
    return text ? text.toUpperCase() : '—';
  }

  function renderSecurityDetails(message) {
    const security = message?.security || {};
    const status = mailSecurityStatus(message);
    if (status === 'UNSCANNED') {
      return `<div class="mail-security-box"><div class="mail-security-head"><span>Security Intelligence</span><span class="mail-badge risk-unscanned">UNSCANNED</span></div><div class="mail-security-note">This older local record has not received the Phase 3 deterministic scan yet. Opening the message or performing a Rescan can backfill it without changing the upstream mailbox.</div></div>`;
    }
    const findings = Array.isArray(security.findings) ? security.findings : [];
    const links = Array.isArray(security.links) ? security.links : [];
    const domains = [
      security.from_registered_domain ? `From: ${security.from_registered_domain}` : '',
      security.reply_to_domain ? `Reply-To: ${security.reply_to_domain}` : '',
      security.return_path_domain ? `Return-Path: ${security.return_path_domain}` : ''
    ].filter(Boolean);
    const findingHTML = findings.length ? `<div class="mail-security-findings">${findings.slice(0,10).map(item=>`<div class="mail-security-finding level-${escapeHtml(item.level||'info')}"><span>${escapeHtml(item.level||'info')}</span>${escapeHtml(item.summary||'')}</div>`).join('')}</div>` : '<div class="mail-security-note">No strong local indicators were detected. This is not a guarantee that the message is safe.</div>';
    const linkHTML = links.some(link=>Array.isArray(link.flags)&&link.flags.length) ? `<div class="mail-security-links"><strong>Link indicators</strong>${links.filter(link=>Array.isArray(link.flags)&&link.flags.length).slice(0,6).map(link=>`<div><span>${escapeHtml(link.registered_domain||link.host||'link')}</span> · ${escapeHtml(link.flags.join(', '))}</div>`).join('')}</div>` : '';
    const protectionBits = [];
    if (Number(security.remote_images_blocked||0)) protectionBits.push(`${Number(security.remote_images_blocked)} remote image reference${Number(security.remote_images_blocked)===1?'':'s'} blocked`);
    if (Number(security.scripts_removed||0)) protectionBits.push(`${Number(security.scripts_removed)} script reference${Number(security.scripts_removed)===1?'':'s'} removed`);
    return `<div class="mail-security-box"><div class="mail-security-head"><span>Security Intelligence</span><span class="mail-badge risk-${mailSecurityClass(status)}">${escapeHtml(status)}</span></div>
      <div class="mail-security-auth"><span>SPF <strong>${escapeHtml(securityField(security.spf))}</strong></span><span>DKIM <strong>${escapeHtml(securityField(security.dkim))}</strong></span><span>DMARC <strong>${escapeHtml(securityField(security.dmarc))}</strong></span></div>
      ${domains.length?`<div class="mail-security-domains">${domains.map(item=>`<span>${escapeHtml(item)}</span>`).join('')}</div>`:''}
      ${findingHTML}${linkHTML}${protectionBits.length?`<div class="mail-security-protection">${escapeHtml(protectionBits.join(' · '))}</div>`:''}
      <div class="mail-security-foot">Local evidence only. AgentGO does not visit links or load remote message content during classification.</div></div>`;
  }

  function mailHasUnsubscribe(message){
    const info=message?.unsubscribe||{};
    const header=Array.isArray(info.header_links)?info.header_links:[];
    const body=Array.isArray(info.body_links)?info.body_links:[];
    return header.length>0||body.length>0||!!info.one_click;
  }

  function renderUnsubscribeIndicator(message){
    if(!mailHasUnsubscribe(message))return '';
    return '<span class="mail-unsubscribe-indicator" title="Open this message to use the unsubscribe option.">unsub</span>';
  }

  function renderUnsubscribe(message){
    const info=message?.unsubscribe||{};
    const header=Array.isArray(info.header_links)?info.header_links:[];
    const body=Array.isArray(info.body_links)?info.body_links:[];
    const links=[...header.map(url=>({url,source:'List-Unsubscribe'})),...body.map(url=>({url,source:'Message link'}))].slice(0,5);
    if(!mailHasUnsubscribe(message))return '';
    const buttons=links.map(item=>{
      const isMailto=String(item.url||'').toLowerCase().startsWith('mailto:');
      return `<button class="mail-unsubscribe-btn" type="button" data-mail-unsubscribe-url="${escapeHtml(item.url)}">${isMailto?'Compose unsubscribe e-mail':'Open unsubscribe'}<span>${escapeHtml(item.source)}</span></button>`;
    }).join('');
    return `<div class="mail-unsubscribe-box"><div><strong>Unsubscribe available</strong><span>${info.one_click?'Sender advertises one-click unsubscribe. AgentGO will not submit it automatically.':'AgentGO found an unsubscribe destination. It is opened only when you choose it.'}</span></div>${buttons?`<div class="mail-unsubscribe-actions">${buttons}</div>`:''}<small>Opening an unsubscribe destination leaves AgentGO. Review the destination before continuing.</small></div>`;
  }

  function openUnsubscribeLink(raw){
    const value=String(raw||'').trim();
    if(!/^(https?:|mailto:)/i.test(value)){window.showToast?.('AgentGO blocked an unsupported unsubscribe destination.','warning',3500);return;}
    if(/^mailto:/i.test(value)){window.location.href=value;return;}
    const opened=window.open(value,'_blank','noopener,noreferrer');
    if(!opened)window.showToast?.('Your browser blocked the unsubscribe window.','warning',3200);
  }

  function eligibleForBulkReanalysis(message) {
    return !!message && !message.blacklisted && !String(message.index_error || '').trim();
  }

  function renderSelectionTools() {
    const count = mailUI.selectedMessageIDs.size;
    const label = document.getElementById('mailSelectionCount');
    if (label) label.textContent = `${count} selected`;
    const busy = !!mailUI.state?.ai?.running || mailUI.checking || mailUI.rescanning || mailUI.analyzingIDs.size > 0;
    const aiBtn=document.getElementById('mailAICheckBtn');
    if(aiBtn){
      const pending=Number(mailUI.state?.pending_ai||0);
      const analyzeCount=count>0?count:pending;
      aiBtn.textContent=`AI Analyze (${analyzeCount})`;
      aiBtn.title=count>0?`Analyze only the ${count} selected e-mail${count===1?'':'s'}`:`Analyze ${pending} downloaded e-mail${pending===1?'':'s'} still awaiting AI`;
      aiBtn.disabled=busy;
    }
    const clear = document.getElementById('mailClearSelectionBtn'); if (clear) clear.disabled = count === 0;
    const remove = document.getElementById('mailDeleteSelectedBtn'); if (remove) remove.disabled = count === 0 || busy;
    const rerun = document.getElementById('mailReanalyzeSelectedBtn'); if (rerun) rerun.disabled = count === 0 || busy;
    const visible = filteredMessages().slice(0,mailUI.visibleLimit);
    const selectedVisible = visible.filter(message=>mailUI.selectedMessageIDs.has(String(message.id))).length;
    const master = document.getElementById('mailSelectAll');
    if (master) {
      master.disabled = visible.length === 0 || busy;
      master.checked = visible.length > 0 && selectedVisible === visible.length;
      master.indeterminate = selectedVisible > 0 && selectedVisible < visible.length;
    }
    const dateBtn = document.getElementById('mailReanalyzeDateBtn'); if (dateBtn) dateBtn.disabled = busy;
  }

  function toggleMessageSelection(id) {
    id = String(id || '');
    if (!id) return;
    if (mailUI.selectedMessageIDs.has(id)) mailUI.selectedMessageIDs.delete(id);
    else mailUI.selectedMessageIDs.add(id);
    renderMessages();
  }

  function toggleVisibleSelectionFromMaster(event) {
    const visible = filteredMessages().slice(0,mailUI.visibleLimit);
    const shouldSelect = !!event?.target?.checked;
    for (const message of visible) {
      const id=String(message.id);
      if (shouldSelect) mailUI.selectedMessageIDs.add(id);
      else mailUI.selectedMessageIDs.delete(id);
    }
    renderMessages();
  }

  function clearMessageSelection() {
    mailUI.selectedMessageIDs.clear();
    renderMessages();
  }

  async function deleteSelectedMessages() {
    const ids=[...mailUI.selectedMessageIDs].map(String).filter(Boolean);
    if(!ids.length)return;
    if(mailUI.state?.ai?.running||mailUI.checking||mailUI.rescanning||mailUI.analyzingIDs.size){
      window.showToast?.('Stop the active E-Mail operation before deleting local messages.','warning',3400);
      return;
    }
    const message=`Delete ${ids.length} selected e-mail${ids.length===1?'':'s'} from AgentGO?\n\nThis removes the selected messages and any cached bodies from AgentGO only. Nothing is deleted from your real mail servers.\n\nA later Rescan Older Mail can intentionally find these messages again.`;
    if(!window.confirm(message))return;
    const button=document.getElementById('mailDeleteSelectedBtn');
    const previous=button?.textContent;
    if(button){button.disabled=true;button.textContent='Deleting…';}
    try{
      const data=await window.api('/api/email/messages/delete',{method:'POST',body:JSON.stringify({ids}),timeoutMs:30000});
      mailUI.state=data?.state||await window.api('/api/email/state',{timeoutMs:10000});
      for(const id of ids)mailUI.bodyCache.delete(id);
      if(ids.includes(String(mailUI.openMessageID||'')))mailUI.openMessageID='';
      mailUI.selectedMessageIDs.clear();
      resetVisibleLimit();
      const deleted=Number(data?.deleted||0);
      mailUI.lastOperation={main:`Deleted ${deleted} local e-mail${deleted===1?'':'s'} from AgentGO.`,sub:'Nothing was deleted from the upstream mail server.'};
      renderAll();
      window.showToast?.(`Deleted ${deleted} local e-mail${deleted===1?'':'s'} from AgentGO.`);
    }catch(err){
      window.showToast?.(err?.message||'Could not delete the selected local e-mails.','warning',4200);
    }finally{
      if(button){button.textContent=previous||'Delete Selected';renderSelectionTools();}
    }
  }

  function formatAnalysisTime(value) {
    if (!value) return '';
    const when = new Date(value);
    if (Number.isNaN(when.getTime())) return String(value);
    return when.toLocaleString([], {month:'short',day:'numeric',hour:'numeric',minute:'2-digit'});
  }

  function ruleActionLabel(action) {
    return ruleActionLabels[String(action||'')] || String(action||'').replaceAll('_',' ');
  }

  function renderRuleEffects(message) {
    const actions=Array.isArray(message?.rule_actions)?message.rule_actions:[];
    if(!actions.length)return '';
    return `<div class="mail-rule-effects"><strong>User Rules</strong>${actions.map(action=>`<span>${escapeHtml(ruleActionLabel(action))}</span>`).join('')}</div>`;
  }

  function displaySubject(message, priority, designation) {
    const subject=String(message?.subject||'(No subject)');
    if(!mailUI.state?.settings?.decorate_subjects)return subject;
    const tags=[];
    if(priority && priority!=='PENDING')tags.push(`[${priority}]`);
    if(designation && designation!=='Pending AI' && designation!=='Index Error')tags.push(`[${String(designation).toUpperCase()}]`);
    return `${tags.join('')}${tags.length?' ':''}${subject}`;
  }

  function renderMessages() {
    const host = document.getElementById('mailMessageList'); if (!host) return;
    renderSortHeaders();
    if(mailUI.loading){host.innerHTML='<div class="mail-empty mail-empty-state"><strong>Loading AgentGO E-Mail…</strong><span>Reading the local mail index.</span></div>';renderSelectionTools();return;}
    if(mailUI.loadError){host.innerHTML=`<div class="mail-empty mail-empty-state"><strong>Could not load E-Mail</strong><span>${escapeHtml(mailUI.loadError)}</span><button class="mail-list-tool-btn" data-mail-retry-load type="button">Retry</button></div>`;renderSelectionTools();return;}
    const allMessages = filteredMessages();
    if (!allMessages.length) {
      const accounts=mailUI.state?.settings?.accounts||[];
      const total=Array.isArray(mailUI.state?.messages)?mailUI.state.messages.length:0;
      if(!accounts.length) host.innerHTML='<div class="mail-empty mail-empty-state"><strong>No inboxes connected</strong><span>Add an e-mail account to start building the AgentGO mail index.</span><button class="mail-list-tool-btn" data-mail-empty-add type="button">Add E-Mail Account</button></div>';
      else if(!total) host.innerHTML='<div class="mail-empty mail-empty-state"><strong>No downloaded e-mail yet</strong><span>Use Download New to retrieve new messages from enabled INBOX accounts.</span></div>';
      else host.innerHTML='<div class="mail-empty mail-empty-state"><strong>No e-mails match this view</strong><span>Change or clear the current filters.</span></div>';
      renderSelectionTools(); return;
    }
    const totalMatches=allMessages.length;
    const messages=allMessages.slice(0,mailUI.visibleLimit);
    host.innerHTML = messages.map(message=>{
      const indexError = String(message.index_error || '');
      const aiError = String(message.ai_error || '');
      const deepAIError = String(message.deep_ai_error || '');
      const priority = message.priority || (message.blacklisted?'LOW':'PENDING');
      const designation = message.designation || (message.blacklisted?'Blacklisted':indexError?'Index Error':'Pending AI');
      const securityStatus = mailSecurityStatus(message);
      const ruleActions=Array.isArray(message.rule_actions)?message.rule_actions:[];
      const ruleSkip=ruleActions.includes('never_auto_analyze');
      const state = indexError ? 'Index Error' : aiError ? 'Needs Retry' : deepAIError ? 'Deep Retry' : message.blacklisted ? (message.analyzed?'Analyzed Override':'Blacklisted') : ruleSkip&&!message.analyzed ? 'Rule Skip' : message.deep_analyzed ? 'Deep Analysis' : message.analyzed ? (message.response_needed?'Response':'Analyzed') : 'Needs AI';
      const attachments = Array.isArray(message.attachments) ? message.attachments : [];
      const attachmentText = attachments.length ? attachments.map(item=>item.filename || item.content_type || 'Attachment').join(', ') : '';
      const open = mailUI.openMessageID === message.id;
      const selected = mailUI.selectedMessageIDs.has(String(message.id));
      const analyzing = mailUI.analyzingIDs.has(String(message.id));
      const analysisMeta = [message.analysis_model_id ? `Model ${message.analysis_model_id}` : '', message.analyzed_at ? formatAnalysisTime(message.analyzed_at) : ''].filter(Boolean).join(' · ');
      const shownSubject=displaySubject(message,priority,designation);
      return `<article class="mail-message${open?' is-open':''}${selected?' is-selected':''}" data-mail-message-wrap="${escapeHtml(message.id)}">
        <div class="mail-message-row">
          <button class="mail-message-select${selected?' is-selected':''}" data-mail-select-message="${escapeHtml(message.id)}" type="button" aria-pressed="${selected?'true':'false'}" title="${selected?'Remove from selection':'Select for batch re-analysis'}">${selected?'✓':''}</button>
          <button class="mail-message-summary" type="button" data-mail-message-id="${escapeHtml(message.id)}" aria-expanded="${open?'true':'false'}">
            <span class="mail-badge mail-type-badge" title="Type: ${escapeHtml(designation)}" aria-label="Type ${escapeHtml(designation)}">${escapeHtml(mailDesignationBadgeLabel(designation))}</span>
            <span class="mail-message-from" title="${escapeHtml(message.from||'')}">${accountBadgeHTML(message.account_id,true)}<span>${escapeHtml(message.from||'Unknown sender')}</span></span>
            <span class="mail-message-subject" title="${escapeHtml(message.subject||'')}">${statusClusterHTML(message,priority)}<span class="mail-subject-text">${escapeHtml(shownSubject)}</span>${renderUnsubscribeIndicator(message)}</span>
            <span class="mail-message-meta">${escapeHtml(formatDate(message.date))}</span>
          </button>
        </div>
        <div class="mail-message-detail"><div class="mail-detail-grid">
          <section class="mail-analysis"><div class="mail-detail-title">AgentGO Analysis</div>
            <div class="mail-analysis-row"><strong>Priority:</strong> ${escapeHtml(priority)}<br><strong>Type:</strong> ${escapeHtml(designation)}<br><strong>Status:</strong> <span class="mail-message-state${message.blacklisted?' blacklisted':''}${indexError?' index-error':''}${(aiError||deepAIError)?' ai-error':''}">${escapeHtml(state)}</span>${message.deep_analyzed?`<br><strong>Confidence:</strong> ${escapeHtml(message.confidence||'MEDIUM')}`:''}</div>
            <div class="mail-summary-text">${escapeHtml(indexError || message.summary || (message.blacklisted?'Skipped by subject blacklist.':'This message has not been AI checked yet.'))}</div>
            ${aiError?`<div class="mail-ai-error"><strong>AI batch retry needed:</strong> ${escapeHtml(aiError)}</div>`:''}
            ${deepAIError?`<div class="mail-ai-error"><strong>Detailed analysis retry needed:</strong> ${escapeHtml(deepAIError)}</div>`:''}
            ${message.deep_analyzed&&message.why_it_matters?`<div class="mail-analysis-section"><strong>Why it matters</strong><span>${escapeHtml(message.why_it_matters)}</span></div>`:''}
            ${message.deep_analyzed&&message.suggested_action?`<div class="mail-analysis-section"><strong>Suggested action</strong><span>${escapeHtml(message.suggested_action)}</span></div>`:''}
            ${message.deep_analyzed&&message.security_interpretation?`<div class="mail-analysis-section"><strong>AI security interpretation</strong><span>${escapeHtml(message.security_interpretation)}</span></div>`:''}
            ${renderRuleEffects(message)}
            ${analysisMeta?`<div class="mail-analysis-meta">${escapeHtml(analysisMeta)}</div>`:''}
            ${attachments.length?`<div class="mail-attachment-note"><strong>${attachments.length} attachment${attachments.length===1?'':'s'}:</strong> ${escapeHtml(attachmentText)}</div>`:''}
            ${renderUnsubscribe(message)}
            ${renderSecurityDetails(message)}
          </section>
          <section class="mail-original"><div class="mail-original-head"><div class="mail-detail-title">Original Message</div><button class="mail-detail-action" data-mail-local-seen="${escapeHtml(message.id)}" data-mail-seen-value="${message.local_seen?'false':'true'}" type="button">${message.local_seen?'Mark Unread':'Mark Read'}</button><button class="mail-detail-action" data-mail-analyze="${escapeHtml(message.id)}" type="button" ${analyzing?'disabled':''}>${analyzing?'Analyzing…':deepAIError?'Retry Analyze':message.deep_analyzed?'Re-analyze':'Analyze'}</button><button class="mail-detail-action" data-mail-create-reply="${escapeHtml(message.id)}" type="button">Create AI Reply</button></div>
            <div class="mail-original-body" data-mail-body="${escapeHtml(message.id)}">${open ? escapeHtml(mailUI.bodyCache.get(message.id) || 'Loading original message…') : ''}</div>
          </section>
        </div></div>
      </article>`;
    }).join('') + (totalMatches>messages.length?`<div class="mail-load-more"><span>Showing ${messages.length} of ${totalMatches} matching e-mails</span><button class="mail-list-tool-btn" data-mail-load-more type="button">Load ${Math.min(mailUI.pageSize,totalMatches-messages.length)} More</button></div>`:`<div class="mail-load-more is-complete"><span>${totalMatches} matching e-mail${totalMatches===1?'':'s'}</span></div>`);
    renderSelectionTools();
  }

  async function toggleMessage(id) {
    id = String(id||'');
    const opening = mailUI.openMessageID !== id;
    mailUI.openMessageID = opening ? id : '';
    renderMessages();
    if (opening) {
      markMessageReadOnOpen(id);
      await ensureBody(id);
    }
  }

  async function markMessageReadOnOpen(id) {
    const record=(mailUI.state?.messages||[]).find(item=>String(item.id)===String(id));
    if(!record||record.local_seen)return;
    record.local_seen=true;
    const wrap=document.querySelector(`[data-mail-message-wrap="${CSS.escape(String(id))}"]`);
    const readState=wrap?.querySelector('.mail-read-state');
    if(readState){
      readState.classList.remove('unread');
      readState.classList.add('read');
      readState.title='Read';
      readState.setAttribute('aria-label','Read');
    }
    const button=wrap?.querySelector('[data-mail-local-seen]');
    if(button){button.dataset.mailSeenValue='false';button.textContent='Mark Unread';}
    renderAccounts();renderAccountHealth();
    try{
      await window.api('/api/email/message',{method:'POST',body:JSON.stringify({id,local_seen:true}),timeoutMs:10000});
    }catch(err){
      record.local_seen=false;
      renderAccounts();renderAccountHealth();renderMessages();
      window.showToast?.(err?.message||'Could not update local read state.','warning',3200);
    }
  }

  async function ensureBody(id) {
    if (!id || mailUI.bodyCache.has(id)) { renderBody(id); return; }
    try {
      const data = await window.api(`/api/email/message?id=${encodeURIComponent(id)}`, {timeoutMs:60000});
      mailUI.bodyCache.set(id,String(data?.body||'(Message body is empty.)'));
      if (Array.isArray(mailUI.state?.messages)) {
        const record=mailUI.state.messages.find(item=>String(item.id)===String(id));
        if(record){
          record.local_seen=true;
          if(data?.security)record.security=data.security;
          if(data?.unsubscribe)record.unsubscribe=data.unsubscribe;
          if(data?.in_reply_to)record.in_reply_to=data.in_reply_to;
          if(Array.isArray(data?.references))record.references=data.references;
        }
      }
    } catch (err) {
      mailUI.bodyCache.set(id,`Could not retrieve the original message: ${err?.message || 'Unknown error'}`);
    }
    renderAccounts(); renderFilterCounts(); renderAccountHealth(); renderMessages();
  }

  function renderBody(id) {
    const target = document.querySelector(`[data-mail-body="${CSS.escape(String(id))}"]`);
    if (target) target.textContent = mailUI.bodyCache.get(id) || 'Loading original message…';
  }

  async function setLocalSeen(id,seen){
    id=String(id||'');if(!id)return;
    try{mailUI.state=await window.api('/api/email/message',{method:'POST',body:JSON.stringify({id,local_seen:!!seen}),timeoutMs:10000});renderAll();}
    catch(err){window.showToast?.(err?.message||'Could not update local read state.','warning',3200);}
  }

  function renderStatus() {
    const ai = mailUI.state?.ai || {};
    const pending = Number(mailUI.state?.pending_ai || 0);
    const syncing = !!mailUI.checking || !!mailUI.rescanning || !!mailUI.state?.sync_running;
    const deepRunning = mailUI.analyzingIDs.size > 0;
    const active = syncing || !!ai.running || deepRunning;
    const aiBtn = document.getElementById('mailAICheckBtn');
    if (aiBtn) {
      const selectedCount=mailUI.selectedMessageIDs.size;
      const analyzeCount=selectedCount>0?selectedCount:pending;
      aiBtn.disabled = !!ai.running || syncing || deepRunning;
      aiBtn.textContent = `AI Analyze (${analyzeCount})`;
      aiBtn.title = selectedCount>0 ? `Analyze only the ${selectedCount} selected e-mail${selectedCount===1?'':'s'}` : `Analyze ${pending} downloaded e-mail${pending===1?'':'s'} still awaiting AI`;
    }
    const checkBtn = document.getElementById('mailCheckBtn');
    if (checkBtn) {
      checkBtn.disabled = syncing || !!ai.running || deepRunning || mailUI.newMailCountLoading;
      const countText = mailUI.newMailCountLoading ? '…' : (Number.isFinite(mailUI.newMailCount) ? String(mailUI.newMailCount) : '?');
      checkBtn.textContent = `Download New (${countText})`;
      checkBtn.title = mailUI.newMailCountError ? `New-mail count warning: ${mailUI.newMailCountError}` : 'Download newly available INBOX messages';
    }
    const refreshBtn=document.getElementById('mailNewCountRefreshBtn');
    if(refreshBtn){refreshBtn.disabled=active||mailUI.newMailCountLoading;refreshBtn.classList.toggle('is-loading',mailUI.newMailCountLoading);}
    const rescanBtn = document.getElementById('mailRescanBtn'); if (rescanBtn) rescanBtn.disabled = syncing || !!ai.running || deepRunning;
    const cancelBtn=document.getElementById('mailCancelBtn'); if(cancelBtn){cancelBtn.hidden=!active;cancelBtn.disabled=!active||mailUI.canceling;cancelBtn.textContent=mailUI.canceling?'Canceling…':'Cancel';}
    const progress=document.querySelector('.mail-status-progress'); if(progress) progress.hidden=!active;
    renderSelectionTools();
    if (!active && mailUI.cancelRequested) { mailUI.cancelRequested=false; mailUI.canceling=false; }
    if (syncing) return;
    if (ai.running) {
      const processed = Number(ai.processed_emails||0), total=Number(ai.total_emails||0), remaining=Number(ai.remaining_batches||0);
      setStatus(ai.status || `Processing ${processed} of ${total} emails.`, `${remaining} batch(es) remaining · batch size ${Number(ai.batch_size||50)}`);
      setProgress(total ? (processed/total)*100 : 0, `${processed}/${total}`);
    } else if (deepRunning) {
      setProgress(0,'deep analysis');
    } else {
      const countNote = mailUI.newMailCountError ? `New-mail count unavailable for at least one inbox: ${mailUI.newMailCountError}` : (Number.isFinite(mailUI.newMailCount) ? `${mailUI.newMailCount} new message${mailUI.newMailCount===1?'':'s'} available on enabled inboxes.` : 'New-mail count has not been refreshed yet.');
      if (mailUI.lastOperation) setStatus(mailUI.lastOperation.main, `${mailUI.lastOperation.sub}${mailUI.lastOperation.sub?' · ':''}${pending} awaiting AI processing.`);
      else setStatus(ai.status || (pending ? `${pending} downloaded e-mail${pending===1?'':'s'} awaiting AI processing.` : 'Ready. Download New retrieves new INBOX messages.'), `${countNote} AI Analyze never downloads mail first.`);
      setProgress(0,'');
    }
  }

  function setStatus(main, sub='') {
    const mainEl=document.getElementById('mailStatusMain'), subEl=document.getElementById('mailStatusSub');
    if (mainEl) mainEl.textContent=String(main||''); if (subEl) subEl.textContent=String(sub||'');
  }
  function setProgress(percent,label) {
    const fill=document.getElementById('mailProgressFill'), text=document.getElementById('mailProgressLabel');
    if (fill) fill.style.width=`${Math.max(0,Math.min(100,Number(percent)||0))}%`; if (text) text.textContent=String(label||'');
  }

  async function cancelActiveMailOperation() {
    const ai=mailUI.state?.ai||{};
    const active=mailUI.checking||mailUI.rescanning||!!mailUI.state?.sync_running||!!ai.running||mailUI.analyzingIDs.size>0;
    if(!active||mailUI.canceling)return;
    mailUI.canceling=true;mailUI.cancelRequested=true;
    setStatus('Cancel requested…','AgentGO is stopping the active E-Mail operation. Completed downloads and completed AI results are kept.');
    renderStatus();
    try{
      const data=await window.api('/api/email/cancel',{method:'POST',body:'{}',timeoutMs:10000});
      if(data?.state)mailUI.state=data.state;
      mailUI.lastOperation={main:'Cancel requested.',sub:'AgentGO is stopping the active E-Mail operation. Completed work is kept.'};
      manageAIPoll();renderStatus();
    }catch(err){
      mailUI.canceling=false;mailUI.cancelRequested=false;renderStatus();
      window.showToast?.(err?.message||'Could not cancel the E-Mail operation.','warning',3600);
    }
  }

  async function refreshNewMailCount(options={}) {
    if (!mailUI.open || mailUI.newMailCountLoading || mailUI.checking || mailUI.rescanning || mailUI.state?.sync_running || mailUI.state?.ai?.running || mailUI.analyzingIDs.size) return;
    mailUI.newMailCountLoading=true; mailUI.newMailCountError=''; renderStatus();
    try {
      const data=await window.api('/api/email/new-count',{timeoutMs:90000});
      mailUI.newMailCount=Number(data?.count||0);
      const failures=(Array.isArray(data?.accounts)?data.accounts:[]).filter(item=>item.error);
      mailUI.newMailCountError=failures.length?`${failures.length} inbox${failures.length===1?'':'es'} could not be counted.`:'';
      if(options.toast&&failures.length)window.showToast?.(mailUI.newMailCountError,'warning',3200);
    } catch(err) {
      mailUI.newMailCount=null;
      mailUI.newMailCountError=String(err?.message||'Could not refresh new-mail count.');
      if(options.toast)window.showToast?.(mailUI.newMailCountError,'warning',3200);
    } finally {
      mailUI.newMailCountLoading=false; renderStatus();
    }
  }

  function startNewMailCountTimer() {
    if(mailUI.newMailCountTimer)clearInterval(mailUI.newMailCountTimer);
    mailUI.newMailCountTimer=setInterval(()=>refreshNewMailCount(),30*60*1000);
  }

  function selectAccount(id) {
    id=String(id||'all');
    if(id==='all') mailUI.selectedAccounts.clear();
    else if(mailUI.selectedAccounts.has(id)) mailUI.selectedAccounts.delete(id);
    else mailUI.selectedAccounts.add(id);
    resetVisibleLimit(); renderAccounts(); renderFilterCounts(); renderMessages();
  }

  async function toggleAccount(id,enabled) {
    try { mailUI.state = await window.api('/api/email/accounts/toggle',{method:'POST',body:JSON.stringify({id,enabled}),timeoutMs:10000}); renderAll(); refreshNewMailCount(); }
    catch(err){ window.showToast?.(err?.message||'Could not update account toggle.','warning',3200); }
  }

  async function checkEmail() {
    if (mailUI.checking || mailUI.rescanning) return;
    const enabled=(mailUI.state?.settings?.accounts||[]).filter(a=>a.enabled).length;
    mailUI.lastOperation=null; mailUI.checking=true; setStatus(`Downloading new mail from ${enabled} enabled INBOX${enabled===1?'':'es'}…`,'Read-only incremental sync. This does not run AI analysis.'); setProgress(0,'downloading'); renderStatus();
    const btn=document.getElementById('mailCheckBtn'); if(btn) btn.disabled=true;
    try {
      const data=await window.api('/api/email/check',{method:'POST',body:'{}',timeoutMs:240000});
      mailUI.state=data?.state || await window.api('/api/email/state',{timeoutMs:10000});
      const results=Array.isArray(data?.accounts)?data.accounts:[];
      const failures=results.filter(item=>item.error);
      const added=Number(data?.added||0);
      mailUI.lastOperation={main:added ? `Downloaded ${added} new e-mail${added===1?'':'s'}.` : 'No new e-mails were found.',sub:operationSummary(results,'Checked')};
      if(failures.length) window.showToast?.(`${failures.length} e-mail account${failures.length===1?'':'s'} could not be checked.`,'warning',4200);
      renderAll();
    } catch(err){ if(mailUI.cancelRequested){mailUI.lastOperation={main:'Download New canceled.',sub:'Any messages fully downloaded before cancellation remain safely indexed.'};}else{mailUI.lastOperation={main:'Download New failed.',sub:err?.message||'Could not check inboxes.'};window.showToast?.(err?.message||'Could not check inboxes.','warning',4200);} }
    finally { mailUI.checking=false; if(btn) btn.disabled=false; if(mailUI.cancelRequested){mailUI.state=await window.api('/api/email/state',{timeoutMs:10000}).catch(()=>mailUI.state);} renderAll(); refreshNewMailCount(); }
  }

  async function startAIBatchOperation(payload) {
    try {
      mailUI.lastOperation=null;
      const data=await window.api('/api/email/ai/check',{method:'POST',body:JSON.stringify(payload||{}),timeoutMs:15000});
      if (mailUI.state) mailUI.state.ai=data?.ai || mailUI.state.ai;
      renderStatus(); renderSelectionTools(); manageAIPoll();
      return data?.ai || null;
    } catch(err){ window.showToast?.(err?.message||'Could not start E-Mail AI processing.','warning',4200); return null; }
  }

  async function aiCheckEmails() {
    const ids=[...mailUI.selectedMessageIDs].map(String).filter(Boolean);
    if(ids.length){
      const ai=await startAIBatchOperation({mode:'selected_analyze',ids});
      if(ai && Number(ai.total_emails||0)>0){
        mailUI.selectedMessageIDs.clear();
        renderMessages();
        renderStatus();
      }
      return;
    }
    await startAIBatchOperation({mode:'pending'});
  }

  async function reanalyzeSelectedMessages() {
    const messages = Array.isArray(mailUI.state?.messages) ? mailUI.state.messages : [];
    const byID = new Map(messages.map(message=>[String(message.id),message]));
    const ids = [...mailUI.selectedMessageIDs].filter(id=>eligibleForBulkReanalysis(byID.get(String(id))));
    if (!ids.length) { window.showToast?.('No eligible selected e-mails to re-analyze.','warning',3200); return; }
    if (!window.confirm(`Re-analyze ${ids.length} selected e-mail${ids.length===1?'':'s'} with the current E-Mail AI model?`)) return;
    const ai = await startAIBatchOperation({mode:'selected',ids});
    if (ai && Number(ai.total_emails||0) > 0) {
      mailUI.selectedMessageIDs.clear();
      renderMessages();
    }
  }

  function localDateInput(date) {
    const y=date.getFullYear(), m=String(date.getMonth()+1).padStart(2,'0'), d=String(date.getDate()).padStart(2,'0');
    return `${y}-${m}-${d}`;
  }

  function parseLocalDateInput(value) {
    const parts=String(value||'').split('-').map(Number);
    if (parts.length!==3 || !parts.every(Number.isFinite)) return null;
    const date=new Date(parts[0],parts[1]-1,parts[2],0,0,0,0);
    return Number.isNaN(date.getTime()) ? null : date;
  }

  function countDateRangeReanalysis(startDate,endDate) {
    if (!startDate || !endDate || endDate < startDate) return 0;
    const endExclusive=new Date(endDate); endExclusive.setDate(endExclusive.getDate()+1);
    return (mailUI.state?.messages||[]).filter(message=>{
      if (!eligibleForBulkReanalysis(message)) return false;
      if (mailUI.selectedAccounts.size && !mailUI.selectedAccounts.has(String(message.account_id))) return false;
      const when=new Date(message.date || message.indexed_at || '');
      return !Number.isNaN(when.getTime()) && when>=startDate && when<endExclusive;
    }).length;
  }

  function updateReanalyzeCount() {
    const start=parseLocalDateInput(document.getElementById('mailReanalyzeStart')?.value);
    const end=parseLocalDateInput(document.getElementById('mailReanalyzeEnd')?.value);
    const count=countDateRangeReanalysis(start,end);
    const selected=selectedAccountIDs(); const scope=document.getElementById('mailReanalyzeScope'); if(scope) scope.textContent=`Account scope: ${selected.length?selected.map(accountLabel).join(', '):'Unified Account'}`;
    const text=document.getElementById('mailReanalyzeCount');
    if(text) text.textContent=start&&end&&end>=start?`${count} eligible indexed e-mail${count===1?'':'s'} in this range.`:'Choose a valid start and end date.';
    const run=document.getElementById('mailReanalyzeRunBtn'); if(run) run.disabled=count===0 || !!mailUI.state?.ai?.running;
    return {start,end,count};
  }

  function openReanalyzeModal() {
    const modal=document.getElementById('mailReanalyzeModal'); if(!modal)return;
    const today=new Date(); const start=new Date(today); start.setDate(start.getDate()-7);
    const startInput=document.getElementById('mailReanalyzeStart'), endInput=document.getElementById('mailReanalyzeEnd');
    if(startInput&&!startInput.value)startInput.value=localDateInput(start);
    if(endInput&&!endInput.value)endInput.value=localDateInput(today);
    updateReanalyzeCount(); modal.classList.add('open'); modal.setAttribute('aria-hidden','false');
  }

  function closeReanalyzeModal(){const modal=document.getElementById('mailReanalyzeModal');if(modal){modal.classList.remove('open');modal.setAttribute('aria-hidden','true');}}

  async function reanalyzeDateRange() {
    const {start,end,count}=updateReanalyzeCount(); if(!start||!end||end<start||!count)return;
    const endExclusive=new Date(end); endExclusive.setDate(endExclusive.getDate()+1);
    closeReanalyzeModal();
    await startAIBatchOperation({mode:'date_range',start_at:start.toISOString(),end_at:endExclusive.toISOString(),account_ids:selectedAccountIDs()});
  }

  async function analyzeMessage(id) {
    id=String(id||''); if(!id || mailUI.analyzingIDs.has(id))return;
    if(mailUI.state?.ai?.running){window.showToast?.('Finish or stop the current E-Mail AI batch first.','warning',3200);return;}
    mailUI.analyzingIDs.add(id); renderMessages();
    const message=(mailUI.state?.messages||[]).find(item=>String(item.id)===id);
    setStatus(`${message?.deep_ai_error?'Retrying analysis of':message?.deep_analyzed?'Re-analyzing':'Analyzing'} ${message?.subject||'e-mail'}…`,'Deep review uses the full message and Phase 3 security evidence. Emergency Stop cancels the AI call.');
    setProgress(0,'deep analysis');
    try{
      const data=await window.api('/api/email/ai/analyze',{method:'POST',body:JSON.stringify({id}),timeoutMs:180000});
      mailUI.state=data?.state||await window.api('/api/email/state',{timeoutMs:10000});
      mailUI.lastOperation={main:'Detailed E-Mail analysis complete.',sub:'Priority, type, suggested action, confidence, and security interpretation were updated.'};
      window.showToast?.('Detailed e-mail analysis complete.');
    }catch(err){if(mailUI.cancelRequested){mailUI.lastOperation={main:'E-Mail analysis canceled.',sub:'The message remains available for Analyze or Re-analyze later.'};}else{window.showToast?.(err?.message||'Could not analyze this e-mail.','warning',4400);}}
    finally{mailUI.analyzingIDs.delete(id);if(mailUI.cancelRequested){mailUI.state=await window.api('/api/email/state',{timeoutMs:10000}).catch(()=>mailUI.state);}renderAll();manageAIPoll();}
  }

  function manageAIPoll() {
    const running=!!mailUI.state?.ai?.running;
    if (running && !mailUI.aiTimer) mailUI.aiTimer=setInterval(()=>loadState({models:false,poll:true}),1000);
    if (!running && mailUI.aiTimer) { clearInterval(mailUI.aiTimer); mailUI.aiTimer=null; }
  }

  function openRescan(){
    mailUI.rescanMode='last_sync';
    document.querySelectorAll('[data-mail-rescan-mode]').forEach(btn=>btn.classList.toggle('is-active',btn.dataset.mailRescanMode==='last_sync'));
    const field=document.getElementById('mailCustomDateField'); if(field) field.hidden=true;
    const date=document.getElementById('mailCustomDate'); if(date && !date.value) date.value=new Date().toISOString().slice(0,10);
    const modal=document.getElementById('mailRescanModal'); if(modal){modal.classList.add('open');modal.setAttribute('aria-hidden','false');}
  }
  function closeRescan(){const modal=document.getElementById('mailRescanModal');if(modal){modal.classList.remove('open');modal.setAttribute('aria-hidden','true');}}
  async function runRescan(){
    if(mailUI.rescanning || mailUI.checking)return;
    const customDate=String(document.getElementById('mailCustomDate')?.value||'');
    if(mailUI.rescanMode==='custom'&&!customDate){window.showToast?.('Choose a custom rescan date.','warning',3000);return;}
    const enabled=(mailUI.state?.settings?.accounts||[]).filter(a=>a.enabled).length;
    mailUI.lastOperation=null; mailUI.rescanning=true; closeRescan(); setStatus(`Rescanning ${enabled} enabled INBOX${enabled===1?'':'es'}…`,'Backfill is read-only and deduplicates against the local AgentGO mail index.'); setProgress(0,'rescanning'); renderStatus();
    const runBtn=document.getElementById('mailRescanRunBtn'); if(runBtn)runBtn.disabled=true;
    try{
      const data=await window.api('/api/email/rescan',{method:'POST',body:JSON.stringify({mode:mailUI.rescanMode,custom_date:customDate}),timeoutMs:360000});
      mailUI.state=data?.state||await window.api('/api/email/state',{timeoutMs:10000});
      const results=Array.isArray(data?.accounts)?data.accounts:[]; const failures=results.filter(item=>item.error); const added=Number(data?.added||0);
      mailUI.lastOperation={main:added?`Rescan added ${added} e-mail${added===1?'':'s'} to the index.`:'Rescan completed with no new indexed e-mail.',sub:operationSummary(results,'Rescanned')};
      if(failures.length)window.showToast?.(`${failures.length} e-mail account${failures.length===1?'':'s'} could not be fully rescanned.`,'warning',4300);
      renderAll();
    }catch(err){if(mailUI.cancelRequested){mailUI.lastOperation={main:'Rescan canceled.',sub:'Any messages fully indexed before cancellation remain available.'};}else{mailUI.lastOperation={main:'Rescan failed.',sub:err?.message||'Could not rescan inboxes.'};window.showToast?.(err?.message||'Could not rescan inboxes.','warning',4300);}}
    finally{mailUI.rescanning=false;if(runBtn)runBtn.disabled=false;if(mailUI.cancelRequested){mailUI.state=await window.api('/api/email/state',{timeoutMs:10000}).catch(()=>mailUI.state);}renderAll();refreshNewMailCount();}
  }

  function openAccountModal(id='') {
    mailUI.accountEditID=String(id||''); const modal=document.getElementById('mailAccountModal'); if(!modal)return;
    const account=accountByID(mailUI.accountEditID);
    document.getElementById('mailAccountTitle').textContent=account?'Edit E-Mail Account':'Add E-Mail Account';
    document.getElementById('mailAccountName').value=account?.name||'';
    document.getElementById('mailAccountEmail').value=account?.email||'';
    document.getElementById('mailAccountHost').value=account?.host||'';
    document.getElementById('mailAccountPort').value=Number(account?.port||993);
    document.getElementById('mailAccountUser').value=account?.username||'';
    document.getElementById('mailAccountSecurity').value=account?.security||'tls';
    document.getElementById('mailAccountPassword').value='';
    document.getElementById('mailAccountBlacklist').value=Array.isArray(account?.subject_blacklist)?account.subject_blacklist.join('\n'):'';
    const deleteBtn=document.getElementById('mailAccountDeleteBtn'); if(deleteBtn)deleteBtn.hidden=!account;
    modal.classList.add('open'); modal.setAttribute('aria-hidden','false');
  }
  function closeAccountModal(){const modal=document.getElementById('mailAccountModal');if(modal){modal.classList.remove('open');modal.setAttribute('aria-hidden','true');}mailUI.accountEditID='';}
  function accountPayload(){return {id:mailUI.accountEditID,name:document.getElementById('mailAccountName')?.value||'',email:document.getElementById('mailAccountEmail')?.value||'',host:document.getElementById('mailAccountHost')?.value||'',port:Number(document.getElementById('mailAccountPort')?.value||0),username:document.getElementById('mailAccountUser')?.value||'',password:document.getElementById('mailAccountPassword')?.value||'',security:document.getElementById('mailAccountSecurity')?.value||'tls',subject_blacklist:String(document.getElementById('mailAccountBlacklist')?.value||'').split(/\r?\n/).map(v=>v.trim()).filter(Boolean)};}


  async function deleteAccount(){
    const id=mailUI.accountEditID; if(!id)return;
    const account=accountByID(id); if(!window.confirm(`Remove ${account?.name || 'this e-mail account'} from AgentGO? The real mailbox is not changed.`))return;
    try{mailUI.state=await window.api('/api/email/accounts/delete',{method:'POST',body:JSON.stringify({id}),timeoutMs:15000});closeAccountModal();renderAll();refreshNewMailCount();window.showToast?.('E-mail account removed from AgentGO.');}
    catch(err){window.showToast?.(err?.message||'Could not remove e-mail account.','warning',4200);}
  }
  async function saveAccount(){try{mailUI.state=await window.api('/api/email/accounts/save',{method:'POST',body:JSON.stringify(accountPayload()),timeoutMs:15000});closeAccountModal();renderAll();refreshNewMailCount();window.showToast?.('E-mail account saved.');}catch(err){window.showToast?.(err?.message||'Could not save e-mail account.','warning',4200);}}
  async function testAccount(){const btn=document.getElementById('mailAccountTestBtn');if(btn)btn.disabled=true;try{const data=await window.api('/api/email/accounts/test',{method:'POST',body:JSON.stringify(accountPayload()),timeoutMs:30000});window.showToast?.(data?.message||'Connection succeeded.');}catch(err){window.showToast?.(err?.message||'Connection test failed.','warning',4500);}finally{if(btn)btn.disabled=false;}}

  function populateRuleScope(){
    const select=document.getElementById('mailRuleScope');if(!select)return;
    const current=select.value;
    select.innerHTML='<option value="">All Accounts</option>'+(mailUI.state?.settings?.accounts||[]).map(account=>`<option value="${escapeHtml(account.id)}">${escapeHtml(account.name||account.email||'Mail Account')}</option>`).join('');
    select.value=[...select.options].some(option=>option.value===current)?current:'';
  }

  function renderStagedRules(){
    const host=document.getElementById('mailRuleList');if(!host)return;
    host.innerHTML=mailUI.stagedRules.length?mailUI.stagedRules.map(rule=>{
      const scope=rule.account_id?accountLabel(rule.account_id):'All Accounts';
      const match=rule.match==='domain'?'Domain':'Sender';
      return `<div class="mail-rule-row"><div><strong>${escapeHtml(ruleActionLabel(rule.action))}</strong><span>${escapeHtml(scope)} · ${escapeHtml(match)} · ${escapeHtml(rule.pattern)}</span></div><button class="mail-account-mini" data-mail-rule-remove="${escapeHtml(rule.id)}" type="button" title="Remove rule">×</button></div>`;
    }).join(''):'<div class="mail-rule-empty">No sender/domain rules yet.</div>';
  }

  function addStagedRule(){
    const match=String(document.getElementById('mailRuleMatch')?.value||'sender');
    let pattern=String(document.getElementById('mailRulePattern')?.value||'').trim().toLowerCase();
    const action=String(document.getElementById('mailRuleAction')?.value||'always_important');
    const accountID=String(document.getElementById('mailRuleScope')?.value||'');
    if(match==='domain')pattern=pattern.replace(/^@/,'').replace(/\.+$/,'');
    if(!pattern){window.showToast?.('Enter a sender address or domain for the rule.','warning',3000);return;}
    if(match==='sender'&&!pattern.includes('@')){window.showToast?.('Sender rules should use a complete e-mail address.','warning',3200);return;}
    const duplicate=mailUI.stagedRules.some(rule=>String(rule.match)===match&&String(rule.pattern).toLowerCase()===pattern&&String(rule.action)===action&&String(rule.account_id||'')===accountID);
    if(duplicate){window.showToast?.('That E-Mail rule already exists.','warning',2800);return;}
    mailUI.stagedRules.push({id:`ui-${Date.now()}-${mailUI.stagedRules.length}`,match,pattern,action,account_id:accountID});
    const input=document.getElementById('mailRulePattern');if(input)input.value='';
    renderStagedRules();
  }

  function openSettings(){
    const settings=mailUI.state?.settings||{};
    mailUI.stagedBatch=Number(settings.ai_batch_size||50);
    mailUI.stagedCacheDays=Number(settings.body_cache_days||7);
    mailUI.stagedReplyTone=String(settings.reply_tone||'neutral');
    mailUI.stagedReplyLength=String(settings.reply_length||'standard');
    mailUI.stagedBlacklist=Array.isArray(settings.subject_blacklist)?settings.subject_blacklist:[];
    mailUI.stagedRules=Array.isArray(settings.rules)?settings.rules.map(rule=>({...rule})):[];
    mailUI.stagedDecorateSubjects=!!settings.decorate_subjects;
    document.querySelectorAll('[data-mail-batch]').forEach(btn=>btn.classList.toggle('is-active',Number(btn.dataset.mailBatch)===mailUI.stagedBatch));
    document.querySelectorAll('[data-mail-cache-days]').forEach(btn=>btn.classList.toggle('is-active',Number(btn.dataset.mailCacheDays)===mailUI.stagedCacheDays));
    document.querySelectorAll('[data-mail-reply-tone]').forEach(btn=>btn.classList.toggle('is-active',String(btn.dataset.mailReplyTone)===mailUI.stagedReplyTone));
    document.querySelectorAll('[data-mail-reply-length]').forEach(btn=>btn.classList.toggle('is-active',String(btn.dataset.mailReplyLength)===mailUI.stagedReplyLength));
    const input=document.getElementById('mailBlacklistInput'); if(input)input.value=mailUI.stagedBlacklist.join('\n');
    const decorate=document.getElementById('mailDecorateSubjects');if(decorate)decorate.checked=mailUI.stagedDecorateSubjects;
    populateRuleScope();renderStagedRules();renderAccounts();renderAccountHealth();
    const modal=document.getElementById('mailSettingsModal'); if(modal){mailUI.modalReturnFocus=document.activeElement;modal.classList.add('open');modal.setAttribute('aria-hidden','false');setTimeout(()=>modal.querySelector('.mail-modal-close')?.focus(),0);}
  }
  function closeSettings(){const modal=document.getElementById('mailSettingsModal');if(modal?.classList.contains('open')){modal.classList.remove('open');modal.setAttribute('aria-hidden','true');mailUI.modalReturnFocus?.focus?.();mailUI.modalReturnFocus=null;}}
  async function saveSettings(){
    const blacklist=String(document.getElementById('mailBlacklistInput')?.value||'').split(/\r?\n/).map(v=>v.trim()).filter(Boolean);
    const decorate=!!document.getElementById('mailDecorateSubjects')?.checked;
    try{mailUI.state=await window.api('/api/email/settings',{method:'POST',body:JSON.stringify({ai_model_id:mailUI.state?.settings?.ai_model_id||'',ai_batch_size:mailUI.stagedBatch,body_cache_days:mailUI.stagedCacheDays,reply_tone:mailUI.stagedReplyTone,reply_length:mailUI.stagedReplyLength,subject_blacklist:blacklist,rules:mailUI.stagedRules,decorate_subjects:decorate}),timeoutMs:12000});closeSettings();renderAll();window.showToast?.('E-Mail settings saved.');}
    catch(err){window.showToast?.(err?.message||'Could not save E-Mail settings.','warning',3800);}
  }
  async function saveQuickSettings(overrides){
    const settings=mailUI.state?.settings||{};
    try{mailUI.state=await window.api('/api/email/settings',{method:'POST',body:JSON.stringify({ai_model_id:overrides.ai_model_id??settings.ai_model_id??'',ai_batch_size:Number(settings.ai_batch_size||50),body_cache_days:Number(settings.body_cache_days||7),reply_tone:overrides.reply_tone??settings.reply_tone??'neutral',reply_length:overrides.reply_length??settings.reply_length??'standard',subject_blacklist:Array.isArray(settings.subject_blacklist)?settings.subject_blacklist:[],rules:Array.isArray(settings.rules)?settings.rules:[],decorate_subjects:!!settings.decorate_subjects}),timeoutMs:12000});renderAll();}
    catch(err){window.showToast?.(err?.message||'Could not save E-Mail preference.','warning',3600);await loadState({models:true});}
  }


  async function clearLocalMailData(){
    const message=`Clear AgentGO's local E-Mail data?\n\nThis removes the downloaded mail index, AI classifications, local read state, and cached message bodies. Connected accounts, saved passwords, and the current sync position are kept. Nothing is deleted from your real mail servers.\n\nOlder mail can still be recovered later with Rescan Older Mail.`;
    if(!window.confirm(message))return;
    const btn=document.getElementById('mailClearLocalDataBtn');const previous=btn?.textContent;if(btn){btn.disabled=true;btn.textContent='Clearing…';}
    try{
      const data=await window.api('/api/email/clear-local',{method:'POST',body:'{}',timeoutMs:30000});
      mailUI.state=data?.state||await window.api('/api/email/state',{timeoutMs:10000});
      mailUI.bodyCache.clear();mailUI.selectedMessageIDs.clear();mailUI.openMessageID='';resetVisibleLimit();
      mailUI.lastOperation={main:`Cleared ${Number(data?.cleared||0)} local e-mail record${Number(data?.cleared||0)===1?'':'s'}.`,sub:'Accounts, saved passwords, and sync position were kept. Nothing was deleted upstream.'};
      closeSettings();renderAll();refreshNewMailCount();window.showToast?.('Local AgentGO E-Mail data cleared.');
    }catch(err){window.showToast?.(err?.message||'Could not clear local E-Mail data.','warning',4200);}
    finally{if(btn){btn.disabled=false;btn.textContent=previous||'Clear Local Mail Data';}}
  }

  async function createReply(id,regenerate=false){
    const modal=document.getElementById('mailReplyModal'), text=document.getElementById('mailReplyText'), tone=document.getElementById('mailReplyTone'), length=document.getElementById('mailReplyLength'), meta=document.getElementById('mailReplyMeta'); if(!modal||!text)return;
    id=String(id||mailUI.replyMessageID||'');if(!id)return;mailUI.replyMessageID=id;mailUI.replyResult=null;
    const settings=mailUI.state?.settings||{};
    if(!regenerate){if(tone)tone.value=String(settings.reply_tone||'neutral');if(length)length.value=String(settings.reply_length||'standard');mailUI.modalReturnFocus=document.activeElement;modal.classList.add('open');modal.setAttribute('aria-hidden','false');}
    const replyTone=String(tone?.value||settings.reply_tone||'neutral'),replyLength=String(length?.value||settings.reply_length||'standard');
    text.value='Generating draft…';text.disabled=true;if(meta)meta.textContent='Building reply context…';setReplyButtonsBusy(true);
    if(!regenerate)setTimeout(()=>tone?.focus(),0);
    try{
      const data=await window.api('/api/email/reply',{method:'POST',body:JSON.stringify({id,tone:replyTone,length:replyLength}),timeoutMs:120000});
      mailUI.replyResult=data||{};text.value=String(data?.reply||'(No reply text returned.)');
      if(meta){const bits=[];if(data?.to)bits.push(`To ${data.to}`);const threads=Number(data?.thread_messages||0);bits.push(threads?`${threads} earlier indexed thread message${threads===1?'':'s'} included`:'Current message context');meta.textContent=bits.join(' · ');}
    }catch(err){text.value=`Could not generate reply: ${err?.message||'Unknown error'}`;if(meta)meta.textContent='Draft generation failed. You can retry or use Emergency Stop if an AI call is still running.';}
    finally{text.disabled=false;setReplyButtonsBusy(false);}
  }
  function setReplyButtonsBusy(busy){for(const id of ['mailRegenerateReplyBtn','mailOpenReplyBtn','mailCopyReplyBtn']){const btn=document.getElementById(id);if(btn)btn.disabled=!!busy;}}
  function regenerateReply(){if(mailUI.replyMessageID)createReply(mailUI.replyMessageID,true);}
  function closeReplyModal(){const modal=document.getElementById('mailReplyModal');if(modal?.classList.contains('open')){modal.classList.remove('open');modal.setAttribute('aria-hidden','true');mailUI.modalReturnFocus?.focus?.();mailUI.modalReturnFocus=null;}}
  async function copyReply(){const text=document.getElementById('mailReplyText')?.value||'';try{await navigator.clipboard.writeText(text);window.showToast?.('AI reply copied.');}catch(_){window.showToast?.('Could not copy the reply.','warning',2800);}}
  function openReplyInMailApp(){
    const result=mailUI.replyResult||{},body=document.getElementById('mailReplyText')?.value||'';
    const to=String(result.to||'').trim(),subject=String(result.subject||'Re:');
    if(!to){window.showToast?.('No reply address is available for this message.','warning',3200);return;}
    if(body.length>6000){window.showToast?.('This draft is too long for a reliable mail-app handoff. Use Copy Reply instead.','warning',4200);return;}
    window.location.href=`mailto:${encodeURIComponent(to)}?subject=${encodeURIComponent(subject)}&body=${encodeURIComponent(body)}`;
  }

  async function openMail(){
    buildUI(); window.closeYetiMenu?.(); window.AgentGOMonitor?.close?.(); if(typeof window.hideWorkModeOverlay==='function')window.hideWorkModeOverlay();
    mailUI.open=true; mailUI.loadError=''; mailUI.loading=true; resetVisibleLimit(); mailUI.overlay.classList.add('open'); mailUI.overlay.setAttribute('aria-hidden','false'); window.syncDiagnosticsDesktopAvailability?.(); window.AgentGOYetiStars?.refresh?.();renderMessages();
    await loadState({toast:true,models:true});
    refreshNewMailCount(); startNewMailCountTimer();
  }
  function closeMail(){if(!mailUI.overlay)return;mailUI.open=false;mailUI.overlay.classList.remove('open');mailUI.overlay.setAttribute('aria-hidden','true');window.syncDiagnosticsDesktopAvailability?.();closeAccountModal();closeSettings();closeRescan();closeReanalyzeModal();closeReplyModal();if(mailUI.aiTimer){clearInterval(mailUI.aiTimer);mailUI.aiTimer=null;}if(mailUI.newMailCountTimer){clearInterval(mailUI.newMailCountTimer);mailUI.newMailCountTimer=null;}window.AgentGOYetiStars?.refresh?.();}

  function installYetiEntry(){document.addEventListener('click',event=>{if(event.target.closest('#yetiStartMailBtn')){event.preventDefault();openMail();}});}

  function init(){
    buildUI(); installYetiEntry();
    document.addEventListener('keydown',event=>{
      const tag=String(event.target?.tagName||'').toLowerCase(),typing=tag==='input'||tag==='textarea'||tag==='select'||event.target?.isContentEditable;
      const modalOpen=!!document.querySelector('.mail-modal.open');
      if(mailUI.open&&event.key==='/'&&!typing&&!modalOpen){event.preventDefault();document.getElementById('mailSearchInput')?.focus();return;}
      if(event.key!=='Escape')return;
      if(document.getElementById('mailReplyModal')?.classList.contains('open'))closeReplyModal();else if(document.getElementById('mailReanalyzeModal')?.classList.contains('open'))closeReanalyzeModal();else if(document.getElementById('mailAccountModal')?.classList.contains('open'))closeAccountModal();else if(document.getElementById('mailRescanModal')?.classList.contains('open'))closeRescan();else if(document.getElementById('mailSettingsModal')?.classList.contains('open'))closeSettings();else if(mailUI.open)closeMail();
    });
    window.AgentGOMail={open:openMail,close:closeMail,refresh:loadState};
  }

  if(document.readyState==='loading')document.addEventListener('DOMContentLoaded',init,{once:true});else init();
})();
