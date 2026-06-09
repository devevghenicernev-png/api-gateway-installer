import type { ReactNode } from "react";
import { Maximize2, Minimize2, Plus } from "lucide-react";
import { Card } from "../ui/card";
import { Button } from "../ui/button";
import { cn } from "../../lib/utils";

// Panel — main building block of the dashboard grid. Wraps shadcn Card
// with a consistent header (title + optional badge + add button +
// maximize toggle) and a content body. Maximize state is driven by the
// parent grid via the `maxed` prop (URL-hash-backed in Dashboard.tsx)
// so any one panel can take over the whole viewport.

export interface PanelProps {
  area: string;                  // grid-area name (apis, deploys, …)
  title: ReactNode;
  badge?: ReactNode;
  add?: { label: string; onClick: () => void };
  maxed: boolean;
  onMaxToggle: () => void;
  headerExtras?: ReactNode;      // toolbar items rendered between title and the ⤢
  className?: string;
  children: ReactNode;
}

export function Panel({ area, title, badge, add, maxed, onMaxToggle, headerExtras, className, children }: PanelProps) {
  return (
    <Card
      style={{ gridArea: area }}
      className={cn("flex min-h-0 flex-col overflow-hidden", className)}
    >
      <div className="flex items-center gap-2 border-b border-border px-4 py-2.5">
        <h3 className="text-sm font-semibold leading-none tracking-tight">{title}</h3>
        {badge}
        <div className="ml-auto flex items-center gap-1">
          {headerExtras}
          {add && (
            <Button variant="outline" size="xs" onClick={add.onClick} className="gap-1">
              <Plus className="h-3 w-3" />
              {add.label}
            </Button>
          )}
          <Button
            variant="ghost"
            size="icon"
            onClick={onMaxToggle}
            aria-label={maxed ? "Restore panel" : "Maximize panel"}
            className="h-7 w-7"
          >
            {maxed ? <Minimize2 className="h-3.5 w-3.5" /> : <Maximize2 className="h-3.5 w-3.5" />}
          </Button>
        </div>
      </div>
      <div className="flex-1 min-h-0 overflow-auto p-4">{children}</div>
    </Card>
  );
}
