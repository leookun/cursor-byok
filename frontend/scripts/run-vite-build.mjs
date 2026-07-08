import { spawn } from "child_process";
import { createRequire } from "module";
import path from "path";

const require = createRequire(import.meta.url);
const vitePackagePath = require.resolve("vite/package.json");
const viteBin = path.join(path.dirname(vitePackagePath), "bin", "vite.js");
const extraArgs = process.argv.slice(2);
const shouldScan = extraArgs.includes("--scan");
const forwardedArgs = extraArgs.filter((arg) => arg !== "--scan");

const child = spawn(process.execPath, [viteBin, "build", ...forwardedArgs], {
  stdio: "inherit",
  env: {
    ...process.env,
    STATIC_I18N_SCAN: shouldScan ? "true" : process.env.STATIC_I18N_SCAN || "false",
  },
});

child.on("exit", (code, signal) => {
  if (signal) {
    process.kill(process.pid, signal);
    return;
  }

  process.exit(code ?? 0);
});
