// The host entry: runs on each BB machine as the person BB runs as, and
// does only what src/contract.ts lists.
import os from "node:os";
import {
  experimental_defineHostEntry,
  experimental_sanitizeInheritedChildProcessEnv,
} from "@get-bb/plugin-sdk/host";
import { hostContract, hostSignals } from "./src/contract.js";
import { cleanEnv, standardDirs } from "./src/env.js";
import { install, probe, relays, runCommand, type Machine } from "./src/host-impl.js";

function machine(tempDir: string): Machine {
  const info = os.userInfo();
  return {
    env: cleanEnv(process.env, experimental_sanitizeInheritedChildProcessEnv),
    home: os.homedir(),
    dirs: standardDirs(os.homedir()),
    platform: process.platform,
    arch: process.arch,
    uid: typeof process.getuid === "function" ? process.getuid() : null,
    user: info.username,
    tempDir,
    fetch,
  };
}

export default experimental_defineHostEntry({
  contract: hostContract,
  experimental_signals: hostSignals,
  handlers: {
    probe: async (_input, context) => probe(machine(context.experimental_paths.tempDir)),
    run: async (input, context) => runCommand(machine(context.experimental_paths.tempDir), input, context.signal),
    relay: async (input, context) => relays(machine(context.experimental_paths.tempDir), input.bin, input.aiHome),
    install: async (input, context) => {
      // The call itself keeps the worker; the lease covers the few seconds
      // between the script's end and the reply.
      const lease = context.experimental_retainWorker();
      try {
        return await install(machine(context.experimental_paths.tempDir), {
          teamKey: input.teamKey,
          relay: input.relay,
          signal: context.signal,
          onLine: (line) => {
            void context.experimental_emitSignal("installLog", { jobId: input.jobId, line: line.slice(0, 4000) });
          },
        });
      } finally {
        await lease.dispose();
      }
    },
  },
});
