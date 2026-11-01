Tests for the pure helpers in `web/lib` (dotenv, refs, cron). No extra dependencies: Node 22.6+ strips the types.

```sh
cd web && node --import ./lib/__tests__/register.mjs --test "lib/__tests__/*.test.mjs"
```
