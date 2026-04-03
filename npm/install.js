#!/usr/bin/env node
"use strict";

const { execFileSync } = require("child_process");
const fs = require("fs");
const https = require("https");
const path = require("path");

const REPO = "Harvey-AU/hover";
const BIN_DIR = path.join(__dirname, "bin");
const BIN_NAME = process.platform === "win32" ? "hover.exe" : "hover";
const BIN_PATH = path.join(BIN_DIR, BIN_NAME);

const PLATFORM_MAP = { darwin: "darwin", linux: "linux", win32: "windows" };
const ARCH_MAP = { x64: "amd64", arm64: "arm64" };

function getVersion() {
  const pkg = require("./package.json");
  return pkg.version;
}

function isWindows() {
  return process.platform === "win32";
}

function getAssetName() {
  const os = PLATFORM_MAP[process.platform];
  const arch = ARCH_MAP[process.arch];
  if (!os || !arch) {
    throw new Error(
      `Unsupported platform: ${process.platform}-${process.arch}`
    );
  }
  const ext = isWindows() ? "zip" : "tar.gz";
  return `hover_${getVersion()}_${os}_${arch}.${ext}`;
}

function fetch(url) {
  return new Promise((resolve, reject) => {
    https
      .get(url, (res) => {
        if (
          res.statusCode >= 300 &&
          res.statusCode < 400 &&
          res.headers.location
        ) {
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

  console.log(
    `Downloading hover v${version} for ${process.platform}-${process.arch}...`
  );

  const buffer = await fetch(url);

  // Write archive to temp file and extract.
  fs.mkdirSync(BIN_DIR, { recursive: true });

  if (isWindows()) {
    const tmpFile = path.join(BIN_DIR, "_download.zip");
    fs.writeFileSync(tmpFile, buffer);
    execFileSync(
      "powershell",
      [
        "-NoProfile",
        "-Command",
        `Expand-Archive -Force -Path '${tmpFile}' -DestinationPath '${BIN_DIR}'`,
      ],
      { stdio: "ignore" }
    );
    fs.unlinkSync(tmpFile);
  } else {
    const tmpFile = path.join(BIN_DIR, "_download.tar.gz");
    fs.writeFileSync(tmpFile, buffer);
    execFileSync("tar", ["xzf", tmpFile, "-C", BIN_DIR], { stdio: "ignore" });
    fs.unlinkSync(tmpFile);
    fs.chmodSync(BIN_PATH, 0o755);
  }
  console.log("hover installed successfully.");
}

main().catch((err) => {
  console.error(`Failed to install hover: ${err.message}`);
  process.exit(1);
});
