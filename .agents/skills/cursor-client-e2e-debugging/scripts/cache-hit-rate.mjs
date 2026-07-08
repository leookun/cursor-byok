#!/usr/bin/env node

import { spawn } from "node:child_process";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

const scriptPath = fileURLToPath(import.meta.url);
const skillRoot = path.resolve(path.dirname(scriptPath), "..");
const repoRoot = path.resolve(skillRoot, "../../..");
const args = ["run", "./scripts/historymetrics", ...process.argv.slice(2)];

const child = spawn("go", args, {
  cwd: repoRoot,
  stdio: "inherit",
});

child.on("error", (error) => {
  const message = error instanceof Error ? error.message : String(error);
  console.error(`cache-hit-rate.mjs failed: ${message}`);
  process.exitCode = 1;
});

child.on("exit", (code, signal) => {
  if (typeof code === "number") {
    process.exitCode = code;
    return;
  }
  if (signal) {
    console.error(`cache-hit-rate.mjs terminated by signal: ${signal}`);
  }
  process.exitCode = 1;
});
