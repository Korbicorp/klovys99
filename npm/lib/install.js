"use strict";

const fs = require("node:fs");
const https = require("node:https");
const path = require("node:path");
const { pipeline } = require("node:stream/promises");

const RELEASE_HOST = "https://github.com";

function detectTarget(platform = process.platform, arch = process.arch) {
  const key = `${platform}/${arch}`;
  switch (key) {
    case "darwin/arm64":
      return { os: "darwin", arch: "arm64", extension: "" };
    case "darwin/x64":
      return { os: "darwin", arch: "amd64", extension: "" };
    case "linux/arm64":
      return { os: "linux", arch: "arm64", extension: "" };
    case "linux/x64":
      return { os: "linux", arch: "amd64", extension: "" };
    case "win32/arm64":
      return { os: "windows", arch: "arm64", extension: ".exe" };
    case "win32/x64":
      return { os: "windows", arch: "amd64", extension: ".exe" };
    default:
      throw new Error(
        `unsupported platform ${JSON.stringify(platform)} and architecture ${JSON.stringify(arch)}`,
      );
  }
}

function releaseTag(version) {
  const trimmed = String(version || "").trim();
  if (!trimmed) {
    throw new Error("missing package version");
  }
  return trimmed.startsWith("v") ? trimmed : `v${trimmed}`;
}

function normalizedVersion(version) {
  return releaseTag(version).slice(1);
}

function binaryFileName(platform = process.platform) {
  return platform === "win32" ? "klovys99.exe" : "klovys99";
}

function releaseAssetName(version, target) {
  return `klovys99_${normalizedVersion(version)}_${target.os}_${target.arch}${target.extension}`;
}

function releaseAssetUrl(version, target, repository = defaultRepository()) {
  const tag = releaseTag(version);
  const assetName = releaseAssetName(version, target);
  return `${RELEASE_HOST}/${repository.owner}/${repository.name}/releases/download/${tag}/${assetName}`;
}

function readPackageManifest(packageRoot) {
  const manifestPath = path.join(packageRoot, "package.json");
  return JSON.parse(fs.readFileSync(manifestPath, "utf8"));
}

function readPackageVersion(packageRoot) {
  return readPackageManifest(packageRoot).version;
}

function readRepository(packageRoot) {
  const manifest = readPackageManifest(packageRoot);
  const value =
    typeof manifest.repository === "string"
      ? manifest.repository
      : manifest.repository && typeof manifest.repository.url === "string"
        ? manifest.repository.url
        : "";
  return parseRepository(value);
}

function defaultRepository() {
  return {
    owner: "Korbicorp",
    name: "klovys99",
  };
}

function parseRepository(value) {
  const trimmed = String(value || "").trim();
  if (!trimmed) {
    return defaultRepository();
  }

  const normalized = trimmed
    .replace(/^git\+/, "")
    .replace(/^git@github\.com:/, "https://github.com/")
    .replace(/\.git$/, "");
  const match = normalized.match(/github\.com\/([^/]+)\/([^/]+)$/);
  if (!match) {
    return defaultRepository();
  }

  return {
    owner: match[1],
    name: match[2],
  };
}

async function installReleaseBinary({
  version,
  packageRoot,
  platform = process.platform,
  arch = process.arch,
}) {
  const target = detectTarget(platform, arch);
  const binaryPath = path.join(packageRoot, "dist", binaryFileName(platform));
  const assetUrl = releaseAssetUrl(version, target, readRepository(packageRoot));

  await fs.promises.mkdir(path.dirname(binaryPath), { recursive: true });
  await downloadToFile(assetUrl, binaryPath);

  if (platform !== "win32") {
    await fs.promises.chmod(binaryPath, 0o755);
  }

  return {
    assetUrl,
    binaryPath,
    target,
  };
}

async function downloadToFile(url, destinationPath) {
  const tempPath = `${destinationPath}.tmp`;
  try {
    await downloadWithRedirects(url, tempPath, 5);
    await fs.promises.rename(tempPath, destinationPath);
  } catch (error) {
    await fs.promises.rm(tempPath, { force: true });
    throw error;
  }
}

async function downloadWithRedirects(url, destinationPath, redirectsRemaining) {
  const response = await request(url);

  if (response.statusCode >= 300 && response.statusCode < 400 && response.headers.location) {
    if (redirectsRemaining <= 0) {
      response.resume();
      throw new Error(`too many redirects while downloading ${url}`);
    }
    const redirectedUrl = new URL(response.headers.location, url).toString();
    response.resume();
    return downloadWithRedirects(redirectedUrl, destinationPath, redirectsRemaining - 1);
  }

  if (response.statusCode !== 200) {
    const body = await readResponseBody(response);
    throw new Error(
      `download ${url} failed with status ${response.statusCode}${body ? `: ${body}` : ""}`,
    );
  }

  const file = fs.createWriteStream(destinationPath, { mode: 0o755 });
  await pipeline(response, file);
}

function request(url) {
  return new Promise((resolve, reject) => {
    const req = https.get(
      url,
      {
        headers: {
          "user-agent": "klovys99-installer",
        },
      },
      resolve,
    );
    req.on("error", reject);
  });
}

async function readResponseBody(response) {
  let body = "";
  response.setEncoding("utf8");
  for await (const chunk of response) {
    body += chunk;
    if (body.length > 512) {
      body = `${body.slice(0, 512)}...`;
      break;
    }
  }
  return body.trim();
}

module.exports = {
  binaryFileName,
  defaultRepository,
  detectTarget,
  installReleaseBinary,
  normalizedVersion,
  parseRepository,
  readPackageVersion,
  readRepository,
  releaseAssetName,
  releaseAssetUrl,
  releaseTag,
};
