#!/usr/bin/env node
"use strict";

const fs = require("node:fs");
const path = require("node:path");
const { spawnSync } = require("node:child_process");
const { configureClients, normalizeBaseUrl } = require("./lib/configure");

const packageRoot = path.resolve(__dirname, "..");

function main() {
  if (process.env.KLOVIS_SKIP_BUILD === "true") {
    process.stdout.write("Skipping Klovis Go build because KLOVIS_SKIP_BUILD=true\n");
    return;
  }

  buildBinary();

  const client = process.env.KLOVIS_CLIENT || process.env.KLOVIS_SETUP_CLIENT;
  if (!client || process.env.KLOVIS_SKIP_CONFIGURE === "true") {
    return;
  }

  const baseUrl = normalizeBaseUrl(process.env.KLOVIS_BASE_URL || "http://127.0.0.1:8080");
  const results = configureClients({ client, baseUrl });
  for (const result of results) {
    process.stdout.write(
      `Configured ${result.client} to use ${result.baseUrl} via ${result.path}\n`,
    );
  }
}

function buildBinary() {
  const binaryName = process.platform === "win32" ? "klovis.exe" : "klovis";
  const binaryPath = path.join(packageRoot, "dist", binaryName);
  fs.mkdirSync(path.dirname(binaryPath), { recursive: true });

  const result = spawnSync("go", ["build", "-o", binaryPath, "./cmd/klovis"], {
    cwd: packageRoot,
    stdio: "inherit",
    env: process.env,
  });

  if (result.error) {
    throw new Error(`unable to run go build: ${result.error.message}`);
  }
  if (result.status !== 0) {
    throw new Error(`go build failed with exit code ${result.status}`);
  }
}

try {
  main();
} catch (error) {
  process.stderr.write(`${error.message}\n`);
  process.exitCode = 1;
}
