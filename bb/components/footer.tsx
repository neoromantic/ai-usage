// The sidebar footer's Team usage: what needs attention and one bar for
// each team subscription. It loads only while it is open.
import { useBbNavigate } from "@get-bb/plugin-sdk/app";
import { Button } from "@/components/ui/button";
import { attentionTone, attentionWords, left, limitingWindows, provider } from "../src/format";
import { Bar, Dot, Spinner } from "./bits";
import { PANEL_PATH, useAiUsage } from "./use-ai-usage";

export function FooterCard({ dismiss }: { dismiss: () => void }) {
  const { state } = useAiUsage();
  const navigate = useBbNavigate();
  const report = state?.report ?? null;
  const open = () => {
    navigate.toPluginPanel(PANEL_PATH);
    dismiss();
  };
  const subs = report === null ? [] : report.team.providers.flatMap((p) => p.accounts.filter((a) => a.subscription).map((a) => ({ p: p.provider, a, w: limitingWindows(a)[0] })));
  return (
    <div className="space-y-3 p-3 text-xs">
      {state === null ? (
        <div className="flex items-center gap-2 text-muted-foreground">
          <Spinner /> Loading…
        </div>
      ) : report === null ? (
        <div className="text-muted-foreground">
          {state.view.phase === "installing" ? "Installing ai-usage…" : state.view.phase === "absent" ? "ai-usage is not set up yet." : "No report yet."}
        </div>
      ) : (
        <>
          {report.attention.length > 0 ? (
            <ul className="space-y-1">
              {report.attention.slice(0, 3).map((a, i) => {
                const tone = attentionTone(a.kind);
                return (
                  <li key={i} className="flex items-center gap-2" title={attentionWords(a).text}>
                    <Dot state={tone === "out" ? "out" : tone === "tight" ? "tight" : null} />
                    <span className="truncate">{attentionWords(a).subject}</span>
                  </li>
                );
              })}
            </ul>
          ) : null}
          <ul className="space-y-2">
            {subs.map(({ p, a, w }) => (
              <li key={`${p}/${a.label}`} className="space-y-1">
                <div className="flex justify-between gap-2">
                  <span className="truncate">
                    {provider(p)} {a.alias ?? a.name}
                    {w !== undefined ? <span className="text-muted-foreground"> · {w.name}</span> : null}
                  </span>
                  <span className="text-muted-foreground">{w !== undefined ? left(w) : "?"}</span>
                </div>
                <Bar percent={w?.percent ?? 0} state={w?.state ?? "unknown"} />
              </li>
            ))}
          </ul>
        </>
      )}
      <Button size="sm" variant="outline" className="w-full" onClick={open}>
        Open AI Usage
      </Button>
    </div>
  );
}
