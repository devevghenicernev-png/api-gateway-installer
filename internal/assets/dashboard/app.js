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
  // localStorage can throw in Safari Private Browsing or when the user
  // has disabled site data. We fall back to a session-only in-memory
  // token rather than crashing; the operator will need to re-sign-in
  // on full reload but at least the dashboard works.
  function safeLocalGet(key) {
    try { return localStorage.getItem(key) || ""; } catch { return ""; }
  }
  function safeLocalSet(key, val) {
    try { localStorage.setItem(key, val); } catch { /* ignore */ }
  }
  function safeLocalDel(key) {
    try { localStorage.removeItem(key); } catch { /* ignore */ }
  }
  let token = safeLocalGet(TOKEN_KEY);

  // ---- toast notifications ----
  // Replaces native alert() — non-blocking, dismissible, screen-reader
  // friendly via the aria-live region. Categories: "info" (default),
  // "ok" (success), "warn" (degraded), "err" (failed).
  const elToastStack = $("toast-stack");
  function toast(message, kind, opts) {
    const k = kind || "info";
    const timeout = (opts && opts.timeout) || (k === "err" ? 8000 : 4500);
    const el = document.createElement("div");
    el.className = "toast toast-" + k;
    el.setAttribute("role", k === "err" ? "alert" : "status");
    el.textContent = message;
    const close = document.createElement("button");
    close.className = "toast-close";
    close.setAttribute("aria-label", "Dismiss notification");
    close.textContent = "×";
    close.addEventListener("click", () => dismiss());
    el.appendChild(close);
    elToastStack.appendChild(el);
    const t = setTimeout(dismiss, timeout);
    function dismiss() {
      clearTimeout(t);
      el.classList.add("leaving");
      setTimeout(() => el.remove(), 200);
    }
  }

  // Audit filter state — kept in JS so the user's last query survives the
  // 10s refresh. We render-time-filter; backend always returns the full
  // (post-rbac) slice it's authorised to expose.
  let lastAuditEntries = [];
  const auditFilter = { actor: "", action: "", result: "" };

  // ---- token / sign-in ----
  function updateTokenChip() {
    elTokenLabel.textContent = token ? "Sign out" : "Sign in";
    elTokenBtn.classList.toggle("authed", !!token);
  }
  updateTokenChip();

  let modalOpenedFrom = null;
  elTokenBtn.addEventListener("click", () => {
    if (token) {
      token = "";
      safeLocalDel(TOKEN_KEY);
      updateTokenChip();
      // Clear protected panels.
      renderApis(null);
      renderAudit(null);
      renderApprovals(null);
      return;
    }
    elTokenInput.value = "";
    modalOpenedFrom = document.activeElement;
    elTokenModal.hidden = false;
    setTimeout(() => elTokenInput.focus(), 50);
  });

  // Modal focus trap + restore: keep Tab cycling between the two
  // buttons + input, and restore focus to the caller on close so
  // keyboard navigation doesn't dump the user back at the body.
  function closeModal() {
    elTokenModal.hidden = true;
    if (modalOpenedFrom && typeof modalOpenedFrom.focus === "function") {
      modalOpenedFrom.focus();
    }
    modalOpenedFrom = null;
  }
  elTokenModal.addEventListener("keydown", (e) => {
    if (e.key === "Tab") {
      const focusables = elTokenModal.querySelectorAll("input, button");
      if (focusables.length === 0) return;
      const first = focusables[0];
      const last = focusables[focusables.length - 1];
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    }
  });
  // Background click dismisses (common pattern, less surprising than
  // "click anywhere does nothing").
  elTokenModal.addEventListener("click", (e) => {
    if (e.target === elTokenModal) closeModal();
  });

  $("token-cancel").addEventListener("click", () => { closeModal(); });
  $("token-save").addEventListener("click", () => {
    const v = elTokenInput.value.trim();
    if (!v) return;
    token = v;
    safeLocalSet(TOKEN_KEY, token);
    closeModal();
    updateTokenChip();
    refreshAdmin();
  });
  elTokenInput.addEventListener("keydown", (e) => {
    if (e.key === "Enter") $("token-save").click();
    if (e.key === "Escape") $("token-cancel").click();
  });

  // ---- panel maximize ----
  // Click ⤢ on any panel header → that panel takes over the whole grid;
  // click again or press Esc → restore the 3×3 layout. State is kept on
  // <main> via a data-attribute so CSS can do all the visual work.
  const elMain = document.querySelector("main");
  document.querySelectorAll(".panel-max").forEach((btn) => {
    btn.addEventListener("click", (e) => {
      e.stopPropagation();
      const panel = btn.closest(".panel");
      const id = panel.dataset.panel;
      if (elMain.dataset.maxed === id) {
        elMain.removeAttribute("data-maxed");
      } else {
        elMain.dataset.maxed = id;
      }
    });
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && elMain.dataset.maxed) {
      elMain.removeAttribute("data-maxed");
    }
  });

  // ---- audit filters ----
  const elFilterActor  = $("filter-actor");
  const elFilterAction = $("filter-action");
  const elFilterResult = $("filter-result");
  const elFilterClear  = $("filter-clear");
  function applyAuditFilters() {
    auditFilter.actor  = elFilterActor.value.trim().toLowerCase();
    auditFilter.action = elFilterAction.value.trim().toLowerCase();
    auditFilter.result = elFilterResult.value;
    renderAudit(lastAuditEntries);
  }
  elFilterActor.addEventListener("input",  applyAuditFilters);
  elFilterAction.addEventListener("input", applyAuditFilters);
  elFilterResult.addEventListener("change", applyAuditFilters);
  elFilterClear.addEventListener("click", () => {
    elFilterActor.value = "";
    elFilterAction.value = "";
    elFilterResult.value = "";
    applyAuditFilters();
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
    // Avoid double-v: goreleaser tags v0.1.0; build-time -X may or may
    // not include the leading 'v'. Normalise so we never render 'vv'.
    const ver = String(s.version || "").replace(/^v+/, "");
    elVer.textContent = `v${ver}` + (s.commit && s.commit !== "none" ? ` (${s.commit.slice(0, 7)})` : "");
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

    // D-2a: show the SSO sign-in button only when security.sso is wired
    // up server-side, so a stock install doesn't dangle a button that 502s.
    const ssoBtn = document.getElementById("sso-btn");
    if (ssoBtn) ssoBtn.hidden = !s.sso_enabled;
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
      <button class="card-btn" data-role="redeploy" title="Redeploy ${esc(d.name)}" aria-label="Redeploy ${esc(d.name)}">↻</button>
      <button class="card-btn" data-role="rollback" title="Rollback ${esc(d.name)} to previous release" aria-label="Rollback ${esc(d.name)}">⤺</button>
      <button class="card-btn" data-role="rotate" title="Rotate webhook secret for ${esc(d.name)}" aria-label="Rotate webhook secret">🔑</button>
      <button class="card-btn danger" data-role="delete" title="Delete ${esc(d.name)}" aria-label="Delete ${esc(d.name)}">🗑</button>
    `;
    el.querySelector('[data-role="redeploy"]').addEventListener("click", (e) => {
      e.stopPropagation();
      redeploy(d.name, el);
    });
    el.querySelector('[data-role="rollback"]').addEventListener("click", (e) => {
      e.stopPropagation();
      rollbackDeploy(d.name, el);
    });
    el.querySelector('[data-role="rotate"]').addEventListener("click", (e) => {
      e.stopPropagation();
      rotateWebhook(d.name, el);
    });
    el.querySelector('[data-role="delete"]').addEventListener("click", (e) => {
      e.stopPropagation();
      deleteResource("deploy", d.name, el);
    });
    return el;
  }

  // rollbackDeploy posts /api/admin/deploy-rollback/<name>. Confirmation
  // dialog because it's a destructive-feeling op (though it just flips
  // a symlink). The Dangerous=true on the server gates this behind
  // approvals when threshold > 0.
  async function rollbackDeploy(name, card) {
    if (!token) { toast("Sign in first.", "warn"); return; }
    const ok = await confirmModal({
      title: "Roll back deploy",
      body: `Roll back ${name} to the previous release?`,
      ok: "Roll back",
      danger: true,
    });
    if (!ok) return;
    const btn = card.querySelector('[data-role="rollback"]');
    btn.disabled = true;
    btn.textContent = "…";
    try {
      const csrfRes = await fetch("/api/admin/csrf", { headers: authHeaders() });
      const csrf = (await csrfRes.json()).token;
      const res = await fetch(`/api/admin/deploy-rollback/${encodeURIComponent(name)}`, {
        method: "POST",
        headers: { ...authHeaders(), "X-CSRF-Token": csrf },
      });
      const body = await res.json().catch(() => ({}));
      if (res.status === 202) {
        toast(`Rolled back ${name}.`, "ok");
      } else if (res.status === 202 || res.status === 200) {
        // 202 also signals approval-parked.
        toast(body.error ? body.error : `Queued rollback for ${name}.`, "info", { timeout: 9000 });
      } else {
        toast(`Rollback refused (HTTP ${res.status}): ${body.error || ""}`, "err");
      }
      refreshAdmin();
    } catch (e) {
      toast("Rollback failed: " + e.message, "err");
    } finally {
      btn.disabled = false;
      btn.textContent = "⤺";
    }
  }

  // rotateWebhook posts /api/admin/webhook-rotate/<name>. The new
  // secret is shown ONCE — operator must paste it into GitHub before
  // dismissing the toast. We render the toast at extended timeout +
  // pin it so an accidental refresh doesn't wipe it.
  async function rotateWebhook(name, card) {
    if (!token) { toast("Sign in first.", "warn"); return; }
    const ok = await confirmModal({
      title: "Rotate webhook secret",
      body: `Rotate the webhook secret for ${name}?\n\nThe previous secret keeps working for 5 minutes (grace window) so in-flight GitHub deliveries don't fail. Update GitHub's webhook UI within that window.`,
      ok: "Rotate",
      danger: true,
    });
    if (!ok) return;
    const btn = card.querySelector('[data-role="rotate"]');
    btn.disabled = true;
    btn.textContent = "…";
    try {
      const csrfRes = await fetch("/api/admin/csrf", { headers: authHeaders() });
      const csrf = (await csrfRes.json()).token;
      const res = await fetch(`/api/admin/webhook-rotate/${encodeURIComponent(name)}`, {
        method: "POST",
        headers: { ...authHeaders(), "X-CSRF-Token": csrf },
      });
      const body = await res.json().catch(() => ({}));
      if (res.status === 200 && body.new_secret) {
        // Long-timeout, copyable. The secret won't be shown again.
        toast(`New webhook secret for ${name} — copy now: ${body.new_secret}`, "warn", { timeout: 60000 });
      } else if (res.status === 202) {
        toast(body.error || "Change parked for approvals.", "info", { timeout: 9000 });
      } else {
        toast(`Rotate refused (HTTP ${res.status}): ${body.error || ""}`, "err");
      }
    } catch (e) {
      toast("Rotate failed: " + e.message, "err");
    } finally {
      btn.disabled = false;
      btn.textContent = "🔑";
    }
  }

  // redeploy posts to /api/admin/deploy-run/<name>. Success → switch the
  // log selector to that deploy so the operator sees the build stream
  // immediately. The badge will flip to "queued" → "building" → "ok|failed"
  // through the SSE `state` events the worker emits.
  async function redeploy(name, card) {
    if (!token) {
      alert("Sign in first.");
      return;
    }
    const btn = card.querySelector('[data-role="redeploy"]');
    btn.disabled = true;
    btn.textContent = "…";
    try {
      const csrfRes = await fetch("/api/admin/csrf", { headers: authHeaders() });
      const csrf = (await csrfRes.json()).token;
      const res = await fetch(`/api/admin/deploy-run/${encodeURIComponent(name)}`, {
        method: "POST",
        headers: { ...authHeaders(), "X-CSRF-Token": csrf },
      });
      if (!res.ok) {
        const body = await res.json().catch(() => ({}));
        toast(`Redeploy refused (HTTP ${res.status}): ${body.error || ""}`, "err");
        return;
      }
      // Switch log panel to this deploy so the build stream is visible.
      if (elLogSel.querySelector(`option[value="${cssEsc(name)}"]`)) {
        elLogSel.value = name;
        currentDeploy = name;
        elLogs.innerHTML = "";
        pinnedToBottom = true;
        loadHistoricalLogs(name);
      }
      // Flip badge to queued immediately for snappy feedback; the real
      // state will follow via SSE.
      const badge = card.querySelector('[data-role="badge"]');
      badge.textContent = "queued";
      badge.className = "badge building";
    } catch (e) {
      toast("Redeploy failed: " + e.message, "err");
    } finally {
      btn.disabled = false;
      btn.textContent = "↻";
    }
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
      <button class="card-btn" data-role="renew" title="Renew ${esc(c.domain)}" aria-label="Renew certificate for ${esc(c.domain)}">↻</button>
    `;
    li.querySelector('[data-role="renew"]').addEventListener("click", (e) => {
      e.stopPropagation();
      renewCert(c.domain, li);
    });
    return li;
  }

  // renewCert posts to /api/admin/tls-renew/<domain>. The renewal runs
  // asynchronously server-side; we show a toast immediately and let the
  // operator watch the panel for the new expiry date on the next status
  // refresh (60s, or sooner if they click again).
  async function renewCert(domain, rowEl) {
    if (!token) { toast("Sign in first.", "warn"); return; }
    const btn = rowEl.querySelector('[data-role="renew"]');
    btn.disabled = true;
    btn.textContent = "…";
    try {
      const csrfRes = await fetch("/api/admin/csrf", { headers: authHeaders() });
      const csrf = (await csrfRes.json()).token;
      const res = await fetch(`/api/admin/tls-renew/${encodeURIComponent(domain)}`, {
        method: "POST",
        headers: { ...authHeaders(), "X-CSRF-Token": csrf },
      });
      const body = await res.json().catch(() => ({}));
      if (res.status === 202) {
        toast(`Renewal queued for ${domain}. Check back in ~30s.`, "ok");
      } else {
        toast(`Renew refused (HTTP ${res.status}): ${body.error || ""}`, "err");
      }
    } catch (e) {
      toast("Renew failed: " + e.message, "err");
    } finally {
      btn.disabled = false;
      btn.textContent = "↻";
    }
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
    // Mark the three admin panels as loading so the previous render dims
    // (CSS .loading rule) instead of flashing to empty for ~500ms. The
    // class is removed on each render call below.
    setLoading(["apis-list", "audit-list", "approvals-list"], true);
    const [apis, audit, approvals] = await Promise.all([
      fetchAdmin("/api/admin/apis"),
      fetchAdmin("/api/admin/audit"),
      fetchAdmin("/api/admin/approvals?status=pending"),
    ]);
    renderApis(apis);
    renderAudit(audit);
    renderApprovals(approvals);
    setLoading(["apis-list", "audit-list", "approvals-list"], false);
  }

  // setLoading toggles the .loading CSS class on a list of element IDs.
  // Used to dim panels during refreshAdmin's network round-trip so the
  // previous content fades to ~50% opacity instead of being nuked first.
  function setLoading(ids, on) {
    for (const id of ids) {
      const el = document.getElementById(id);
      if (!el) continue;
      const parent = el.closest(".card") || el;
      parent.classList.toggle("loading", !!on);
    }
  }

  // confirmModal replaces window.confirm() so destructive actions show a
  // styled dialog instead of the browser-native popup (which can be
  // suppressed per-site by users, and doesn't fit our visual language).
  // Returns a Promise<boolean>: resolves true on OK, false on Cancel,
  // Escape, or backdrop click. Body accepts plain text — newlines are
  // preserved via white-space: pre-line CSS on #confirm-modal-body.
  function confirmModal({ title = "Confirm", body = "", ok = "OK", danger = false } = {}) {
    return new Promise((resolve) => {
      const bg = document.getElementById("confirm-modal");
      const titleEl = document.getElementById("confirm-modal-title");
      const bodyEl = document.getElementById("confirm-modal-body");
      const okBtn = document.getElementById("confirm-modal-ok");
      const cancelBtn = document.getElementById("confirm-modal-cancel");
      if (!bg) {
        // Fallback if the markup was stripped — keep the action working.
        resolve(window.confirm(body));
        return;
      }
      titleEl.textContent = title;
      bodyEl.textContent = body;
      okBtn.textContent = ok;
      okBtn.classList.toggle("danger", !!danger);
      bg.hidden = false;

      const cleanup = (result) => {
        bg.hidden = true;
        okBtn.removeEventListener("click", onOk);
        cancelBtn.removeEventListener("click", onCancel);
        bg.removeEventListener("click", onBackdrop);
        document.removeEventListener("keydown", onKey);
        okBtn.classList.remove("danger");
        resolve(result);
      };
      const onOk = () => cleanup(true);
      const onCancel = () => cleanup(false);
      const onBackdrop = (e) => { if (e.target === bg) cleanup(false); };
      const onKey = (e) => {
        if (e.key === "Escape") cleanup(false);
        if (e.key === "Enter") cleanup(true);
      };
      okBtn.addEventListener("click", onOk);
      cancelBtn.addEventListener("click", onCancel);
      bg.addEventListener("click", onBackdrop);
      document.addEventListener("keydown", onKey);
      okBtn.focus();
    });
  }

  async function fetchAdmin(url) {
    try {
      const res = await fetch(url, { headers: authHeaders() });
      if (res.status === 401 || res.status === 403) {
        // Token rejected — wipe so user can re-enter.
        console.warn("admin endpoint rejected token", url, res.status);
        if (token) toast("Your token was rejected — please sign in again.", "warn");
        token = "";
        safeLocalDel(TOKEN_KEY);
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
      li.dataset.name = a.Name;
      const enabled = !!a.Enabled;
      li.innerHTML = `
        <span class="api-name">${esc(a.Name)}</span>
        <span class="muted api-meta">${esc(a.Path || "/api/" + a.Name)} → :${a.Port}</span>
        <span class="badge ${enabled ? "ok" : "idle"}">${enabled ? "enabled" : "disabled"}</span>
        <button class="card-btn" data-role="toggle" title="${enabled ? "Disable" : "Enable"} ${esc(a.Name)}" aria-label="${enabled ? "Disable" : "Enable"} ${esc(a.Name)}">${enabled ? "⏸" : "▶"}</button>
        <button class="card-btn danger" data-role="delete" title="Delete ${esc(a.Name)}" aria-label="Delete ${esc(a.Name)}">🗑</button>
      `;
      li.querySelector('[data-role="toggle"]').addEventListener("click", (e) => {
        e.stopPropagation();
        toggleAPI(a, li);
      });
      li.querySelector('[data-role="delete"]').addEventListener("click", (e) => {
        e.stopPropagation();
        deleteResource("api", a.Name, li);
      });
      elApis.appendChild(li);
    }
  }

  // toggleAPI flips api.Enabled by PUT-ing the whole resource back —
  // the admin PUT handler treats it as a replace (less surface than a
  // dedicated /enable endpoint). The shape we send mirrors what we got.
  async function toggleAPI(api, rowEl) {
    if (!token) { toast("Sign in first.", "warn"); return; }
    const btn = rowEl.querySelector('[data-role="toggle"]');
    btn.disabled = true;
    try {
      const csrfRes = await fetch("/api/admin/csrf", { headers: authHeaders() });
      const csrf = (await csrfRes.json()).token;
      const next = { ...api, Enabled: !api.Enabled };
      const res = await fetch(`/api/admin/apis/${encodeURIComponent(api.Name)}`, {
        method: "PUT",
        headers: { ...authHeaders(), "Content-Type": "application/json", "X-CSRF-Token": csrf },
        body: JSON.stringify(next),
      });
      if (!res.ok) {
        const body = await res.json().catch(() => ({}));
        toast(`Toggle refused (HTTP ${res.status}): ${body.error || ""}`, "err");
        return;
      }
      toast(`${api.Name} ${next.Enabled ? "enabled" : "disabled"}.`, "ok");
      refreshAdmin();
    } catch (e) {
      toast("Toggle failed: " + e.message, "err");
    } finally {
      btn.disabled = false;
    }
  }

  // deleteResource is the shared DELETE path for APIs and deploys. It
  // confirms with the user (destructive), POSTs DELETE with CSRF, and
  // refreshes the relevant panel on success. On 202 (approvals parked)
  // it surfaces the change ID so reviewers can find it.
  async function deleteResource(kind, name, rowEl) {
    if (!token) { toast("Sign in first.", "warn"); return; }
    const ok = await confirmModal({
      title: `Delete ${kind}`,
      body: `Delete ${kind} "${name}"?\n\nThis will remove its nginx config; if approvals are required, the change will be parked instead of applied.`,
      ok: "Delete",
      danger: true,
    });
    if (!ok) return;
    const btn = rowEl.querySelector('[data-role="delete"]');
    btn.disabled = true;
    btn.textContent = "…";
    try {
      const csrfRes = await fetch("/api/admin/csrf", { headers: authHeaders() });
      const csrf = (await csrfRes.json()).token;
      const path = kind === "api" ? "/api/admin/apis/" : "/api/admin/deploys/";
      const res = await fetch(path + encodeURIComponent(name), {
        method: "DELETE",
        headers: { ...authHeaders(), "X-CSRF-Token": csrf },
      });
      if (res.status === 204) {
        // Real delete — animate out + refresh.
        rowEl.classList.add("removing");
        setTimeout(refreshAdmin, 200);
        return;
      }
      if (res.status === 202) {
        const body = await res.json().catch(() => ({}));
        toast(`Change parked for approvals — id: ${body.id || "(see /api/admin/approvals)"}`, "info", { timeout: 9000 });
        refreshAdmin();
        return;
      }
      const body = await res.json().catch(() => ({}));
      toast(`Delete refused (HTTP ${res.status}): ${body.error || ""}`, "err");
    } catch (e) {
      toast("Delete failed: " + e.message, "err");
    } finally {
      btn.disabled = false;
      btn.textContent = "🗑";
    }
  }

  function renderAudit(entries) {
    if (entries === null) {
      // Unauth state — wipe and bail (no preservation needed).
      elAudit.innerHTML = "";
      lastAuditEntries = [];
      elAuditEmpty.textContent = "Sign in to read the hash-chained audit log.";
      elAuditEmpty.hidden = false;
      elAuditMeta.textContent = "";
      return;
    }
    lastAuditEntries = entries;
    const filtered = entries.filter((e) => {
      if (auditFilter.actor && !(e.actor || "").toLowerCase().includes(auditFilter.actor)) return false;
      if (auditFilter.action && !(e.action || "").toLowerCase().includes(auditFilter.action)) return false;
      if (auditFilter.result && e.result !== auditFilter.result) return false;
      return true;
    });
    const total = entries.length;
    const shown = filtered.length;
    const isFiltered = shown !== total;
    elAuditMeta.textContent = isFiltered
      ? `${shown} of ${total} ${total === 1 ? "entry" : "entries"}`
      : `${total} ${total === 1 ? "entry" : "entries"}`;
    if (total === 0) {
      elAudit.innerHTML = "";
      elAuditEmpty.textContent = "Audit log is empty.";
      elAuditEmpty.hidden = false;
      return;
    }
    if (shown === 0) {
      elAudit.innerHTML = "";
      elAuditEmpty.textContent = "No entries match the current filter.";
      elAuditEmpty.hidden = false;
      return;
    }
    elAuditEmpty.hidden = true;

    // Key-based DOM diff so the 10-second poll doesn't blow away expanded
    // detail rows or reset scroll position. Audit entries are hash-chained
    // and immutable once written — same id ⇒ identical content, so we can
    // keep the existing <li> in place.
    //
    // Algorithm: scan existing children → {key: row, detail?}; walk the new
    // ordered list creating only missing rows + moving them into position;
    // remove any rows whose keys aren't in the new list. Their attached
    // detail-li (created lazily by toggleAuditDetail) follows the parent.
    const ordered = filtered.slice().reverse().slice(0, 200);
    const wantKeys = new Set(ordered.map((e) => String(e.id)));
    const existing = new Map();
    for (let n = elAudit.firstElementChild; n; ) {
      const next = n.nextElementSibling;
      if (n.classList.contains("audit-row")) {
        const k = n.dataset.key;
        if (k && wantKeys.has(k)) {
          existing.set(k, n);
        } else {
          // Stale: drop the row AND any expanded detail-li immediately
          // following it (toggleAuditDetail inserts detail as next sibling).
          if (next && next.classList.contains("audit-detail")) {
            next.remove();
          }
          n.remove();
        }
      }
      n = next;
    }
    // Walk the new ordered list, placing rows in order. Existing rows are
    // moved (not replaced) via appendChild so any attached detail row
    // stays alongside. New rows get freshly constructed.
    let cursor = elAudit.firstElementChild;
    for (const e of ordered) {
      const key = String(e.id);
      let li = existing.get(key);
      if (!li) {
        li = document.createElement("li");
        li.className = "audit-row " + resultClass(e.result);
        li.dataset.key = key;
        li.innerHTML = `
          <span class="audit-ts">${fmtTsISO(e.timestamp)}</span>
          <span class="audit-actor">${esc(e.actor || "—")}</span>
          <span class="audit-action">${esc(e.action || "")}</span>
          <span class="audit-resource muted">${esc(e.resource || "")}</span>
          <span class="badge ${resultClass(e.result)}">${esc(e.result || "")}</span>
        `;
        li.addEventListener("click", (ev) => {
          if (ev.target.closest(".badge")) return;
          toggleAuditDetail(li, e);
        });
      }
      if (cursor !== li) {
        elAudit.insertBefore(li, cursor);
      } else {
        cursor = li.nextElementSibling;
        // Skip over an attached detail row so we don't try to position
        // the next audit-row before it.
        if (cursor && cursor.classList.contains("audit-detail")) {
          cursor = cursor.nextElementSibling;
        }
      }
    }
  }

  // toggleAuditDetail injects/removes a sibling <li> with the full Entry
  // payload (reason, before/after JSON, hash chain). Reading audit details
  // is itself an audited action when AuditReads is on, so we keep it click-
  // gated rather than always-rendered.
  function toggleAuditDetail(li, entry) {
    const next = li.nextElementSibling;
    if (next && next.classList.contains("audit-detail")) {
      next.remove();
      li.classList.remove("expanded");
      return;
    }
    li.classList.add("expanded");
    const det = document.createElement("li");
    det.className = "audit-detail";
    const beforeStr = entry.before ? JSON.stringify(entry.before, null, 2) : null;
    const afterStr  = entry.after  ? JSON.stringify(entry.after,  null, 2) : null;
    det.innerHTML = `
      <div class="audit-detail-grid">
        <div><span class="muted">id</span><code>${esc(String(entry.id))}</code></div>
        <div><span class="muted">timestamp</span><code>${esc(entry.timestamp || "")}</code></div>
        <div><span class="muted">actor</span><code>${esc(entry.actor || "")}</code></div>
        <div><span class="muted">action</span><code>${esc(entry.action || "")}</code></div>
        <div><span class="muted">resource</span><code>${esc(entry.resource || "—")}</code></div>
        <div><span class="muted">result</span><code>${esc(entry.result || "")}</code></div>
        ${entry.reason ? `<div class="span2"><span class="muted">reason</span><code>${esc(entry.reason)}</code></div>` : ""}
        <div class="span2"><span class="muted">prev_hash</span><code class="mono-small">${esc(entry.prev_hash || "(genesis)")}</code></div>
        <div class="span2"><span class="muted">hash</span><code class="mono-small">${esc(entry.hash || "")}</code></div>
        ${beforeStr ? `<div class="span2"><span class="muted">before</span><pre class="json">${esc(beforeStr)}</pre></div>` : ""}
        ${afterStr  ? `<div class="span2"><span class="muted">after</span><pre class="json">${esc(afterStr)}</pre></div>` : ""}
      </div>
    `;
    li.insertAdjacentElement("afterend", det);
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
      li.dataset.id = a.id;
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
        <div class="approval-actions">
          <button class="btn-approve" data-role="approve">✓ Approve</button>
          <button class="btn-reject"  data-role="reject">✗ Reject</button>
        </div>
      `;
      li.querySelector('[data-role="approve"]').addEventListener("click", (e) => {
        e.stopPropagation();
        approvalAct("approve", a.id, li, a);
      });
      li.querySelector('[data-role="reject"]').addEventListener("click", (e) => {
        e.stopPropagation();
        approvalAct("reject", a.id, li, a);
      });
      elApprovals.appendChild(li);
    }
  }

  // approvalAct posts /api/admin/approvals/<id>/{approve,reject}. The
  // submitter cannot self-approve — that comes back as 400 from the
  // server with a clear message which we surface as-is.
  async function approvalAct(verb, id, rowEl, cr) {
    if (!token) { toast("Sign in first.", "warn"); return; }
    const prompt_msg = verb === "approve"
      ? `Approve "${cr.action}" on ${cr.resource}?\n\nOptional comment:`
      : `Reject "${cr.action}" on ${cr.resource}?\n\nReason:`;
    const note = prompt(prompt_msg, "");
    if (note === null) return;
    const btnA = rowEl.querySelector('[data-role="approve"]');
    const btnR = rowEl.querySelector('[data-role="reject"]');
    btnA.disabled = btnR.disabled = true;
    try {
      const csrfRes = await fetch("/api/admin/csrf", { headers: authHeaders() });
      const csrf = (await csrfRes.json()).token;
      const body = verb === "approve" ? { comment: note } : { reason: note };
      const res = await fetch(`/api/admin/approvals/${encodeURIComponent(id)}/${verb}`, {
        method: "POST",
        headers: { ...authHeaders(), "Content-Type": "application/json", "X-CSRF-Token": csrf },
        body: JSON.stringify(body),
      });
      if (!res.ok) {
        const j = await res.json().catch(() => ({}));
        toast(`${verb} refused (HTTP ${res.status}): ${j.error || ""}`, "err");
        return;
      }
      const updated = await res.json();
      if (updated.status === "applied") {
        toast(`Approved and applied: ${updated.action} on ${updated.resource}.`, "ok");
      } else if (updated.status === "approved") {
        toast(`Approved (${(updated.approvals || []).length}/${updated.threshold}). Waiting for more reviewers.`, "ok");
      } else if (updated.status === "rejected") {
        toast(`Rejected.`, "info");
      }
      refreshAdmin();
    } catch (e) {
      toast(`${verb} failed: ${e.message}`, "err");
    } finally {
      btnA.disabled = btnR.disabled = false;
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
  // We classify each line on arrival (err / warn / info), store the
  // classification on the DOM node, then apply visual style and current
  // search filter on every flush. Keeps the hot path tight.
  const reError = /\b(?:ERROR|ERR|FATAL|FAIL(?:ED)?|panic(?::|!)?|Exception|Traceback)\b/i;
  const reWarn  = /\b(?:WARN(?:ING)?|deprecat)/i;
  function classifyLine(s) {
    if (reError.test(s)) return "err";
    if (reWarn.test(s)) return "warn";
    return "";
  }

  const elLogSearch = $("log-search");
  const elLogTs     = $("log-ts");
  const elLogHi     = $("log-hi");
  let logSearch = "";
  function applyLogSearch() {
    logSearch = elLogSearch.value;
    // Re-filter existing lines without rewriting them.
    const q = logSearch.toLowerCase();
    for (const child of elLogs.children) {
      const txt = child.dataset.raw || child.textContent;
      child.hidden = q !== "" && !txt.toLowerCase().includes(q);
    }
  }
  elLogSearch.addEventListener("input", applyLogSearch);
  elLogTs.addEventListener("change", () => elLogs.classList.toggle("show-ts", elLogTs.checked));
  elLogHi.addEventListener("change", () => elLogs.classList.toggle("no-highlight", !elLogHi.checked));
  // initialise classes
  if (!elLogHi.checked) elLogs.classList.add("no-highlight");

  $("log-clear").addEventListener("click", () => { elLogs.innerHTML = ""; });
  $("log-download").addEventListener("click", () => {
    const lines = [];
    for (const c of elLogs.children) {
      if (c.hidden) continue;
      lines.push(c.dataset.raw || c.textContent);
    }
    const blob = new Blob([lines.join("\n")], { type: "text/plain" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = `${currentDeploy || "logs"}-${new Date().toISOString()}.log`;
    a.click();
    URL.revokeObjectURL(a.href);
  });

  function enqueueLine(line, isErrStream) {
    logQueue.push({ line, isErrStream });
    if (logQueue.length === 1) requestAnimationFrame(flushLogs);
  }

  function flushLogs() {
    const frag = document.createDocumentFragment();
    const now = new Date();
    const tsStr = now.toLocaleTimeString();
    for (const { line, isErrStream } of logQueue) {
      const span = document.createElement("span");
      const cls = isErrStream ? "err" : classifyLine(line);
      span.className = "line" + (cls ? " " + cls : "");
      span.dataset.raw = line;
      span.innerHTML = `<span class="ts">${esc(tsStr)} </span>${esc(line)}\n`;
      if (logSearch && !line.toLowerCase().includes(logSearch.toLowerCase())) {
        span.hidden = true;
      }
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
    // Audit entries are security-relevant: showing only time loses
    // cross-day context. If the event is today, render HH:MM:SS for
    // density; otherwise prepend the date so log review across days
    // doesn't conflate "2am yesterday" with "2am today".
    const now = new Date();
    const sameDay = d.getFullYear() === now.getFullYear() &&
                    d.getMonth() === now.getMonth() &&
                    d.getDate() === now.getDate();
    return sameDay ? d.toLocaleTimeString()
                   : d.toLocaleDateString() + " " + d.toLocaleTimeString();
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
