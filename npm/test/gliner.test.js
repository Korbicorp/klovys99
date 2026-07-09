"use strict";

const assert = require("node:assert/strict");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");
const { install, resolveSettings, start } = require("../lib/gliner");

test("resolveSettings requires immutable model identity", () => {
  assert.throws(() => resolveSettings([], {}, "/tmp/home"), /requires --model and --revision/);
  assert.deepEqual(
    resolveSettings(["--model", "owner/model", "--revision=abc123"], {}, "/tmp/home"),
    {
      model: "owner/model",
      revision: "abc123",
      dataDir: path.join("/tmp/home", ".klovys99", "gliner"),
    },
  );
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

test("start uses compose without rebuilding and enables Go NER", () => {
  let call;
  const env = start(
    { model: "owner/model", revision: "abc123", dataDir: "/models" },
    "/package",
    (command, args, options) => {
      call = { command, args, options };
      return { status: 0 };
    },
  );
  assert.equal(call.command, "docker");
  assert.ok(call.args.includes("--no-build"));
  assert.equal(env.KLOVIS_GLINER_ENABLED, "true");
  assert.equal(env.KLOVIS_GLINER_MODEL_REVISION, "abc123");
});
