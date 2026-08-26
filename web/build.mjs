// Deterministic zero-dependency build: copies the static sources into dist/
// with fixed content so the Go service can embed and serve a reproducible
// artifact. No network access, no version drift, no wall-clock inputs.
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = dirname(fileURLToPath(import.meta.url));
const outDir = join(root, "dist");
mkdirSync(outDir, { recursive: true });

const files = ["index.html", "app.js", "style.css"];
for (const f of files) {
  writeFileSync(join(outDir, f), readFileSync(join(root, f)));
}
console.log("built", files.length, "static files into dist/");
