// Builds a standalone executable (no Node.js needed) for the current OS into dist/.
// Uses Node's single executable applications: https://nodejs.org/api/single-executable-applications.html
// Run: npm run build   (needs Node 25.5+ for --build-sea)
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { build } from "esbuild";

const root = path.resolve(import.meta.dirname, "..");
const dist = path.join(root, "dist");
const { version } = JSON.parse(fs.readFileSync(path.join(root, "package.json"), "utf8"));
const exe = path.join(dist, process.platform === "win32" ? "multiviewer-replay-sync.exe" : "multiviewer-replay-sync");

fs.rmSync(dist, { recursive: true, force: true });
fs.mkdirSync(dist);

// 1. One CommonJS file with everything in it (SEA can only run a single script).
await build({
  entryPoints: [path.join(root, "src", "cli.js")],
  bundle: true,
  platform: "node",
  format: "cjs",
  target: "node20",
  outfile: path.join(dist, "bundle.cjs"),
  external: ["bufferutil", "utf-8-validate"], // optional speedups for ws; it works without them
  define: { "process.env.MVRS_VERSION": JSON.stringify(version) },
  logLevel: "warning",
});

// 2. Wrap it into a copy of the Node binary.
const seaConfig = path.join(dist, "sea-config.json");
fs.writeFileSync(
  seaConfig,
  JSON.stringify({ main: path.join(dist, "bundle.cjs"), output: exe, disableExperimentalSEAWarning: true })
);
execFileSync(process.execPath, ["--build-sea", seaConfig], { stdio: "inherit" });

// 3. macOS refuses to run a modified binary without a (here: ad-hoc) signature.
if (process.platform === "darwin") execFileSync("codesign", ["--sign", "-", "--force", exe], { stdio: "inherit" });

fs.rmSync(seaConfig);
fs.rmSync(path.join(dist, "bundle.cjs"));
console.log(`Built ${path.relative(root, exe)} (${(fs.statSync(exe).size / 1e6).toFixed(0)} MB)`);
