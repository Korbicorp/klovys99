#!/usr/bin/env node
"use strict";

const fs = require("node:fs");
const path = require("node:path");
const { spawnSync } = require("node:child_process");
const { configureClients, normalizeBaseUrl } = require("./lib/configure");
const {
  binaryFileName,
  installReleaseBinary,
  readPackageVersion,
} = require("./lib/install");

const packageRoot = path.resolve(__dirname, "..");

async function main() {
  await ensureBinaryInstalled();

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

async function ensureBinaryInstalled() {
  if (process.env.KLOVIS_SKIP_DOWNLOAD === "true") {
    process.stdout.write("Skipping Klovys99 binary download because KLOVIS_SKIP_DOWNLOAD=true\n");
    if (canBuildFromSource()) {
      buildBinary();
      return;
    }
    throw new Error(
      "KLOVIS_SKIP_DOWNLOAD=true but no local Go source checkout is available for fallback build",
    );
  }

  const version = readPackageVersion(packageRoot);
  try {
    const result = await installReleaseBinary({ version, packageRoot });
    process.stdout.write(`Installed Klovys99 binary from ${result.assetUrl}\n`);
    return;
  } catch (error) {
    if (!canBuildFromSource()) {
      throw new Error(
        `unable to install Klovys99 prebuilt binary: ${error.message}. ` +
          "This package expects a published GitHub release for the current version.",
      );
    }
    process.stdout.write(
      `Prebuilt Klovys99 binary download failed (${error.message}). Falling back to local Go build.\n`,
    );
  }

  buildBinary();
}

function canBuildFromSource() {
  return (
    process.env.KLOVIS_SKIP_BUILD !== "true" &&
    fs.existsSync(path.join(packageRoot, "cmd", "klovys99"))
  );
}

function buildBinary() {
  const binaryName = binaryFileName(process.platform);
  const binaryPath = path.join(packageRoot, "dist", binaryName);
  fs.mkdirSync(path.dirname(binaryPath), { recursive: true });

  const result = spawnSync("go", ["build", "-o", binaryPath, "./cmd/klovys99"], {
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
  Promise.resolve(main()).catch((error) => {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  });
} catch (error) {
  process.stderr.write(`${error.message}\n`);
  process.exitCode = 1;
}
