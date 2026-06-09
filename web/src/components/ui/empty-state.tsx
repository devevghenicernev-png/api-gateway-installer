import type { ReactNode } from "react";
import { cn } from "../../lib/utils";
import { Button } from "./button";

interface EmptyStateProps {
  icon?: ReactNode;
  title: string;
  description?: ReactNode;
  primaryAction?: { label: string; onClick?: () => void; href?: string };
  secondaryAction?: { label: string; onClick?: () => void; href?: string };
  className?: string;
}

// EmptyState — reusable panel placeholder. Avoids the trap of a panel
// looking broken when it's genuinely correct-but-empty (no deploys,
// nothing pending, etc). Icon + headline + body + optional CTAs.
export function EmptyState({ icon, title, description, primaryAction, secondaryAction, className }: EmptyStateProps) {
  return (
    <div className={cn("flex flex-col items-center justify-center gap-3 px-6 py-10 text-center", className)}>
      {icon && (
        <div className="rounded-full bg-muted p-3 text-muted-foreground" aria-hidden="true">
          {icon}
        </div>
      )}
      <div className="space-y-1">
        <p className="text-sm font-semibold text-foreground">{title}</p>
        {description && <div className="text-xs text-muted-foreground max-w-md leading-relaxed">{description}</div>}
      </div>
      {(primaryAction || secondaryAction) && (
        <div className="flex flex-wrap gap-2 justify-center pt-1">
          {primaryAction && (
            primaryAction.href
              ? <Button asChild size="sm"><a href={primaryAction.href}>{primaryAction.label}</a></Button>
              : <Button size="sm" onClick={primaryAction.onClick}>{primaryAction.label}</Button>
          )}
          {secondaryAction && (
            secondaryAction.href
              ? <Button asChild size="sm" variant="outline"><a href={secondaryAction.href}>{secondaryAction.label}</a></Button>
              : <Button size="sm" variant="outline" onClick={secondaryAction.onClick}>{secondaryAction.label}</Button>
          )}
        </div>
      )}
    </div>
  );
}
