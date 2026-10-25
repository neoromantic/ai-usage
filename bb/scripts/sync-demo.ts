// Copies the made-up team report the repository's demo tooling writes into
// the plugin, which bundles it as its example team: BB builds only files
// inside the plugin's folder.
import { copyFileSync, mkdirSync } from "node:fs";
import path from "node:path";

const root = path.join(import.meta.dir, "..");
mkdirSync(path.join(root, "demo"), { recursive: true });
copyFileSync(path.join(root, "../docs/demo/team.json"), path.join(root, "demo/team.json"));
