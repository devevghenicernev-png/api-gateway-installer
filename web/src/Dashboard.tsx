import { useCallback, useEffect, useState } from "react";
// Pinned to react-resizable-panels v2.x (stable PanelGroup / Panel /
// PanelResizeHandle API). v4 renamed exports + flipped to a single
// `defaultLayout` array prop on the group, which doesn't compose well
// with our auto-saved-per-group ID scheme.
import { Panel as RPanel, PanelGroup, PanelResizeHandle } from "react-resizable-panels";
import { useQueryClient } from "@tanstack/react-query";
import { useStatus } from "./hooks/useAdminQueries";
import { TopBar } from "./components/layout/TopBar";
import { Panel } from "./components/layout/Panel";
import { SignInDialog } from "./components/layout/SignInDialog";
import { SettingsSheet } from "./components/layout/SettingsSheet";
import { ApisPanel } from "./components/apis/ApisPanel";
import { ApiAddDialog } from "./components/apis/ApiAddDialog";
import { DeploysPanel } from "./components/deploys/DeploysPanel";
import { DeployAddDialog } from "./components/deploys/DeployAddDialog";
import { DeploySSHKeyDialog } from "./components/deploys/DeploySSHKeyDialog";
import { TlsPanel } from "./components/tls/TlsPanel";
import { TlsAddDialog } from "./components/tls/TlsAddDialog";
import { AuditPanel } from "./components/audit/AuditPanel";
import { ApprovalsPanel } from "./components/approvals/ApprovalsPanel";
import { WebhookActivityPanel } from "./components/webhook/WebhookActivityPanel";
import { WebhookSetupDialog } from "./components/webhook/WebhookSetupDialog";
import { LogsPanel } from "./components/logs/LogsPanel";
import { Badge } from "./components/ui/badge";
import { useApis, useApprovals, useDeploys } from "./hooks/useAdminQueries";
import { useSSE, type SSEStatus } from "./hooks/useSSE";
import { TooltipProvider } from "./components/ui/tooltip";

type PanelKey = "apis" | "deploys" | "tls" | "audit" | "approvals" | "webhook" | "logs";

// Persisted layout IDs. react-resizable-panels writes to localStorage
// under these keys — drag once, every subsequent reload keeps the
// proportions. Bump the version suffix when the panel set changes so
// stale percentages can't strand a panel at 0%.
const RESIZE_VERTICAL_ID = "apigw.dashboard.rows.v1";
const RESIZE_ROW1_ID     = "apigw.dashboard.row1.v1";
const RESIZE_ROW2_ID     = "apigw.dashboard.row2.v1";

// Visible resize handles. v0.5.3 used `bg-transparent` 6px — operators
// couldn't see where to grab, the whole feature looked broken. Now a
// 1px subtle line + 3-dot grip in the middle, both fade to primary on
// hover or active drag. 7px hit area is comfortable on touch screens.
function VHandle() {
  return (
    <PanelResizeHandle className="group relative w-[7px] cursor-col-resize">
      <div className="pointer-events-none absolute inset-y-0 left-1/2 w-px -translate-x-1/2 bg-border transition-colors group-hover:bg-primary group-data-[resize-handle-active]:bg-primary" />
      <div className="pointer-events-none absolute left-1/2 top-1/2 flex -translate-x-1/2 -translate-y-1/2 flex-col gap-0.5 opacity-40 transition-opacity group-hover:opacity-100">
        <span className="h-0.5 w-0.5 rounded-full bg-foreground" />
        <span className="h-0.5 w-0.5 rounded-full bg-foreground" />
        <span className="h-0.5 w-0.5 rounded-full bg-foreground" />
      </div>
    </PanelResizeHandle>
  );
}
function HHandle() {
  return (
    <PanelResizeHandle className="group relative h-[7px] cursor-row-resize">
      <div className="pointer-events-none absolute inset-x-0 top-1/2 h-px -translate-y-1/2 bg-border transition-colors group-hover:bg-primary group-data-[resize-handle-active]:bg-primary" />
      <div className="pointer-events-none absolute left-1/2 top-1/2 flex -translate-x-1/2 -translate-y-1/2 gap-0.5 opacity-40 transition-opacity group-hover:opacity-100">
        <span className="h-0.5 w-0.5 rounded-full bg-foreground" />
        <span className="h-0.5 w-0.5 rounded-full bg-foreground" />
        <span className="h-0.5 w-0.5 rounded-full bg-foreground" />
      </div>
    </PanelResizeHandle>
  );
}

// Map cfg.change `section` → React Query key prefix to invalidate.
// `any` invalidates everything under ["admin", ...] — the catch-all
// path for operations that touch multiple sections at once.
const SECTION_TO_QUERY: Record<string, readonly unknown[][]> = {
  apis:      [["admin", "apis"]],
  deploys:   [["admin", "deploys"]],
  tls:       [["admin", "tls"]],
  approvals: [["admin", "approvals", "pending"], ["admin", "approvals", "all"]],
  webhook:   [["admin", "webhook-activity"]],
  streams:   [["admin", "streams"]],
  consumers: [["admin", "consumers"]],
  tokens:    [["admin", "admin-tokens"]],
  sso:       [["admin", "sso"], ["status"]],
  tuning:    [["admin", "tuning"]],
  any:       [["admin"]],
};

export function Dashboard() {
  const qc = useQueryClient();

  // Maximize state persisted in URL hash so a refresh / share-link
  // keeps focus on the panel the operator was inspecting.
  const [maxed, setMaxed] = useState<PanelKey | null>(() => {
    const m = window.location.hash.match(/max=(\w+)/);
    return (m?.[1] as PanelKey) || null;
  });
  useEffect(() => {
    const next = maxed ? `#max=${maxed}` : "";
    if (window.location.hash !== next) {
      history.replaceState(null, "", window.location.pathname + window.location.search + next);
    }
  }, [maxed]);
  const onMaxToggle = (k: PanelKey) => () => setMaxed((cur) => (cur === k ? null : k));

  const [signInOpen, setSignInOpen] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [apiAddOpen, setApiAddOpen] = useState(false);
  const [deployAddOpen, setDeployAddOpen] = useState(false);
  const [tlsAddOpen, setTlsAddOpen] = useState(false);
  const [sshOpen, setSshOpen] = useState(false);
  const [webhookDeploy, setWebhookDeploy] = useState<string | null>(null);

  const [sseStatus, setSseStatus] = useState<SSEStatus>("connecting");
  const handleSSEStatus = useCallback((s: SSEStatus) => setSseStatus(s), []);

  useStatus();
  const { data: apis } = useApis();
  const { data: deploys } = useDeploys();
  const { data: approvals } = useApprovals("pending");

  // SSE-driven cache invalidation. The backend publishes a `cfg.change`
  // event with `{section: "apis"|"deploys"|..."any"}` after every
  // successful Guard+Save inside the admin handlers. React Query
  // invalidates only the affected section so we don't refetch
  // everything — and we don't have to keep polling at 10s intervals.
  // Polling stays as a 60s fallback in case SSE is disconnected
  // (the queries' refetchInterval).
  //
  // `audit.entry` pushes new rows for the Audit log live tail without
  // a periodic /api/admin/audit poll.
  const { status: cfgSSEStatus } = useSSE(
    ["cfg.change", "audit.entry"],
    {
      "cfg.change": (data) => {
        const section = (data as { section?: string })?.section || "any";
        const targets = SECTION_TO_QUERY[section] || SECTION_TO_QUERY.any;
        for (const key of targets) {
          qc.invalidateQueries({ queryKey: key as unknown as string[] });
        }
      },
      "audit.entry": () => {
        qc.invalidateQueries({ queryKey: ["admin", "audit"] });
      },
    },
  );
  useEffect(() => { handleSSEStatus(cfgSSEStatus); }, [cfgSSEStatus, handleSSEStatus]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.target instanceof HTMLElement && /input|textarea|select/i.test(e.target.tagName)) return;
      if (e.key === "Escape" && maxed) { setMaxed(null); }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [maxed]);

  // ---- Panel renderers ----
  const apisPanel = (
    <Panel area="apis" title="APIs"
      badge={<Badge variant="muted">{(apis?.length ?? 0) + (deploys?.length ?? 0)}</Badge>}
      add={{ label: "Add", onClick: () => setApiAddOpen(true) }}
      maxed={maxed === "apis"} onMaxToggle={onMaxToggle("apis")}
    >
      <ApisPanel onAdd={() => setApiAddOpen(true)} />
    </Panel>
  );
  const deploysPanel = (
    <Panel area="deploys" title="Deployments"
      add={{ label: "Add", onClick: () => setDeployAddOpen(true) }}
      maxed={maxed === "deploys"} onMaxToggle={onMaxToggle("deploys")}
    >
      <DeploysPanel
        onAdd={() => setDeployAddOpen(true)}
        onShowWebhook={(name) => setWebhookDeploy(name)}
        onShowSSH={() => setSshOpen(true)}
      />
    </Panel>
  );
  const tlsPanel = (
    <Panel area="tls" title="TLS"
      add={{ label: "Add", onClick: () => setTlsAddOpen(true) }}
      maxed={maxed === "tls"} onMaxToggle={onMaxToggle("tls")}
    >
      <TlsPanel onAdd={() => setTlsAddOpen(true)} />
    </Panel>
  );
  const auditPanel = (
    <Panel area="audit" title="Audit log" maxed={maxed === "audit"} onMaxToggle={onMaxToggle("audit")}>
      <AuditPanel />
    </Panel>
  );
  const approvalsPanel = (
    <Panel area="approvals" title="Pending approvals"
      badge={approvals && approvals.length > 0 ? <Badge variant="warning">{approvals.length}</Badge> : null}
      maxed={maxed === "approvals"} onMaxToggle={onMaxToggle("approvals")}
    >
      <ApprovalsPanel />
    </Panel>
  );
  const webhookPanel = (
    <Panel area="webhook" title="Webhook activity" maxed={maxed === "webhook"} onMaxToggle={onMaxToggle("webhook")}>
      <WebhookActivityPanel onAddDeploy={() => setDeployAddOpen(true)} />
    </Panel>
  );
  const logsPanel = (
    <Panel area="logs" title="Live logs" maxed={maxed === "logs"} onMaxToggle={onMaxToggle("logs")}>
      <LogsPanel onSSEStatus={handleSSEStatus} />
    </Panel>
  );

  const single: Record<PanelKey, React.ReactNode> = {
    apis: apisPanel, deploys: deploysPanel, tls: tlsPanel,
    audit: auditPanel, approvals: approvalsPanel, webhook: webhookPanel, logs: logsPanel,
  };

  return (
    <TooltipProvider>
      <div className="flex min-h-screen flex-col bg-background text-foreground">
        <TopBar sseStatus={sseStatus} onOpenSignIn={() => setSignInOpen(true)} onOpenSettings={() => setSettingsOpen(true)} />

        {/* p-3 padding around the panel area. The PanelGroup container
            below uses `h-[calc(100vh-...)] min-h-[720px]` so that on a
            tall viewport the dashboard fits in one screen exactly, but
            on a short one (laptop folded, devtools open) the body grows
            beyond viewport and the page scrolls — instead of clipping
            the bottom panel. Internal panel scrolling still works via
            Panel.tsx's overflow-auto body, so very long audit/log
            content scrolls inside its own panel. */}
        <main id="main" className="flex-1 p-3">
          {maxed ? (
            <div className="flex h-[calc(100vh-80px)] min-h-[600px]">
              <div className="flex-1 min-h-0">{single[maxed]}</div>
            </div>
          ) : (
            <div className="h-[calc(100vh-80px)] min-h-[720px]">
              <PanelGroup direction="vertical" autoSaveId={RESIZE_VERTICAL_ID} className="h-full">
                <RPanel defaultSize={32} minSize={15} className="min-h-0">
                  <PanelGroup direction="horizontal" autoSaveId={RESIZE_ROW1_ID}>
                    <RPanel defaultSize={33} minSize={15} className="min-h-0"><div className="h-full pr-1">{apisPanel}</div></RPanel>
                    <VHandle />
                    <RPanel defaultSize={33} minSize={15} className="min-h-0"><div className="h-full px-1">{deploysPanel}</div></RPanel>
                    <VHandle />
                    <RPanel defaultSize={34} minSize={15} className="min-h-0"><div className="h-full pl-1">{tlsPanel}</div></RPanel>
                  </PanelGroup>
                </RPanel>
                <HHandle />
                <RPanel defaultSize={32} minSize={15} className="min-h-0">
                  <PanelGroup direction="horizontal" autoSaveId={RESIZE_ROW2_ID}>
                    <RPanel defaultSize={33} minSize={15} className="min-h-0"><div className="h-full pr-1">{auditPanel}</div></RPanel>
                    <VHandle />
                    <RPanel defaultSize={33} minSize={15} className="min-h-0"><div className="h-full px-1">{approvalsPanel}</div></RPanel>
                    <VHandle />
                    <RPanel defaultSize={34} minSize={15} className="min-h-0"><div className="h-full pl-1">{webhookPanel}</div></RPanel>
                  </PanelGroup>
                </RPanel>
                <HHandle />
                <RPanel defaultSize={36} minSize={15} className="min-h-0">
                  <div className="h-full">{logsPanel}</div>
                </RPanel>
              </PanelGroup>
            </div>
          )}
        </main>

        <SignInDialog open={signInOpen} onOpenChange={setSignInOpen} />
        <SettingsSheet open={settingsOpen} onOpenChange={setSettingsOpen} />
        <ApiAddDialog open={apiAddOpen} onOpenChange={setApiAddOpen} />
        <DeployAddDialog open={deployAddOpen} onOpenChange={setDeployAddOpen} />
        <TlsAddDialog open={tlsAddOpen} onOpenChange={setTlsAddOpen} />
        <DeploySSHKeyDialog open={sshOpen} onOpenChange={setSshOpen} />
        <WebhookSetupDialog deploy={webhookDeploy} onClose={() => setWebhookDeploy(null)} />
      </div>
    </TooltipProvider>
  );
}
