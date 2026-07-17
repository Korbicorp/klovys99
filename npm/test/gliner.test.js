"use strict";

const assert = require("node:assert/strict");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");
const {
  DEFAULT_MODEL,
  DEFAULT_REVISION,
  MODE_FULL,
  MODE_OFF,
  install,
  resolveSettings,
  resolveStartSettings,
  start,
} = require("../lib/gliner");

test("resolveSettings uses pinned defaults", () => {
  assert.deepEqual(
    resolveSettings([], {}, "/tmp/home"),
    {
      model: DEFAULT_MODEL,
      revision: DEFAULT_REVISION,
      dataDir: path.join("/tmp/home", ".klovys99", "gliner"),
      mode: MODE_FULL,
    },
  );
  assert.deepEqual(
    resolveSettings(["--model", "owner/model", "--revision=abc123", "--mode", "full"], {}, "/tmp/home"),
    {
      model: "owner/model",
      revision: "abc123",
      dataDir: path.join("/tmp/home", ".klovys99", "gliner"),
      mode: MODE_FULL,
    },
  );
});

test("resolveSettings requires custom model and revision together", () => {
  assert.throws(
    () => resolveSettings(["--model", "owner/model"], {}, "/tmp/home"),
    /Custom GLiNER settings require both model and revision together/,
  );
  assert.throws(
    () => resolveSettings(["--revision", "abc123"], {}, "/tmp/home"),
    /Custom GLiNER settings require both model and revision together/,
  );
});

test("resolveStartSettings defaults start flow to full", () => {
  assert.deepEqual(resolveStartSettings(["--stats"], {}, "/tmp/home"), {
    model: DEFAULT_MODEL,
    revision: DEFAULT_REVISION,
    dataDir: path.join("/tmp/home", ".klovys99", "gliner"),
    mode: MODE_FULL,
    presidioMode: MODE_FULL,
    binaryArgs: ["--stats"],
  });
});

test("resolveStartSettings supports explicit off mode", () => {
  const settings = resolveStartSettings(["--gliner-mode", "off", "--stats"], {}, "/tmp/home");
  assert.equal(settings.mode, MODE_OFF);
  assert.deepEqual(settings.binaryArgs, ["--stats"]);
  assert.equal(settings.env.KLOVIS_GLINER_MODE, MODE_OFF);
  assert.equal(settings.env.KLOVIS_GLINER_ENABLED, "false");
});

test("resolveStartSettings supports explicit full mode", () => {
  const settings = resolveStartSettings(["--gliner-mode", "full", "--stats"], {}, "/tmp/home");
  assert.equal(settings.mode, MODE_FULL);
  assert.deepEqual(settings.binaryArgs, ["--stats"]);
  assert.equal(settings.model, DEFAULT_MODEL);
  assert.equal(settings.revision, DEFAULT_REVISION);
});

test("resolveStartSettings supports disabling Presidio independently", () => {
  const settings = resolveStartSettings(["--presidio-mode", "off", "--stats"], {}, "/tmp/home");
  assert.equal(settings.presidioMode, MODE_OFF);
  assert.deepEqual(settings.binaryArgs, ["--stats"]);
});

test("install builds before explicit model download", () => {
  const calls = [];
  const dataDir = path.join(os.tmpdir(), `klovis-gliner-test-${process.pid}`);
  install(
    { model: "owner/model", revision: "abc123", dataDir },
    "/package",
    (command, args) => {
      calls.push([command, args]);
      return { status: 0 };
    },
  );
  assert.deepEqual(calls[0][1].slice(0, 3), ["build", "-t", "klovys99-gliner:local"]);
  assert.equal(calls[1][1][0], "run");
  assert.ok(calls[1][1].includes("GLINER_MODEL_REVISION=abc123"));
});

test("start auto-installs missing model, uses compose without rebuilding and enables Go NER", () => {
  const calls = [];
  const dataDir = path.join(os.tmpdir(), `klovis-gliner-start-${process.pid}`);
  const env = start(
    { model: "owner/model", revision: "abc123", dataDir, mode: MODE_FULL },
    "/package",
    (command, args, options) => {
      calls.push({ command, args, options });
      return { status: 0 };
    },
  );
  assert.deepEqual(calls[0].args.slice(0, 3), ["build", "-t", "klovys99-gliner:local"]);
  assert.equal(calls[2].command, "docker");
  assert.ok(calls[2].args.includes("--no-build"));
  assert.equal(env.KLOVIS_GLINER_MODE, MODE_FULL);
  assert.equal(env.KLOVIS_GLINER_ENABLED, "true");
  assert.equal(env.KLOVIS_GLINER_MODEL_REVISION, "abc123");
  assert.equal(env.KLOVIS_PRESIDIO_MODE, MODE_FULL);
  assert.equal(env.KLOVIS_PRESIDIO_URL, "http://127.0.0.1:8092");
});
