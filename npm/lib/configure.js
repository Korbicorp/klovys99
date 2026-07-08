"use strict";

const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const DEFAULT_BASE_URL = "http://127.0.0.1:8080";
const KLOVIS_BEGIN_MARKER = "# BEGIN KLOVIS";
const KLOVIS_END_MARKER = "# END KLOVIS";
const KLOVIS_PROVIDER_ID = "klovis99";

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
  return `${normalizeBaseUrl(baseUrl)}/v1`;
}

function claudeProxyBaseUrl(baseUrl) {
  return `${normalizeBaseUrl(baseUrl)}/anthropic`;
}

function configureClients({ client, baseUrl, homeDir = os.homedir(), env = process.env }) {
  switch (client) {
    case "codex":
      return [configureCodex({ baseUrl, homeDir, env })];
    case "claude":
      return [configureClaude({ baseUrl, homeDir })];
    case "both":
      return [
        configureCodex({ baseUrl, homeDir, env }),
        configureClaude({ baseUrl, homeDir }),
      ];
    default:
      throw new Error(`unsupported client ${JSON.stringify(client)}`);
  }
}

function configureCodex({ baseUrl, homeDir = os.homedir(), env = process.env } = {}) {
  const configDir = codexConfigDir({ homeDir, env });
  const configPath = path.join(configDir, "config.toml");
  const desiredBaseUrl = codexProxyBaseUrl(baseUrl);
  const existing = readFileIfExists(configPath);
  const requiresOpenAIAuth = codexUsesChatGPTAuth(path.join(configDir, "auth.json"));
  const updated = updateCodexConfig(existing, desiredBaseUrl, { requiresOpenAIAuth });
  let backupPath = null;
  if (existing !== "" && existing !== updated) {
    backupPath = backupFile(configPath, existing);
  }
  writeTextFile(configPath, updated);
  return {
    client: "codex",
    path: configPath,
    baseUrl: desiredBaseUrl,
    backupPath,
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

function updateCodexConfig(content, desiredBaseUrl, options = {}) {
  const requiresOpenAIAuth = Boolean(options.requiresOpenAIAuth);
  const managedLines = [
    KLOVIS_BEGIN_MARKER,
    `model_provider = ${tomlString(KLOVIS_PROVIDER_ID)}`,
    `openai_base_url = ${tomlString(desiredBaseUrl)}`,
    "",
    `[model_providers.${KLOVIS_PROVIDER_ID}]`,
    `name = ${tomlString("OpenAI via Klovys anonymization proxy")}`,
    `base_url = ${tomlString(desiredBaseUrl)}`,
    "supports_websockets = true",
  ];
  if (requiresOpenAIAuth) {
    managedLines.push("requires_openai_auth = true");
  }
  managedLines.push(KLOVIS_END_MARKER);
  const managedBlock = managedLines.join("\n");

  if (!content.trim()) {
    return `${managedBlock}\n`;
  }

  let cleaned = stripCodexManagedContent(content);
  const managedBlockPattern = new RegExp(
    `${escapeRegExp(KLOVIS_BEGIN_MARKER)}[\\s\\S]*?${escapeRegExp(KLOVIS_END_MARKER)}`,
    "m",
  );
  if (managedBlockPattern.test(content)) {
    const trimmed = cleaned.replace(/\s+$/, "");
    return trimmed ? `${managedBlock}\n\n${trimmed}\n` : `${managedBlock}\n`;
  }

  const trimmed = cleaned.replace(/\s+$/, "");
  return trimmed ? `${managedBlock}\n\n${trimmed}\n` : `${managedBlock}\n`;
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

function backupFile(filePath, content, now = new Date()) {
  fs.mkdirSync(path.dirname(filePath), { recursive: true });
  const stamp = now.toISOString().replace(/[:.]/g, "-");
  const backupPath = `${filePath}.bak.${stamp}`;
  fs.writeFileSync(backupPath, content, "utf8");
  return backupPath;
}

function codexConfigDir({ homeDir = os.homedir(), env = process.env } = {}) {
  const codexHome = String(env.CODEX_HOME || "").trim();
  if (codexHome) {
    return codexHome;
  }
  return path.join(homeDir, ".codex");
}

function codexUsesChatGPTAuth(authPath) {
  let parsed;
  try {
    parsed = JSON.parse(fs.readFileSync(authPath, "utf8"));
  } catch (_error) {
    return false;
  }
  if (parsed === null || Array.isArray(parsed) || typeof parsed !== "object") {
    return false;
  }
  if (String(parsed.auth_mode || "").toLowerCase() === "chatgpt") {
    return true;
  }
  const tokens = parsed.tokens;
  return Boolean(
    tokens &&
      !Array.isArray(tokens) &&
      typeof tokens === "object" &&
      typeof tokens.account_id === "string" &&
      tokens.account_id.trim(),
  );
}

function stripCodexManagedContent(content) {
  let output = content;
  const managedBlockPattern = new RegExp(
    `${escapeRegExp(KLOVIS_BEGIN_MARKER)}[\\s\\S]*?${escapeRegExp(KLOVIS_END_MARKER)}\\s*`,
    "gm",
  );
  output = output.replace(managedBlockPattern, "");
  output = output.replace(/^[ \t]*model_provider[ \t]*=.*\r?\n/gm, "");
  output = output.replace(/^[ \t]*openai_base_url[ \t]*=.*\r?\n/gm, "");
  const providerTablePattern = new RegExp(
    `(^|\\n)\\[model_providers\\.${escapeRegExp(KLOVIS_PROVIDER_ID)}\\][\\s\\S]*?(?=\\n\\[|\\s*$)`,
    "m",
  );
  output = output.replace(providerTablePattern, "\n");
  return output;
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
  backupFile,
  claudeProxyBaseUrl,
  codexProxyBaseUrl,
  codexConfigDir,
  codexUsesChatGPTAuth,
  configureClaude,
  configureClients,
  configureCodex,
  normalizeBaseUrl,
  stripCodexManagedContent,
  updateClaudeConfig,
  updateCodexConfig,
};
