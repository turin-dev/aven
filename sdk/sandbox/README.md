# Aven sandbox SDK (TypeScript)

Dependency-free `aven-sandbox` client for [Aven sandboxes](../docs/api-v1.md#sandboxes). Works in Node.js 22+ (uses the global `fetch`) and other runtimes with `fetch`, `atob`/`btoa` and `TextEncoder`.

Configuration comes from options or the environment: `AVEN_URL`, `AVEN_TOKEN` (an API token with member access to the project), `AVEN_ENVIRONMENT_ID`. The `KEEL_*` names still work.

```ts
import { Sandbox } from "aven-sandbox";

await using sandbox = await Sandbox.create({ idleTimeoutMinutes: 30, env: { CI: "true" } });

await sandbox.files.write("/app/run.sh", "#!/bin/sh\necho hello\n", { mode: 0o755 });
const { exitCode, stdout } = await sandbox.exec("/app/run.sh", { cwd: "/app", timeoutSec: 60 });

// Prepare once, then branch.
await sandbox.exec("apt-get update && apt-get install -y git");
await sandbox.checkpoint("with-git");
const fork = await sandbox.fork();            // same filesystem, fresh processes
const fresh = await Sandbox.create("with-git"); // from the checkpoint, even after the source is gone
await fork.destroy();
await fresh.destroy();
// `sandbox` is destroyed when the scope exits.
```

| API | |
|-----|-|
| `Sandbox.create(options?)` | New sandbox. `image`, `nodeId`, `idleTimeoutMinutes` (0 = never), `resources: { cpu, memoryMB }`, `networkIsolation: "ISOLATED" \| "PRIVATE"`, `env` |
| `Sandbox.create("name")` / `Sandbox.create(source)` | From a checkpoint / fork of a running sandbox |
| `Sandbox.connect(id)`, `Sandbox.list()` | Reattach, list the environment's sandboxes |
| `sandbox.exec(cmd, { cwd, env, stdin, timeoutSec, maxOutputBytes })` | String runs via `sh -lc`, array as argv. Resolves with `exitCode`, `stdout`, `stderr`, `truncated`, `timedOut` (does not throw on non-zero exit) |
| `sandbox.files.read/write/list` | Read (text or `{ format: "bytes" }`, up to 2 MiB), write (up to 256 KiB, creates parents), list a directory |
| `sandbox.fork()`, `sandbox.checkpoint(name)` | Copy the filesystem into a new sandbox / save it under a name (reusing a name replaces it) |
| `Sandbox.checkpoints()`, `renameCheckpoint`, `deleteCheckpoint` | Manage the environment's checkpoints |
| `sandbox.heartbeat()`, `refresh()`, `destroy()` | Reset the idle timer, re-read, tear down |

Commands run over HTTP (no streaming, detach/reattach or writable stdin), each `exec` is limited to 110 seconds, and there are no templates or public sandbox domains.

Type-check with `npx tsc -p sdk/tsconfig.json --noEmit`.
