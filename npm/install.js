#!/usr/bin/env node
"use strict";

const { execFileSync } = require("child_process");
const fs = require("fs");
const https = require("https");
const path = require("path");

const REPO = "Harvey-AU/hover";
const BIN_DIR = path.join(__dirname, "bin");
const BIN_PATH = path.join(BIN_DIR, "hover");

const PLATFORM_MAP = { darwin: "darwin", linux: "linux" };
const ARCH_MAP = { x64: "amd64", arm64: "arm64" };

function getVersion() {
  const pkg = require("./package.json");
  return pkg.version;
}

function getAssetName() {
  const os = PLATFORM_MAP[process.platform];
  const arch = ARCH_MAP[process.arch];
  if (!os || !arch) {
    throw new Error(
      `Unsupported platform: ${process.platform}-${process.arch}`
    );
  }
  return `hover_${getVersion()}_${os}_${arch}.tar.gz`;
}

function fetch(url) {
  return new Promise((resolve, reject) => {
    https
      .get(url, (res) => {
        if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
          return fetch(res.headers.location).then(resolve, reject);
        }
        if (res.statusCode !== 200) {
          return reject(new Error(`HTTP ${res.statusCode} for ${url}`));
        }
        const chunks = [];
        res.on("data", (c) => chunks.push(c));
        res.on("end", () => resolve(Buffer.concat(chunks)));
        res.on("error", reject);
      })
      .on("error", reject);
  });
}

async function main() {
  const version = getVersion();
  const asset = getAssetName();
  const url = `https://github.com/${REPO}/releases/download/v${version}/${asset}`;

  console.log(`Downloading hover v${version} for ${process.platform}-${process.arch}...`);

  const buffer = await fetch(url);

  // Write tarball to temp file, extract with tar (no shell).
  fs.mkdirSync(BIN_DIR, { recursive: true });
  const tmpFile = path.join(BIN_DIR, "_download.tar.gz");
  fs.writeFileSync(tmpFile, buffer);
  execFileSync("tar", ["xzf", tmpFile, "-C", BIN_DIR], { stdio: "ignore" });
  fs.unlinkSync(tmpFile);

  fs.chmodSync(BIN_PATH, 0o755);
  console.log("hover installed successfully.");
}

main().catch((err) => {
  console.error(`Failed to install hover: ${err.message}`);
  process.exit(1);
});
