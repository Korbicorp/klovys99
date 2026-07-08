"use strict";

const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const DEFAULT_BASE_URL = "http://127.0.0.1:8080";
const KLOVIS_BEGIN_MARKER = "# BEGIN KLOVIS";
const KLOVIS_END_MARKER = "# END KLOVIS";

function normalizeBaseUrl(baseUrl = DEFAULT_BASE_URL) {
  const trimmed = String(baseUrl).trim().replace(/\/+$/, "");
  let parsed;
  try {
    parsed = new URL(trimmed);
  } catch (error) {
    throw new Error(`invalid base URL ${JSON.stringify(baseUrl)}: ${error.message}`);
  }
  if (!parsed.protocol || !parsed.host) {
    throw new Error(`invalid base URL ${JSON.stringify(baseUrl)}: missing protocol or host`);
  }
  return parsed.toString().replace(/\/+$/, "");
}

function codexProxyBaseUrl(baseUrl) {
  return `${normalizeBaseUrl(baseUrl)}/openai/v1`;
}

function claudeProxyBaseUrl(baseUrl) {
  return `${normalizeBaseUrl(baseUrl)}/anthropic`;
}

function configureClients({ client, baseUrl, homeDir = os.homedir() }) {
  switch (client) {
    case "codex":
      return [configureCodex({ baseUrl, homeDir })];
    case "claude":
      return [configureClaude({ baseUrl, homeDir })];
    case "both":
      return [
        configureCodex({ baseUrl, homeDir }),
        configureClaude({ baseUrl, homeDir }),
      ];
    default:
      throw new Error(`unsupported client ${JSON.stringify(client)}`);
  }
}

function configureCodex({ baseUrl, homeDir = os.homedir() }) {
  const configPath = path.join(homeDir, ".codex", "config.toml");
  const desiredBaseUrl = codexProxyBaseUrl(baseUrl);
  const existing = readFileIfExists(configPath);
  const updated = updateCodexConfig(existing, desiredBaseUrl);
  writeTextFile(configPath, updated);
  return {
    client: "codex",
    path: configPath,
    baseUrl: desiredBaseUrl,
  };
}

function configureClaude({ baseUrl, homeDir = os.homedir() }) {
  const configPath = path.join(homeDir, ".claude", "settings.json");
  const desiredBaseUrl = claudeProxyBaseUrl(baseUrl);
  const existing = readFileIfExists(configPath);
  const updated = updateClaudeConfig(existing, desiredBaseUrl);
  writeTextFile(configPath, `${JSON.stringify(updated, null, 2)}\n`);
  return {
    client: "claude",
    path: configPath,
    baseUrl: desiredBaseUrl,
  };
}

function updateCodexConfig(content, desiredBaseUrl) {
  const managedBlock = [
    KLOVIS_BEGIN_MARKER,
    `openai_base_url = ${tomlString(desiredBaseUrl)}`,
    KLOVIS_END_MARKER,
  ].join("\n");

  if (!content.trim()) {
    return `${managedBlock}\n`;
  }

  const managedBlockPattern = new RegExp(
    `${escapeRegExp(KLOVIS_BEGIN_MARKER)}[\\s\\S]*?${escapeRegExp(KLOVIS_END_MARKER)}`,
    "m",
  );
  if (managedBlockPattern.test(content)) {
    return ensureTrailingNewline(content.replace(managedBlockPattern, managedBlock));
  }

  const openAIBaseURLPattern = /^openai_base_url\s*=.*$/m;
  if (openAIBaseURLPattern.test(content)) {
    return ensureTrailingNewline(
      content.replace(openAIBaseURLPattern, `openai_base_url = ${tomlString(desiredBaseUrl)}`),
    );
  }

  const trimmed = content.replace(/\s+$/, "");
  return `${trimmed}\n\n${managedBlock}\n`;
}

function updateClaudeConfig(content, desiredBaseUrl) {
  let parsed;
  if (content.trim() === "") {
    parsed = {};
  } else {
    try {
      parsed = JSON.parse(content);
    } catch (error) {
      throw new Error(`invalid Claude settings JSON: ${error.message}`);
    }
  }

  if (parsed === null || Array.isArray(parsed) || typeof parsed !== "object") {
    throw new Error("invalid Claude settings JSON: top-level value must be an object");
  }

  const env = parsed.env;
  if (env !== undefined && (env === null || Array.isArray(env) || typeof env !== "object")) {
    throw new Error("invalid Claude settings JSON: env must be an object");
  }

  parsed.env = { ...(env || {}), ANTHROPIC_BASE_URL: desiredBaseUrl };
  return parsed;
}

function readFileIfExists(filePath) {
  if (!fs.existsSync(filePath)) {
    return "";
  }
  return fs.readFileSync(filePath, "utf8");
}

function writeTextFile(filePath, content) {
  fs.mkdirSync(path.dirname(filePath), { recursive: true });
  fs.writeFileSync(filePath, content, "utf8");
}

function tomlString(value) {
  return JSON.stringify(String(value));
}

function ensureTrailingNewline(content) {
  return content.endsWith("\n") ? content : `${content}\n`;
}

function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

module.exports = {
  DEFAULT_BASE_URL,
  claudeProxyBaseUrl,
  codexProxyBaseUrl,
  configureClaude,
  configureClients,
  configureCodex,
  normalizeBaseUrl,
  updateClaudeConfig,
  updateCodexConfig,
};
