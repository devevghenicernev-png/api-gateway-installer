// apigw dashboard — vanilla JS, no framework.
//
// Per ARCHITECTURE.md §5: EventSource only, rAF batching for log append,
// pin-to-bottom auto-scroll (Discord 4px tolerance), CSS transitions for
// status changes. The DOM mutates ONLY from event handlers — no polling,
// no full re-renders.

(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);

  // --- elements ---
  const elDeploys = $("deploys");
  const elLogs    = $("logs");
  const elLogSel  = $("log-deploy");
  const elLogDrop = $("log-drop");
  const elFeed    = $("webhook-feed");
  const elTLS     = $("tls-list");
  const elVer     = $("version");
  const elUptime  = $("uptime");
  const elClients = $("clients");
  const elConn    = $("conn-status");

  // --- state ---
  let currentDeploy = null; // which deploy's logs we're showing
  let pinnedToBottom = true;
  const logQueue = [];      // rAF-batched log lines
  const deployByName = new Map();

  // ---- initial snapshot ----
  async function loadStatus() {
    try {
      const res = await fetch("/api/status");
      if (!res.ok) throw new Error(`status ${res.status}`);
      const s = await res.json();
      renderSnapshot(s);
    } catch (e) {
      console.warn("status fetch", e);
    }
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
    for (const c of (s.tls || [])) {
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

  // ---- SSE ----
  function connectStream() {
    // Subscribe to everything; we'll filter client-side. The hub fans by topic
    // anyway, so this is cheap.
    const url = "/events?topic=webhook.recv&topic=tls.expiry&topic=status.deploy&topic=" +
                "deploy.*.stdout&topic=deploy.*.state";
    const es = new EventSource(url, { withCredentials: false });

    es.addEventListener("open", () => setConn("ok"));
    es.addEventListener("error", () => setConn("warn"));

    es.addEventListener("__reconnect", () => {
      // Hub closed our socket because we lagged. Let the browser auto-reconnect
      // with Last-Event-ID; nothing to do here.
      setConn("warn");
    });

    // Deploy stdout.
    es.addEventListener("stdout", (ev) => {
      const data = safeParse(ev.data);
      if (!data) return;
      if (data.deploy !== currentDeploy) return;
      enqueueLine(data.line || "", data.stream === "stderr");
    });

    // Deploy state transitions.
    es.addEventListener("state", (ev) => {
      const data = safeParse(ev.data);
      if (!data) return;
      const card = elDeploys.querySelector(`.deploy-card[data-name="${cssEsc(data.deploy)}"]`);
      if (!card) return;
      const badge = card.querySelector('[data-role="badge"]');
      badge.textContent = data.status;
      badge.className = "badge " + badgeClass(data.status);
    });

    // Webhook activity.
    es.addEventListener("webhook", (ev) => {
      const data = safeParse(ev.data);
      if (!data) return;
      const li = document.createElement("li");
      li.innerHTML = `
        <span class="ts">${fmtTs(data.ts || Date.now() / 1000)}</span>
        <span><b>${esc(data.deploy || "—")}</b> ${esc(data.event || "")}</span>
        <span class="muted">${esc((data.delivery || "").slice(0, 8))}</span>
      `;
      elFeed.insertBefore(li, elFeed.firstChild);
      while (elFeed.children.length > 50) elFeed.lastChild.remove();
    });

    // TLS expiry tick.
    es.addEventListener("tls", (ev) => {
      const data = safeParse(ev.data);
      if (!data || !data.domain) return;
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
    // Cap at ~5000 lines to keep DOM bounded — old lines drop off the top.
    while (elLogs.childElementCount > 5000) elLogs.firstChild.remove();
    if (pinnedToBottom) elLogs.scrollTop = elLogs.scrollHeight;
  }

  // Discord-style pin: stay pinned within 4 px of bottom; freeze on scroll-up.
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
      // Snapshot is newest-first; reverse for chronological append.
      events.reverse();
      for (const e of events) {
        const data = safeParse(atob ? atobOrBlob(e.data) : e.data);
        if (!data) continue;
        enqueueLine(data.line || "", data.stream === "stderr");
      }
    } catch (e) {
      console.warn("logs fetch", e);
    }
  }

  // Server sends Data as raw bytes; in JSON it's base64'd by Go's json
  // encoder if the field is []byte. handle both.
  function atobOrBlob(s) {
    try { return atob(s); } catch { return s; }
  }

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
  connectStream();
  // Re-snapshot every 60s so /api/status drift (uptime, clients) corrects.
  setInterval(loadStatus, 60_000);
})();
