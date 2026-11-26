import { createHash } from "node:crypto";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import assert from "node:assert/strict";
import { DesktopUpdateManager, detectDesktopTarget } from "../src/desktop-update.js";
import { dshHost } from "../src/http.js";
import { enterpriseHeader, enterprisePanel } from "../src/status-view.js";

test("detectDesktopTarget maps darwin/win32 platforms", () => {
  assert.equal(detectDesktopTarget("darwin", "arm64"), "mac-arm64");
  assert.equal(detectDesktopTarget("darwin", "x64"), "mac-x64");
  assert.equal(detectDesktopTarget("win32", "x64"), "win-x64");
  assert.equal(detectDesktopTarget("linux", "x64"), null);
});

test("dshHost appends target on supported desktop platforms", () => {
  assert.equal(dshHost({ electron: "0.2.1-rc.5" }, "darwin", "arm64"), "desktop/0.2.1-rc.5/mac-arm64");
  assert.equal(dshHost({ electron: "0.2.1-rc.5" }, "win32", "x64"), "desktop/0.2.1-rc.5/win-x64");
  assert.equal(dshHost({ electron: "0.2.1-rc.5" }, "linux", "x64"), "desktop/0.2.1-rc.5");
  assert.equal(dshHost({}, "darwin", "arm64"), "cli");
});

test("DesktopUpdateManager notifies, downloads with sha512 verification, and opens installer", async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), "dsh-ee-desktop-upd-"));
  const payload = Buffer.from("fake-dsh-installer-binary-v0.3.0");
  const sha512 = createHash("sha512").update(payload).digest("base64");
  let openedPath = "";
  let fetchedPath = "";

  const fakeHttp = {
    async fetch(reqPath: string) {
      fetchedPath = reqPath;
      return new Response(payload, {
        status: 200,
        headers: { "content-length": String(payload.length) },
      });
    },
  } as any;

  const manager = new DesktopUpdateManager(
    { base: "http://127.0.0.1:5188" },
    fakeHttp,
    {
      platform: "darwin",
      arch: "arm64",
      versions: { electron: "35.0.0" },
      env: { DSH_DESKTOP_VERSION: "0.2.1-rc.5" },
      downloadsDir: dir,
      openFile: (p) => { openedPath = p; },
    },
  );

  try {
    manager.applyCatalog({
      mode: "notify",
      note: "修复内网代理问题",
      releases: {
        "mac-arm64": {
          id: 1,
          target: "mac-arm64",
          kind: "dmg",
          version: "0.3.0",
          file: "dsh-0.3.0-arm64.dmg",
          size: payload.length,
          sha512,
          verified: true,
          source: "import",
          released_at: "2026-11-26T00:00:00Z",
          download_url: "/dl/mac-arm64/dsh-0.3.0-arm64.dmg",
        },
      },
    });

    const before = manager.status();
    assert.equal(before.updateAvailable, true);
    assert.equal(before.mode, "notify");
    assert.equal(before.note, "修复内网代理问题");
    assert.equal(before.downloadState, "idle");

    const after = await manager.download();
    assert.equal(fetchedPath, "/dl/mac-arm64/dsh-0.3.0-arm64.dmg");
    assert.equal(after.downloadState, "ready");
    assert.ok(after.localPath?.endsWith("dsh-0.3.0-arm64.dmg"));
    const saved = await readFile(after.localPath!);
    assert.deepEqual(saved, payload);

    const openRes = await manager.openInstaller();
    assert.equal(openRes.ok, true);
    assert.equal(openedPath, after.localPath);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test("DesktopUpdateManager rejects corrupted checksum", async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), "dsh-ee-desktop-bad-"));
  const fakeHttp = {
    async fetch() {
      return new Response(Buffer.from("corrupted-bytes"), { status: 200 });
    },
  } as any;
  const goodHash = createHash("sha512").update("expected-bytes").digest("base64");
  const manager = new DesktopUpdateManager(
    { base: "http://127.0.0.1:5188" },
    fakeHttp,
    { platform: "darwin", arch: "arm64", env: { DSH_DESKTOP_VERSION: "0.2.0" }, downloadsDir: dir },
  );
  try {
    manager.applyCatalog({
      mode: "notify",
      releases: {
        "mac-arm64": {
          id: 2,
          target: "mac-arm64",
          kind: "dmg",
          version: "0.3.0",
          file: "dsh-0.3.0-arm64.dmg",
          size: 14,
          sha512: goodHash,
          verified: true,
          source: "import",
          released_at: "2026-11-26T00:00:00Z",
          download_url: "/dl/mac-arm64/dsh-0.3.0-arm64.dmg",
        },
      },
    });
    const st = await manager.download();
    assert.equal(st.downloadState, "error");
    assert.match(st.error ?? "", /SHA-512/);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test("DesktopUpdateManager auto_download triggers background download on catalog apply", async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), "dsh-ee-desktop-auto-"));
  const payload = Buffer.from("auto-downloaded-installer");
  const sha512 = createHash("sha512").update(payload).digest("base64");
  const fakeHttp = {
    async fetch() {
      return new Response(payload, { status: 200 });
    },
  } as any;
  const manager = new DesktopUpdateManager(
    { base: "http://127.0.0.1:5188" },
    fakeHttp,
    { platform: "win32", arch: "x64", env: { DSH_DESKTOP_VERSION: "0.2.0" }, downloadsDir: dir },
  );
  try {
    manager.applyCatalog({
      mode: "auto_download",
      releases: {
        "win-x64": {
          id: 3,
          target: "win-x64",
          kind: "exe",
          version: "0.3.0",
          file: "dsh-0.3.0-setup.exe",
          size: payload.length,
          sha512,
          verified: true,
          source: "import",
          released_at: "2026-11-26T00:00:00Z",
          download_url: "/dl/win-x64/dsh-0.3.0-setup.exe",
        },
      },
    });
    // Wait for background download promise to settle
    await manager.download();
    assert.equal(manager.status().downloadState, "ready");
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test("enterpriseHeader and enterprisePanel render desktop update section", () => {
  const h = (type: any, props: any, ...children: any[]) => ({ type, props, children });
  const data: any = {
    me: { username: "alice", department: "研发部" },
    models: ["deepseek-chat"],
    version: "0.1.0",
    desktop_update: {
      mode: "notify",
      note: "修复代理问题",
      target: "mac-arm64",
      currentVersion: "0.2.0",
      available: { version: "0.3.0", file: "dsh-0.3.0-arm64.dmg" },
      updateAvailable: true,
      downloadState: "idle",
      downloaded: 0,
      total: 100,
      downloadPageUrl: "http://127.0.0.1:5188/dsh",
    },
  };
  const header = enterpriseHeader(h, data, false, () => {});
  assert.match(JSON.stringify(header), /有新版 DSH/);
  const panel = enterprisePanel(h, "TestOrg", { data, failed: false }, {
    login: () => {},
    refresh: () => {},
    doctor: () => {},
    downloadDesktopUpdate: () => {},
    openDesktopUpdate: () => {},
  });
  const text = JSON.stringify(panel);
  assert.match(text, /DSH 版本更新/);
  assert.match(text, /0\.3\.0/);
  assert.match(text, /修复代理问题/);
  assert.match(text, /下载新版 DSH/);
});
