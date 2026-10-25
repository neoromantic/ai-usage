// The frontend: the AI Usage page, its settings section, the sidebar
// footer's Team usage, and two palette commands. Everything it shows comes
// from `ai-usage report --json` through the server.
import { useEffect } from "react";
import { toast } from "sonner";
import { definePluginApp, useBbNavigate, useRpc } from "@get-bb/plugin-sdk/app";
import type { rpcContract } from "./src/rpc";
import { FooterCard } from "./components/footer";
import { PanelHeader, PanelPage } from "./components/panel";
import { SettingsSection } from "./components/settings";
import { PANEL_PATH } from "./components/use-ai-usage";

// Palette commands run outside React; this window's overlay lends them the
// navigation and RPC client its hooks give.
const bridge: { open?: () => void; refresh?: () => void } = {};

function CommandBridge() {
  const navigate = useBbNavigate();
  const rpc = useRpc<typeof rpcContract>();
  useEffect(() => {
    bridge.open = () => navigate.toPluginPanel(PANEL_PATH);
    bridge.refresh = () => {
      rpc.call("collect", { hostId: null }).then(
        () => toast("Collecting…"),
        (e: unknown) => toast.error(e instanceof Error ? e.message : String(e)),
      );
    };
    return () => {
      bridge.open = undefined;
      bridge.refresh = undefined;
    };
  }, [navigate, rpc]);
  return null;
}

export default definePluginApp((app) => {
  app.slots.navPanel({
    id: "usage",
    title: "AI Usage",
    icon: "ai-usage/team",
    path: PANEL_PATH,
    component: PanelPage,
    headerContent: PanelHeader,
  });
  app.slots.settingsSection({
    id: "machine",
    title: "This machine",
    description: "The ai-usage this plugin reads, on the machine BB runs on.",
    component: SettingsSection,
  });
  app.experimental_sidebarFooter.register({
    kind: "disclosure",
    id: "team-usage",
    label: "Team usage",
    icon: "ai-usage/team",
    component: FooterCard,
  });
  app.slots.experimental_appOverlay({ id: "commands", component: CommandBridge });
  app.commands.register({ id: "open", title: "AI Usage: Open", run: () => bridge.open?.() });
  app.commands.register({ id: "refresh", title: "AI Usage: Refresh", run: () => bridge.refresh?.() });
});
