"use strict";

const { spawnSync } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const DEFAULT_MODEL = "urchade/gliner_multi_pii-v1";
const DEFAULT_REVISION = "1fcf13e85f4eef5394e1fcd406cf2ca9ea82351d";
const MODE_OFF = "off";
const MODE_FULL = "full";

function resolveSettings(args, env = process.env, home = os.homedir()) {
  const options = parseArgs(args);
  return settingsFromOptions(options, env, home);
}

function resolveStartSettings(args, env = process.env, home = os.homedir()) {
  const { options, binaryArgs } = parseStartArgs(args);
  const presidioMode = resolveMode(options.presidioMode || env.KLOVIS_PRESIDIO_MODE || MODE_FULL);
  if (!presidioMode) throw new Error("Presidio mode must be full or off.");
  const mode = resolveMode(options.mode || env.KLOVIS_GLINER_MODE || "");
  if (!mode) {
    throw new Error("GLiNER mode must be full or off.");
  }
  if (mode === MODE_OFF) {
    return {
      mode,
      binaryArgs,
      env: {
        ...process.env,
        KLOVIS_GLINER_MODE: MODE_OFF,
        KLOVIS_GLINER_ENABLED: "false",
        KLOVIS_PRESIDIO_MODE: presidioMode,
      },
    };
  }
  return {
    ...settingsFromOptions({ ...options, mode }, env, home),
    mode,
    presidioMode,
    binaryArgs,
  };
}

function settingsFromOptions(options, env = process.env, home = os.homedir()) {
  const modelOverride = options.model || env.KLOVIS_GLINER_MODEL || "";
  const revisionOverride = options.revision || env.KLOVIS_GLINER_MODEL_REVISION || "";
  if ((modelOverride && !revisionOverride) || (!modelOverride && revisionOverride)) {
    throw new Error(
      "Custom GLiNER settings require both model and revision together.",
    );
  }
  const model = modelOverride || DEFAULT_MODEL;
  const revision = revisionOverride || DEFAULT_REVISION;
  return {
    model,
    revision,
    dataDir:
      options.dataDir ||
      env.KLOVIS_GLINER_DATA_DIR ||
      path.join(home, ".klovys99", "gliner"),
    mode: resolveMode(options.mode || env.KLOVIS_GLINER_MODE || MODE_FULL),
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
      "--mode": "mode",
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

function parseStartArgs(args) {
  const options = {};
  const binaryArgs = [];
  for (let index = 0; index < args.length; index += 1) {
    const value = args[index];
    if (value === "--gliner") {
      continue;
    }
    const [key, inline] = value.split("=", 2);
    const names = {
      "--gliner-mode": "mode",
      "--gliner-model": "model",
      "--gliner-revision": "revision",
      "--gliner-data-dir": "dataDir",
      "--presidio-mode": "presidioMode",
      "--mode": "mode",
      "--model": "model",
      "--revision": "revision",
      "--data-dir": "dataDir",
    };
    if (!names[key]) {
      binaryArgs.push(value);
      continue;
    }
    const next = inline || args[index + 1];
    if (!next) {
      throw new Error(`missing value for ${key}`);
    }
    options[names[key]] = next;
    if (!inline) {
      index += 1;
    }
  }
  return { options, binaryArgs };
}

function resolveMode(value) {
  switch (String(value || "").trim().toLowerCase()) {
    case MODE_OFF:
      return MODE_OFF;
    case "":
    case MODE_FULL:
      return MODE_FULL;
    default:
      return "";
  }
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
      "-e",
      "HOME=/tmp",
      "-e",
      "XDG_CACHE_HOME=/tmp/.cache",
      "-e",
      "HF_HOME=/tmp/.cache/huggingface",
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

function isInstalled(settings) {
  const manifestPath = path.join(settings.dataDir, "model", "klovis-model-manifest.json");
  if (!fs.existsSync(manifestPath)) {
    return false;
  }
  try {
    const manifest = JSON.parse(fs.readFileSync(manifestPath, "utf8"));
    return manifest.model === settings.model && manifest.revision === settings.revision;
  } catch {
    return false;
  }
}

function start(settings, packageRoot, spawn = spawnSync) {
  if (!isInstalled(settings)) {
    install(settings, packageRoot, spawn);
  }
  const composePath = path.join(packageRoot, "sidecar", "gliner", "compose.yaml");
  const env = {
    ...process.env,
    KLOVIS_GLINER_MODE: settings.mode,
    KLOVIS_GLINER_ENABLED: "true",
    KLOVIS_GLINER_URL: process.env.KLOVIS_GLINER_URL || "http://127.0.0.1:8091",
    KLOVIS_GLINER_MODEL: settings.model,
    KLOVIS_GLINER_MODEL_REVISION: settings.revision,
    KLOVIS_GLINER_DATA_DIR: path.resolve(settings.dataDir),
    KLOVIS_PRESIDIO_MODE: settings.presidioMode || process.env.KLOVIS_PRESIDIO_MODE || MODE_FULL,
  };
  run("docker", ["compose", "-f", composePath, "up", "-d", "--no-build"], { env }, spawn);
  if ((settings.presidioMode || MODE_FULL) === MODE_FULL) {
    const presidioDir = path.join(packageRoot, "sidecar", "presidio");
    run("docker", ["build", "-t", "klovys99-presidio:local", presidioDir], {}, spawn);
    run("docker", ["compose", "-f", path.join(presidioDir, "compose.yaml"), "up", "-d", "--no-build"], { env }, spawn);
    env.KLOVIS_PRESIDIO_URL = process.env.KLOVIS_PRESIDIO_URL || "http://127.0.0.1:8092";
  }
  return env;
}

module.exports = {
  DEFAULT_MODEL,
  DEFAULT_REVISION,
  MODE_FULL,
  MODE_OFF,
  install,
  isInstalled,
  parseArgs,
  resolveSettings,
  resolveStartSettings,
  run,
  start,
};
