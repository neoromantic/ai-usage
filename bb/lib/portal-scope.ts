declare const __BB_PLUGIN_ID__: string | undefined;

export function usePortalScopeProps() {
  const pluginId = typeof __BB_PLUGIN_ID__ === "string" ? __BB_PLUGIN_ID__ : undefined;
  return {
    "data-bb-portaled-overlay": "",
    "data-bb-plugin-root": "",
    ...(pluginId !== undefined ? { "data-bb-plugin": pluginId } : {}),
  };
}
