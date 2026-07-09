"use strict";

const { spawnSync } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

function resolveSettings(args, env = process.env, home = os.homedir()) {
  const options = parseArgs(args);
  const model = options.model || env.KLOVIS_GLINER_MODEL || "";
  const revision = options.revision || env.KLOVIS_GLINER_MODEL_REVISION || "";
  if (!model || !revision) {
    throw new Error(
      "GLiNER requires --model and --revision (an immutable Hugging Face commit SHA).",
    );
  }
  return {
    model,
    revision,
    dataDir:
      options.dataDir ||
      env.KLOVIS_GLINER_DATA_DIR ||
      path.join(home, ".klovys99", "gliner"),
  };
}

function parseArgs(args) {
  const result = {};
  for (let index = 0; index < args.length; index += 1) {
    const value = args[index];
    const [key, inline] = value.split("=", 2);
    const names = {
      "--model": "model",
      "--revision": "revision",
      "--data-dir": "dataDir",
    };
    if (!names[key]) {
      throw new Error(`unexpected GLiNER argument ${JSON.stringify(value)}`);
    }
    const next = inline || args[index + 1];
    if (!next) {
      throw new Error(`missing value for ${key}`);
    }
    result[names[key]] = next;
    if (!inline) index += 1;
  }
  return result;
}

function run(command, args, options = {}, spawn = spawnSync) {
  const result = spawn(command, args, { stdio: "inherit", ...options });
  if (result.error) throw result.error;
  if (result.status !== 0) {
    throw new Error(`${command} exited with status ${result.status}`);
  }
}

function install(settings, packageRoot, spawn = spawnSync) {
  const sidecarDir = path.join(packageRoot, "sidecar", "gliner");
  fs.mkdirSync(settings.dataDir, { recursive: true, mode: 0o700 });
  run("docker", ["build", "-t", "klovys99-gliner:local", sidecarDir], {}, spawn);
  const userArgs =
    process.platform !== "win32" && typeof process.getuid === "function"
      ? ["--user", `${process.getuid()}:${process.getgid()}`]
      : [];
  run(
    "docker",
    [
      "run",
      "--rm",
      ...userArgs,
      "-e",
      `GLINER_MODEL=${settings.model}`,
      "-e",
      `GLINER_MODEL_REVISION=${settings.revision}`,
      "-e",
      "GLINER_MODEL_DIR=/models/model",
      "-v",
      `${path.resolve(settings.dataDir)}:/models`,
      "klovys99-gliner:local",
      "python",
      "/app/install_model.py",
    ],
    {},
    spawn,
  );
}

function start(settings, packageRoot, spawn = spawnSync) {
  const composePath = path.join(packageRoot, "sidecar", "gliner", "compose.yaml");
  const env = {
    ...process.env,
    KLOVIS_GLINER_ENABLED: "true",
    KLOVIS_GLINER_URL: process.env.KLOVIS_GLINER_URL || "http://127.0.0.1:8091",
    KLOVIS_GLINER_MODEL: settings.model,
    KLOVIS_GLINER_MODEL_REVISION: settings.revision,
    KLOVIS_GLINER_DATA_DIR: path.resolve(settings.dataDir),
  };
  run("docker", ["compose", "-f", composePath, "up", "-d", "--no-build"], { env }, spawn);
  return env;
}

module.exports = { install, parseArgs, resolveSettings, run, start };
