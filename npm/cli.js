#!/usr/bin/env node
"use strict";

const { spawnSync } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");
const {
  DEFAULT_BASE_URL,
  configureClients,
  normalizeBaseUrl,
} = require("./lib/configure");

const packageRoot = path.resolve(__dirname, "..");

function main(argv) {
  const [command = "start", ...rest] = argv;
  switch (command) {
    case "configure":
      return runConfigure(rest);
    case "start":
    case "serve":
      return runBinary(rest);
    case "help":
    case "--help":
    case "-h":
      printHelp();
      return 0;
    default:
      return runBinary([command, ...rest]);
  }
}

function runConfigure(args) {
  const options = parseConfigureArgs(args);
  const results = configureClients(options);
  for (const result of results) {
    process.stdout.write(
      `Configured ${result.client} to use ${result.baseUrl} via ${result.path}\n`,
    );
  }
  return 0;
}

function parseConfigureArgs(args) {
  let client = process.env.KLOVIS_CLIENT || "";
  let baseUrl = process.env.KLOVIS_BASE_URL || DEFAULT_BASE_URL;

  for (let index = 0; index < args.length; index += 1) {
    const value = args[index];
    if (value === "--base-url") {
      const next = args[index + 1];
      if (!next) {
        throw new Error("missing value for --base-url");
      }
      baseUrl = next;
      index += 1;
      continue;
    }
    if (value.startsWith("--base-url=")) {
      baseUrl = value.slice("--base-url=".length);
      continue;
    }
    if (!client) {
      client = value;
      continue;
    }
    throw new Error(`unexpected argument ${JSON.stringify(value)}`);
  }

  if (!client) {
    throw new Error("missing client, expected codex, claude, or both");
  }

  return {
    client,
    baseUrl: normalizeBaseUrl(baseUrl),
  };
}

function runBinary(args) {
  const binaryPath = resolveBinaryPath();
  const result = spawnSync(binaryPath, args, {
    cwd: process.cwd(),
    stdio: "inherit",
    env: process.env,
  });

  if (result.error) {
    throw result.error;
  }

  return result.status || 0;
}

function resolveBinaryPath() {
  const binaryName = process.platform === "win32" ? "klovys99.exe" : "klovys99";
  const binaryPath = path.join(packageRoot, "dist", binaryName);
  if (!fs.existsSync(binaryPath)) {
    throw new Error(
      `missing compiled binary at ${binaryPath}. Run npm install again or build with go build -o dist/${binaryName} ./cmd/klovys99.`,
    );
  }
  return binaryPath;
}

function printHelp() {
  process.stdout.write(`Klovys99

Usage:
  klovys99 start
  klovys99 configure codex [--base-url http://127.0.0.1:8080]
  klovys99 configure claude [--base-url http://127.0.0.1:8080]
  klovys99 configure both [--base-url http://127.0.0.1:8080]
`);
}

try {
  process.exitCode = main(process.argv.slice(2));
} catch (error) {
  process.stderr.write(`${error.message}\n`);
  process.exitCode = 1;
}
