"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");

const {
  binaryFileName,
  defaultRepository,
  detectTarget,
  normalizedVersion,
  parseRepository,
  releaseAssetName,
  releaseAssetUrl,
  releaseTag,
} = require("../lib/install");

test("detectTarget maps macOS arm64 to darwin arm64", () => {
  assert.deepEqual(detectTarget("darwin", "arm64"), {
    os: "darwin",
    arch: "arm64",
    extension: "",
  });
});

test("detectTarget maps linux x64 to amd64", () => {
  assert.deepEqual(detectTarget("linux", "x64"), {
    os: "linux",
    arch: "amd64",
    extension: "",
  });
});

test("detectTarget rejects unsupported architectures", () => {
  assert.throws(() => detectTarget("linux", "ia32"), /unsupported platform/);
});

test("releaseTag always uses a leading v", () => {
  assert.equal(releaseTag("0.1.0"), "v0.1.0");
  assert.equal(releaseTag("v0.1.0"), "v0.1.0");
});

test("normalizedVersion strips the leading v", () => {
  assert.equal(normalizedVersion("v0.1.0"), "0.1.0");
});

test("releaseAssetName matches the release naming convention", () => {
  const asset = releaseAssetName("0.1.0", {
    os: "windows",
    arch: "arm64",
    extension: ".exe",
  });
  assert.equal(asset, "klovys99_0.1.0_windows_arm64.exe");
});

test("releaseAssetUrl points to the GitHub release asset", () => {
  const url = releaseAssetUrl("0.1.0", {
    os: "linux",
    arch: "amd64",
    extension: "",
  });
  assert.equal(
    url,
    "https://github.com/Korbicorp/klovys99/releases/download/v0.1.0/klovys99_0.1.0_linux_amd64",
  );
});

test("binaryFileName uses .exe on Windows", () => {
  assert.equal(binaryFileName("win32"), "klovys99.exe");
  assert.equal(binaryFileName("linux"), "klovys99");
});

test("parseRepository supports git https URLs", () => {
  assert.deepEqual(
    parseRepository("git+https://github.com/Korbicorp/klovys99.git"),
    { owner: "Korbicorp", name: "klovys99" },
  );
});

test("parseRepository falls back to the default repository", () => {
  assert.deepEqual(parseRepository(""), defaultRepository());
});
