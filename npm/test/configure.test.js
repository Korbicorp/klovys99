"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");

const {
  claudeProxyBaseUrl,
  codexProxyBaseUrl,
  updateClaudeConfig,
  updateCodexConfig,
} = require("../lib/configure");

test("codex base URL uses the openai route prefix", () => {
  assert.equal(codexProxyBaseUrl("http://127.0.0.1:8080/"), "http://127.0.0.1:8080/openai/v1");
});

test("claude base URL uses the anthropic route prefix", () => {
  assert.equal(claudeProxyBaseUrl("http://127.0.0.1:8080/"), "http://127.0.0.1:8080/anthropic");
});

test("updateCodexConfig appends a managed block when the file is empty", () => {
  const updated = updateCodexConfig("", "http://127.0.0.1:8080/openai/v1");
  assert.match(updated, /# BEGIN KLOVIS/);
  assert.match(updated, /openai_base_url = "http:\/\/127\.0\.0\.1:8080\/openai\/v1"/);
});

test("updateCodexConfig replaces an existing openai_base_url", () => {
  const updated = updateCodexConfig(
    'model = "gpt-5.4"\nopenai_base_url = "https://api.openai.com/v1"\n',
    "http://127.0.0.1:8080/openai/v1",
  );
  assert.match(updated, /model = "gpt-5.4"/);
  assert.match(updated, /openai_base_url = "http:\/\/127\.0\.0\.1:8080\/openai\/v1"/);
  assert.doesNotMatch(updated, /https:\/\/api\.openai\.com\/v1/);
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
