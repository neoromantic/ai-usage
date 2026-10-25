// The machine's view and report, kept current: from the server's cache at
// once, again when the server says something changed, and every minute
// while a surface is open. The report comes only when it changed.
import { useCallback, useEffect, useRef, useState } from "react";
import { useBbNavigate, useRealtime, useRealtimeConnectionState, useRpc } from "@get-bb/plugin-sdk/app";
import type { Report } from "../src/report";
import type { HostView, JobView, rpcContract } from "../src/rpc";

export interface AiUsageState {
  view: HostView;
  job: JobView | null;
  report: Report | null;
  reportAt: number | null;
  refreshing: boolean;
}

export function useAiUsage(hostId: string | null = null) {
  const rpc = useRpc<typeof rpcContract>();
  const [state, setState] = useState<AiUsageState | null>(null);
  const [error, setError] = useState<string | null>(null);
  const hash = useRef<string | null>(null);
  const report = useRef<Report | null>(null);
  const viewHost = useRef<string | null>(null);

  const load = useCallback(async () => {
    try {
      const r = await rpc.call("state", { hostId, hash: hash.current });
      if (r.report !== null) report.current = r.report as Report;
      else if (r.hash === null) report.current = null;
      hash.current = r.hash;
      viewHost.current = r.view.hostId;
      setState({ view: r.view, job: r.job, report: report.current, reportAt: r.reportAt, refreshing: r.refreshing });
      setError(null);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    }
  }, [rpc, hostId]);

  useEffect(() => {
    void load();
    const timer = setInterval(() => void load(), 60_000);
    return () => clearInterval(timer);
  }, [load]);

  const pending = useRef<ReturnType<typeof setTimeout> | null>(null);
  useRealtime("changed", (payload) => {
    const p = payload as { hostId?: string } | null;
    if (viewHost.current !== null && p?.hostId !== viewHost.current) return;
    if (pending.current !== null) return;
    pending.current = setTimeout(() => {
      pending.current = null;
      void load();
    }, 250);
  });
  useRealtime("job", (payload) => {
    const p = payload as { id: string; line?: string } | null;
    if (p?.line === undefined) return;
    setState((s) => {
      if (s?.job === null || s?.job === undefined || s.job.id !== p.id) return s;
      return { ...s, job: { ...s.job, log: [...s.job.log, p.line!].slice(-400) } };
    });
  });
  const connection = useRealtimeConnectionState();
  const wasConnected = useRef(false);
  useEffect(() => {
    if (connection === "connected") {
      if (wasConnected.current) void load();
      wasConnected.current = true;
    }
  }, [connection, load]);

  return { rpc, state, error, reload: load };
}

// The example team lives at the page's "example" path, so a link can open it.
export const EXAMPLE = "example";
export const PANEL_PATH = "usage";

export function useExample(): { show: () => void; leave: () => void } {
  const navigate = useBbNavigate();
  return {
    show: () => navigate.toPluginPanel(PANEL_PATH, { subPath: EXAMPLE }),
    leave: () => navigate.toPluginPanel(PANEL_PATH),
  };
}

export function useDemoReport(on: boolean): Report | null {
  const rpc = useRpc<typeof rpcContract>();
  const [report, setReport] = useState<Report | null>(null);
  useEffect(() => {
    if (!on || report !== null) return;
    void rpc.call("demo").then((r) => setReport(r.report as Report));
  }, [on, rpc, report]);
  return on ? report : null;
}

/** "now" that moves on every half minute, for ages and countdowns. */
export function useNow(): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 30_000);
    return () => clearInterval(timer);
  }, []);
  return now;
}
