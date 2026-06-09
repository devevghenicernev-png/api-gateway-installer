import { useCallback, useEffect, useState } from "react";
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
import { useApis, useApprovals } from "./hooks/useAdminQueries";
import type { SSEStatus } from "./hooks/useSSE";
import { TooltipProvider } from "./components/ui/tooltip";

type PanelKey = "apis" | "deploys" | "tls" | "audit" | "approvals" | "webhook" | "logs";

export function Dashboard() {
  // Maximize state persisted in URL hash so a refresh / share-link
  // keeps focus on the panel the operator was inspecting.
  const [maxed, setMaxed] = useState<PanelKey | null>(() => {
    const h = window.location.hash;
    const m = h.match(/max=(\w+)/);
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
  const handleSSE = useCallback((s: SSEStatus) => setSseStatus(s), []);

  // Force-mount the status query so TopBar can show uptime.
  useStatus();

  // Counts for panel badges.
  const { data: apis } = useApis();
  const { data: approvals } = useApprovals("pending");

  // Keyboard: "/" → audit filter, "g a/d/t/x" → jump panel, Esc → restore.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.target instanceof HTMLElement && /input|textarea|select/i.test(e.target.tagName)) return;
      if (e.key === "Escape" && maxed) { setMaxed(null); return; }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [maxed]);

  return (
    <TooltipProvider>
      <div className="flex min-h-screen flex-col bg-background text-foreground">
        <TopBar sseStatus={sseStatus} onOpenSignIn={() => setSignInOpen(true)} onOpenSettings={() => setSettingsOpen(true)} />

        <main id="main" className="panel-grid" data-maxed={maxed || undefined}>
          <Panel
            area="apis" title="APIs"
            badge={apis ? <Badge variant="muted">{apis.length}</Badge> : null}
            add={{ label: "Add", onClick: () => setApiAddOpen(true) }}
            maxed={maxed === "apis"} onMaxToggle={onMaxToggle("apis")}
          >
            <ApisPanel onAdd={() => setApiAddOpen(true)} />
          </Panel>

          <Panel
            area="deploys" title="Deployments"
            add={{ label: "Add", onClick: () => setDeployAddOpen(true) }}
            maxed={maxed === "deploys"} onMaxToggle={onMaxToggle("deploys")}
          >
            <DeploysPanel
              onAdd={() => setDeployAddOpen(true)}
              onShowWebhook={(name) => setWebhookDeploy(name)}
              onShowSSH={() => setSshOpen(true)}
            />
          </Panel>

          <Panel
            area="tls" title="TLS"
            add={{ label: "Add", onClick: () => setTlsAddOpen(true) }}
            maxed={maxed === "tls"} onMaxToggle={onMaxToggle("tls")}
          >
            <TlsPanel onAdd={() => setTlsAddOpen(true)} />
          </Panel>

          <Panel area="audit" title="Audit log" maxed={maxed === "audit"} onMaxToggle={onMaxToggle("audit")}>
            <AuditPanel />
          </Panel>

          <Panel
            area="approvals" title="Pending approvals"
            badge={approvals && approvals.length > 0 ? <Badge variant="warning">{approvals.length}</Badge> : null}
            maxed={maxed === "approvals"} onMaxToggle={onMaxToggle("approvals")}
          >
            <ApprovalsPanel />
          </Panel>

          <Panel area="webhook" title="Webhook activity" maxed={maxed === "webhook"} onMaxToggle={onMaxToggle("webhook")}>
            <WebhookActivityPanel onAddDeploy={() => setDeployAddOpen(true)} />
          </Panel>

          <Panel area="logs" title="Live logs" maxed={maxed === "logs"} onMaxToggle={onMaxToggle("logs")}>
            <LogsPanel onSSEStatus={handleSSE} />
          </Panel>
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
