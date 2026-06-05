// apigw dashboard — vanilla JS, no framework.
//
// Per ARCHITECTURE.md §5: EventSource only for live updates, rAF batching
// for log append, pin-to-bottom auto-scroll (Discord 4px tolerance), CSS
// transitions for status changes.
//
// v2 adds the security panels (APIs, audit log, pending approvals) fed by
// /api/admin/* endpoints. Those require a bearer token; the user signs
// in via the topbar button and the token persists in localStorage.

(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);

  // ---- elements ----
  const elDeploys = $("deploys");
  const elLogs    = $("logs");
  const elLogSel  = $("log-deploy");
  const elFeed    = $("webhook-feed");
  const elFeedEmpty = $("webhook-empty");
  const elTLS     = $("tls-list");
  const elTLSEmpty = $("tls-empty");
  const elVer     = $("version");
  const elUptime  = $("uptime");
  const elClients = $("clients");
  const elConn    = $("conn-status");
  const elTokenBtn   = $("token-btn");
  const elTokenLabel = $("token-label");
  const elTokenModal = $("token-modal");
  const elTokenInput = $("token-input");
  const elApis        = $("apis-list");
  const elApisCount   = $("apis-count");
  const elApisEmpty   = $("apis-empty");
  const elAudit       = $("audit-list");
  const elAuditMeta   = $("audit-meta");
  const elAuditEmpty  = $("audit-empty");
  const elApprovals       = $("approvals-list");
  const elApprovalsCount  = $("approvals-count");
  const elApprovalsEmpty  = $("approvals-empty");

  // ---- state ----
  let currentDeploy = null;
  let pinnedToBottom = true;
  const logQueue = [];
  const deployByName = new Map();

  const TOKEN_KEY = "apigw.admin_token";
  let token = localStorage.getItem(TOKEN_KEY) || "";

  // ---- token / sign-in ----
  function updateTokenChip() {
    elTokenLabel.textContent = token ? "Sign out" : "Sign in";
    elTokenBtn.classList.toggle("authed", !!token);
  }
  updateTokenChip();

  elTokenBtn.addEventListener("click", () => {
    if (token) {
      token = "";
      localStorage.removeItem(TOKEN_KEY);
      updateTokenChip();
      // Clear protected panels.
      renderApis(null);
      renderAudit(null);
      renderApprovals(null);
      return;
    }
    elTokenInput.value = "";
    elTokenModal.hidden = false;
    setTimeout(() => elTokenInput.focus(), 50);
  });

  $("token-cancel").addEventListener("click", () => { elTokenModal.hidden = true; });
  $("token-save").addEventListener("click", () => {
    const v = elTokenInput.value.trim();
    if (!v) return;
    token = v;
    localStorage.setItem(TOKEN_KEY, token);
    elTokenModal.hidden = true;
    updateTokenChip();
    refreshAdmin();
  });
  elTokenInput.addEventListener("keydown", (e) => {
    if (e.key === "Enter") $("token-save").click();
    if (e.key === "Escape") $("token-cancel").click();
  });

  function authHeaders() {
    return token ? { "Authorization": `Bearer ${token}` } : {};
  }

  // ---- initial snapshot ----
  async function loadStatus() {
    try {
      const res = await fetch("/api/status");
      if (!res.ok) throw new Error(`status ${res.status}`);
      const s = await res.json();
      renderSnapshot(s);
    } catch (e) { console.warn("status fetch", e); }
  }

  function renderSnapshot(s) {
    elVer.textContent = `v${s.version}` + (s.commit && s.commit !== "none" ? ` (${s.commit.slice(0, 7)})` : "");
    elUptime.textContent = fmtDuration(s.uptime_sec);
    elClients.textContent = s.sse_clients;

    elDeploys.innerHTML = "";
    elLogSel.innerHTML = "";
    if (!s.deploys || s.deploys.length === 0) {
      elDeploys.innerHTML = `<div class="muted">No deployments registered.</div>`;
    }
    for (const d of (s.deploys || [])) {
      deployByName.set(d.name, d);
      elDeploys.appendChild(renderDeployCard(d));
      const opt = document.createElement("option");
      opt.value = d.name;
      opt.textContent = d.name;
      elLogSel.appendChild(opt);
    }
    if (s.deploys && s.deploys.length > 0 && !currentDeploy) {
      currentDeploy = s.deploys[0].name;
      elLogSel.value = currentDeploy;
      loadHistoricalLogs(currentDeploy);
    }

    elTLS.innerHTML = "";
    const certs = s.tls || [];
    elTLSEmpty.hidden = certs.length > 0;
    for (const c of certs) {
      elTLS.appendChild(renderTLS(c));
    }
  }

  function renderDeployCard(d) {
    const el = document.createElement("div");
    el.className = "deploy-card";
    el.dataset.name = d.name;
    const badgeCls = badgeClass(d.last_status);
    el.innerHTML = `
      <span class="deploy-name">${esc(d.name)}</span>
      <span class="deploy-meta">${esc(d.runtime || "—")} · ${esc(d.path || "—")} · ${shortSHA(d.last_sha)}</span>
      <span class="badge ${badgeCls}" data-role="badge">${esc(d.last_status || "idle")}</span>
    `;
    return el;
  }

  function renderTLS(c) {
    const days = c.days_left;
    let cls = "ok";
    if (days < 0) cls = "expired";
    else if (days < 30) cls = "warn";
    const li = document.createElement("li");
    li.dataset.domain = c.domain;
    li.innerHTML = `
      <span class="domain">${esc(c.domain)}</span>
      <span class="muted">${esc(c.strategy || "—")}</span>
      <span class="days ${cls}">${days < 0 ? `expired ${-days}d ago` : `${days}d left`}</span>
    `;
    return li;
  }

  function badgeClass(s) {
    switch (s) {
      case "ok": case "running": return "ok";
      case "building": case "queued": case "pending": return "building";
      case "failed": return "failed";
      default: return "idle";
    }
  }

  // ---- admin endpoints (require token) ----
  async function refreshAdmin() {
    if (!token) {
      renderApis(null);
      renderAudit(null);
      renderApprovals(null);
      return;
    }
    const [apis, audit, approvals] = await Promise.all([
      fetchAdmin("/api/admin/apis"),
      fetchAdmin("/api/admin/audit"),
      fetchAdmin("/api/admin/approvals?status=pending"),
    ]);
    renderApis(apis);
    renderAudit(audit);
    renderApprovals(approvals);
  }

  async function fetchAdmin(url) {
    try {
      const res = await fetch(url, { headers: authHeaders() });
      if (res.status === 401 || res.status === 403) {
        // Token rejected — wipe so user can re-enter.
        console.warn("admin endpoint rejected token", url, res.status);
        token = "";
        localStorage.removeItem(TOKEN_KEY);
        updateTokenChip();
        return null;
      }
      if (!res.ok) return null;
      return await res.json();
    } catch (e) {
      console.warn("admin fetch", url, e);
      return null;
    }
  }

  function renderApis(apis) {
    elApis.innerHTML = "";
    if (apis === null) {
      elApisEmpty.textContent = "Sign in to view configured APIs.";
      elApisEmpty.hidden = false;
      elApisCount.textContent = "—";
      return;
    }
    elApisCount.textContent = apis.length;
    if (apis.length === 0) {
      elApisEmpty.textContent = "No APIs configured.";
      elApisEmpty.hidden = false;
      return;
    }
    elApisEmpty.hidden = true;
    for (const a of apis) {
      const li = document.createElement("li");
      li.innerHTML = `
        <span class="api-name">${esc(a.Name)}</span>
        <span class="muted api-meta">${esc(a.Path || "/api/" + a.Name)} → :${a.Port}</span>
        <span class="badge ${a.Enabled ? "ok" : "idle"}">${a.Enabled ? "enabled" : "disabled"}</span>
      `;
      elApis.appendChild(li);
    }
  }

  function renderAudit(entries) {
    elAudit.innerHTML = "";
    if (entries === null) {
      elAuditEmpty.textContent = "Sign in to read the hash-chained audit log.";
      elAuditEmpty.hidden = false;
      elAuditMeta.textContent = "";
      return;
    }
    elAuditMeta.textContent = `${entries.length} ${entries.length === 1 ? "entry" : "entries"}`;
    if (entries.length === 0) {
      elAuditEmpty.textContent = "Audit log is empty.";
      elAuditEmpty.hidden = false;
      return;
    }
    elAuditEmpty.hidden = true;
    // newest first
    const ordered = entries.slice().reverse().slice(0, 80);
    for (const e of ordered) {
      const li = document.createElement("li");
      li.className = "audit-row " + resultClass(e.result);
      li.innerHTML = `
        <span class="audit-ts">${fmtTsISO(e.timestamp)}</span>
        <span class="audit-actor">${esc(e.actor || "—")}</span>
        <span class="audit-action">${esc(e.action || "")}</span>
        <span class="audit-resource muted">${esc(e.resource || "")}</span>
        <span class="badge ${resultClass(e.result)}">${esc(e.result || "")}</span>
      `;
      elAudit.appendChild(li);
    }
  }

  function renderApprovals(items) {
    elApprovals.innerHTML = "";
    if (items === null) {
      elApprovalsEmpty.textContent = "Sign in to see pending approvals.";
      elApprovalsEmpty.hidden = false;
      elApprovalsCount.textContent = "—";
      return;
    }
    elApprovalsCount.textContent = items.length;
    if (items.length === 0) {
      elApprovalsEmpty.textContent = "Nothing waiting on approval.";
      elApprovalsEmpty.hidden = false;
      return;
    }
    elApprovalsEmpty.hidden = true;
    for (const a of items) {
      const li = document.createElement("li");
      li.className = "approval-row";
      const have = (a.approvals || []).length;
      li.innerHTML = `
        <div class="approval-head">
          <span class="approval-action"><b>${esc(a.action)}</b> on ${esc(a.resource)}</span>
          <span class="badge building">${have}/${a.threshold} approvals</span>
        </div>
        <div class="approval-meta muted">
          submitted by ${esc(a.submitted_by)} · ${fmtTsISO(a.submitted_at)} · expires ${fmtTsISO(a.expires_at)}
        </div>
        <div class="approval-id muted">id: <code>${esc(a.id)}</code></div>
      `;
      elApprovals.appendChild(li);
    }
  }

  function resultClass(r) {
    if (r === "ok") return "ok";
    if (r === "denied" || r === "failed") return "failed";
    if (r === "parked") return "building";
    return "idle";
  }

  // ---- SSE ----
  function connectStream() {
    const url = "/events?topic=webhook.recv&topic=tls.expiry&topic=status.deploy&topic=" +
                "deploy.*.stdout&topic=deploy.*.state";
    const es = new EventSource(url, { withCredentials: false });

    es.addEventListener("open", () => setConn("ok"));
    es.addEventListener("error", () => setConn("warn"));
    es.addEventListener("__reconnect", () => setConn("warn"));

    es.addEventListener("stdout", (ev) => {
      const data = safeParse(ev.data);
      if (!data) return;
      if (data.deploy !== currentDeploy) return;
      enqueueLine(data.line || "", data.stream === "stderr");
    });

    es.addEventListener("state", (ev) => {
      const data = safeParse(ev.data);
      if (!data) return;
      const card = elDeploys.querySelector(`.deploy-card[data-name="${cssEsc(data.deploy)}"]`);
      if (!card) return;
      const badge = card.querySelector('[data-role="badge"]');
      badge.textContent = data.status;
      badge.className = "badge " + badgeClass(data.status);
    });

    es.addEventListener("webhook", (ev) => {
      const data = safeParse(ev.data);
      if (!data) return;
      elFeedEmpty.hidden = true;
      const li = document.createElement("li");
      li.innerHTML = `
        <span class="ts">${fmtTs(data.ts || Date.now() / 1000)}</span>
        <span><b>${esc(data.deploy || "—")}</b> ${esc(data.event || "")}</span>
        <span class="muted">${esc((data.delivery || "").slice(0, 8))}</span>
      `;
      elFeed.insertBefore(li, elFeed.firstChild);
      while (elFeed.children.length > 50) elFeed.lastChild.remove();
    });

    es.addEventListener("tls", (ev) => {
      const data = safeParse(ev.data);
      if (!data || !data.domain) return;
      elTLSEmpty.hidden = true;
      const existing = elTLS.querySelector(`li[data-domain="${cssEsc(data.domain)}"]`);
      const node = renderTLS(data);
      if (existing) existing.replaceWith(node);
      else elTLS.appendChild(node);
    });
  }

  function setConn(state) {
    elConn.classList.remove("dead", "warn");
    if (state === "warn") elConn.classList.add("warn");
    else if (state === "dead") elConn.classList.add("dead");
  }

  // ---- log batching ----
  function enqueueLine(line, isErr) {
    logQueue.push({ line, isErr });
    if (logQueue.length === 1) requestAnimationFrame(flushLogs);
  }

  function flushLogs() {
    const frag = document.createDocumentFragment();
    for (const { line, isErr } of logQueue) {
      const span = document.createElement("span");
      span.className = isErr ? "line err" : "line";
      span.textContent = line + "\n";
      frag.appendChild(span);
    }
    elLogs.appendChild(frag);
    logQueue.length = 0;
    while (elLogs.childElementCount > 5000) elLogs.firstChild.remove();
    if (pinnedToBottom) elLogs.scrollTop = elLogs.scrollHeight;
  }

  elLogs.addEventListener("scroll", () => {
    const slack = 4;
    pinnedToBottom = (elLogs.scrollHeight - elLogs.scrollTop - elLogs.clientHeight) < slack;
  });

  elLogSel.addEventListener("change", () => {
    currentDeploy = elLogSel.value;
    elLogs.innerHTML = "";
    pinnedToBottom = true;
    loadHistoricalLogs(currentDeploy);
  });

  async function loadHistoricalLogs(name) {
    if (!name) return;
    try {
      const res = await fetch(`/api/logs/${encodeURIComponent(name)}?lines=200`);
      if (!res.ok) return;
      const events = await res.json();
      events.reverse();
      for (const e of events) {
        const data = safeParse(atobOrBlob(e.data));
        if (!data) continue;
        enqueueLine(data.line || "", data.stream === "stderr");
      }
    } catch (e) { console.warn("logs fetch", e); }
  }

  function atobOrBlob(s) { try { return atob(s); } catch { return s; } }

  // ---- uptime ticker ----
  setInterval(() => {
    const cur = parseDuration(elUptime.textContent);
    if (!isNaN(cur)) elUptime.textContent = fmtDuration(cur + 1);
  }, 1000);

  // ---- utils ----
  function esc(s) {
    return String(s == null ? "" : s)
      .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;").replace(/'/g, "&#39;");
  }
  function cssEsc(s) { return String(s).replace(/"/g, '\\"'); }
  function safeParse(s) { try { return JSON.parse(s); } catch { return null; } }
  function shortSHA(s) { return s ? s.slice(0, 7) : "—"; }
  function fmtTs(unixSec) {
    const d = new Date(unixSec * 1000);
    return d.toLocaleTimeString();
  }
  function fmtTsISO(iso) {
    if (!iso) return "—";
    const d = new Date(iso);
    if (isNaN(d.getTime())) return iso;
    return d.toLocaleTimeString();
  }
  function fmtDuration(sec) {
    sec = Math.max(0, Math.floor(sec));
    const h = Math.floor(sec / 3600);
    const m = Math.floor((sec % 3600) / 60);
    const s = sec % 60;
    if (h > 0) return `${h}h ${m}m ${s}s`;
    if (m > 0) return `${m}m ${s}s`;
    return `${s}s`;
  }
  function parseDuration(text) {
    const m = /(?:(\d+)h\s*)?(?:(\d+)m\s*)?(\d+)s/.exec(text || "");
    if (!m) return NaN;
    return (+m[1] || 0) * 3600 + (+m[2] || 0) * 60 + (+m[3] || 0);
  }

  // boot
  loadStatus();
  refreshAdmin();
  connectStream();
  setInterval(loadStatus, 60_000);
  setInterval(refreshAdmin, 10_000);
})();
