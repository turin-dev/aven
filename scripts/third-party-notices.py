#!/usr/bin/env python3
"""Regenerate THIRD_PARTY_NOTICES.md (run through `make notices`).

Needs go-licenses (go install github.com/google/go-licenses/v2@latest) on PATH
and the dashboard's node_modules (cd web && npm ci).
"""
import csv, io, json, pathlib, subprocess

root = pathlib.Path(__file__).resolve().parent.parent
go = subprocess.run(["go-licenses", "report", "./cmd/..."], cwd=root, capture_output=True, text=True, check=True).stdout
npm = json.loads(subprocess.run(["npx", "-y", "license-checker-rseidelsohn@4", "--production", "--json"], cwd=root / "web",
                                capture_output=True, text=True, check=True).stdout)

out = [
    "# Third-party notices", "",
    "Aven (`aven-server`, `aven-agent`, the `aven` CLI and the embedded dashboard) is licensed under AGPL-3.0-only (see `LICENSE`). "
    "It includes the third-party software below, each under its own license. The full license texts are in the linked repositories; "
    "the SIL Open Font License text for the bundled fonts is reproduced at the end.", "",
    "Regenerate this file with `make notices` when dependencies change.", "",
    "## Go modules (compiled into the binaries)", "", "| Module | License | Source |", "| --- | --- | --- |",
]
for mod, url, lic in sorted(csv.reader(io.StringIO(go))):
    if not mod.startswith("github.com/turin-dev/aven"):
        out.append(f"| `{mod}` | {lic} | {url} |")
out += ["", "## JavaScript packages (bundled into the dashboard)", "", "| Package | License | Source |", "| --- | --- | --- |"]
for name in sorted(npm):
    if name.startswith("aven-web@"):
        continue
    v = npm[name]
    lic = v.get("licenses")
    lic = ", ".join(lic) if isinstance(lic, list) else lic
    out.append(f"| `{name}` | {lic} | {v.get('repository', '')} |")
out += ["", "Build-time only (not shipped): `caniuse-lite` (CC-BY-4.0) through browserslist, and `sharp`/libvips (LGPL-3.0-or-later), "
        "which Next.js installs but the static export does not use.", "", "## Fonts", "",
        "Inter, JetBrains Mono and Pretendard are bundled through `@fontsource-variable/inter`, `@fontsource-variable/jetbrains-mono` and "
        "`pretendard` under the SIL Open Font License 1.1.", ""]
ofl = None
for pkg in ["@fontsource-variable/inter", "@fontsource-variable/jetbrains-mono", "pretendard"]:
    d = root / "web/node_modules" / pkg
    lic = next((p for p in [*d.iterdir(), *(d / "dist").glob("*")] if p.is_file() and p.name.upper().startswith("LICENSE")), None) if d.exists() else None
    text = lic.read_text(encoding="utf-8") if lic else ""
    head, sep, body = text.partition("This Font Software is licensed")
    for line in head.strip().splitlines():
        if line.strip():
            out.append(f"- {line.strip()}")
    if sep and ofl is None:
        ofl = sep + body
out += ["", "```text", (ofl or "").strip(), "```", ""]
(root / "THIRD_PARTY_NOTICES.md").write_text("\n".join(out), encoding="utf-8")
print("wrote THIRD_PARTY_NOTICES.md")
