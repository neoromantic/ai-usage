import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

/** The color a report state takes, as the console's: out and over red, tight yellow, ok green. */
export function stateTone(state: string | null | undefined): string {
  switch (state) {
    case "out":
    case "over":
    case "error":
      return "bg-destructive";
    case "tight":
    case "warn":
      return "bg-warning";
    case "ok":
      return "bg-success";
    case "info":
      return "bg-attention";
    default:
      return "bg-muted-foreground/40";
  }
}

export function Dot({ state, title, className }: { state: string | null | undefined; title?: string; className?: string }) {
  return <span title={title} aria-label={title} className={cn("inline-block size-2 shrink-0 rounded-full", stateTone(state), className)} />;
}

export function Bar({ percent, state, className }: { percent: number; state: string; className?: string }) {
  const width = Math.max(0, Math.min(100, percent));
  return (
    <div className={cn("h-1.5 w-full overflow-hidden rounded-full bg-muted", className)} role="meter" aria-valuenow={Math.round(width)} aria-valuemin={0} aria-valuemax={100}>
      <div className={cn("h-full rounded-full", stateTone(state))} style={{ width: `${width}%` }} />
    </div>
  );
}

export function Badge({ children, className, title }: { children: ReactNode; className?: string; title?: string }) {
  return (
    <span title={title} className={cn("inline-flex items-center rounded-md border border-border px-1.5 py-0.5 text-[11px] leading-none text-muted-foreground", className)}>
      {children}
    </span>
  );
}

export function Section({ title, aside, children }: { title: string; aside?: ReactNode; children: ReactNode }) {
  return (
    <section className="space-y-2">
      <div className="flex items-baseline justify-between gap-3">
        <h2 className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{title}</h2>
        {aside}
      </div>
      {children}
    </section>
  );
}

export function Spinner({ className }: { className?: string }) {
  return <span aria-hidden className={cn("inline-block size-3.5 animate-spin rounded-full border-2 border-muted-foreground/30 border-t-muted-foreground", className)} />;
}

export function Banner({ tone = "quiet", children, action }: { tone?: "out" | "tight" | "quiet"; children: ReactNode; action?: ReactNode }) {
  return (
    <div
      role="status"
      className={cn(
        "flex items-center justify-between gap-3 rounded-lg border px-3 py-2 text-sm",
        tone === "out" ? "border-destructive/40 bg-destructive/5" : tone === "tight" ? "border-warning/50 bg-warning/5" : "border-border bg-card",
      )}
    >
      <div className="min-w-0">{children}</div>
      {action}
    </div>
  );
}

/** A log that opens on a click, as an installer's details. */
export function Log({ lines, open }: { lines: string[]; open?: boolean }) {
  if (lines.length === 0) return null;
  return (
    <details open={open} className="rounded-lg border border-border bg-card text-xs">
      <summary className="cursor-pointer select-none px-3 py-2 text-muted-foreground">Details</summary>
      <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words px-3 pb-3 font-mono text-[11px] leading-relaxed text-muted-foreground">{lines.join("\n")}</pre>
    </details>
  );
}

export function Page({ children }: { children: ReactNode }) {
  return (
    <div className="h-full min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto box-border w-full max-w-4xl space-y-6 p-4 md:p-5">{children}</div>
    </div>
  );
}
