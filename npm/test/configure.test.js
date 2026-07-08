"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const {
  claudeProxyBaseUrl,
  codexProxyBaseUrl,
  codexConfigDir,
  configureCodex,
  updateClaudeConfig,
  updateCodexConfig,
} = require("../lib/configure");

test("codex base URL uses the root OpenAI-compatible route", () => {
  assert.equal(codexProxyBaseUrl("http://127.0.0.1:8080/"), "http://127.0.0.1:8080/v1");
});

test("claude base URL uses the anthropic route prefix", () => {
  assert.equal(claudeProxyBaseUrl("http://127.0.0.1:8080/"), "http://127.0.0.1:8080/anthropic");
});

test("updateCodexConfig appends a managed block when the file is empty", () => {
  const updated = updateCodexConfig("", "http://127.0.0.1:8080/v1");
  assert.match(updated, /# BEGIN KLOVIS/);
  assert.match(updated, /model_provider = "klovis99"/);
  assert.match(updated, /openai_base_url = "http:\/\/127\.0\.0\.1:8080\/v1"/);
  assert.match(updated, /\[model_providers\.klovis99\]/);
  assert.match(updated, /supports_websockets = true/);
  assert.doesNotMatch(updated, /env_key/);
});

test("updateCodexConfig replaces existing top-level Codex routing keys", () => {
  const updated = updateCodexConfig(
    'model = "gpt-5.4"\nmodel_provider = "openai"\nopenai_base_url = "https://api.openai.com/v1"\n',
    "http://127.0.0.1:8080/v1",
  );
  assert.match(updated, /model = "gpt-5.4"/);
  assert.match(updated, /openai_base_url = "http:\/\/127\.0\.0\.1:8080\/v1"/);
  assert.doesNotMatch(updated, /https:\/\/api\.openai\.com\/v1/);
  assert.equal((updated.match(/^model_provider\s*=/gm) || []).length, 1);
  assert.equal((updated.match(/^openai_base_url\s*=/gm) || []).length, 1);
});

test("updateCodexConfig includes requires_openai_auth only for ChatGPT auth", () => {
  const apiKeyConfig = updateCodexConfig("", "http://127.0.0.1:8080/v1");
  const chatGPTConfig = updateCodexConfig("", "http://127.0.0.1:8080/v1", {
    requiresOpenAIAuth: true,
  });
  assert.doesNotMatch(apiKeyConfig, /requires_openai_auth/);
  assert.match(chatGPTConfig, /requires_openai_auth = true/);
});

test("codexConfigDir respects CODEX_HOME", () => {
  assert.equal(
    codexConfigDir({ homeDir: "/home/alice", env: { CODEX_HOME: "/tmp/codex-home" } }),
    "/tmp/codex-home",
  );
  assert.equal(codexConfigDir({ homeDir: "/home/alice", env: {} }), path.join("/home/alice", ".codex"));
});

test("configureCodex writes backup and detects ChatGPT auth", () => {
  const homeDir = fs.mkdtempSync(path.join(os.tmpdir(), "klovys-config-"));
  const codexHome = path.join(homeDir, "codex");
  fs.mkdirSync(codexHome, { recursive: true });
  const configPath = path.join(codexHome, "config.toml");
  fs.writeFileSync(configPath, 'model = "gpt-5.4"\nopenai_base_url = "https://api.openai.com/v1"\n');
  fs.writeFileSync(path.join(codexHome, "auth.json"), '{"auth_mode":"chatgpt"}');

  const result = configureCodex({
    baseUrl: "http://127.0.0.1:8080",
    homeDir,
    env: { CODEX_HOME: codexHome },
  });
  const updated = fs.readFileSync(configPath, "utf8");

  assert.equal(result.path, configPath);
  assert.equal(result.baseUrl, "http://127.0.0.1:8080/v1");
  assert.ok(result.backupPath);
  assert.equal(
    fs.readFileSync(result.backupPath, "utf8"),
    'model = "gpt-5.4"\nopenai_base_url = "https://api.openai.com/v1"\n',
  );
  assert.match(updated, /requires_openai_auth = true/);
  assert.match(updated, /supports_websockets = true/);
});

test("updateClaudeConfig writes ANTHROPIC_BASE_URL under env", () => {
  const updated = updateClaudeConfig('{"model":"claude-sonnet-4-5"}', "http://127.0.0.1:8080/anthropic");
  assert.deepEqual(updated, {
    model: "claude-sonnet-4-5",
    env: {
      ANTHROPIC_BASE_URL: "http://127.0.0.1:8080/anthropic",
    },
  });
});

test("updateClaudeConfig rejects invalid JSON", () => {
  assert.throws(
    () => updateClaudeConfig("{", "http://127.0.0.1:8080/anthropic"),
    /invalid Claude settings JSON/,
  );
});
