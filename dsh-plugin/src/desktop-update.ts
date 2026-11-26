import { execFile } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import { createWriteStream } from "node:fs";
import { mkdir, mkdtemp, rename, rm, stat } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { compareVersions } from "./doctor.js";
import type { PluginConfig } from "./dsh.js";
import type { HttpClient } from "./http.js";
import { t } from "./i18n/index.js";

export type DesktopTarget = "mac-arm64" | "mac-x64" | "win-x64";

export interface DesktopReleaseInfo {
  id: number;
  target: DesktopTarget;
  kind: "zip" | "dmg" | "exe";
  version: string;
  file: string;
  size: number;
  sha512: string;
  verified: boolean;
  source: "official" | "import";
  released_at: string;
  download_url: string;
}

export interface DesktopUpdateCatalog {
  mode: "off" | "notify" | "auto_download";
  note?: string;
  releases?: Record<string, DesktopReleaseInfo>;
}

export type DesktopDownloadState = "idle" | "downloading" | "ready" | "error";

export interface DesktopUpdateStatus {
  mode: "off" | "notify" | "auto_download";
  note: string;
  target: DesktopTarget | null;
  currentVersion: string | null;
  available?: DesktopReleaseInfo;
  updateAvailable: boolean;
  downloadState: DesktopDownloadState;
  downloaded: number;
  total: number;
  localPath?: string;
  error?: string;
  downloadPageUrl: string;
}

export function detectDesktopTarget(
  platform: string = process.platform,
  arch: string = process.arch,
): DesktopTarget | null {
  if (platform === "darwin" && arch === "arm64") return "mac-arm64";
  if (platform === "darwin" && arch === "x64") return "mac-x64";
  if (platform === "win32" && arch === "x64") return "win-x64";
  return null;
}

export function detectDesktopVersion(
  versions: Record<string, string | undefined> = process.versions as unknown as Record<string, string | undefined>,
  env: Record<string, string | undefined> = process.env,
): string | null {
  const explicit = (env.DSH_DESKTOP_VERSION || env.DSH_VERSION || "").trim();
  if (explicit) return explicit;
  if (!versions.electron) return null;
  return null;
}

function normalizeSha512(expected: string): Buffer | null {
  const raw = expected.trim().replace(/^sha512:/i, "").replace(/\s+/g, "");
  if (!raw) return null;
  const b64 = Buffer.from(raw, "base64");
  if (b64.length === 64 && b64.toString("base64").replace(/=+$/, "") === raw.replace(/=+$/, "")) {
    return b64;
  }
  if (/^[0-9a-fA-F]{128}$/.test(raw)) {
    return Buffer.from(raw, "hex");
  }
  return null;
}

function safeFileName(file: string): string {
  const base = path.basename(file);
  if (!base || base === "." || base === "..") return "dsh-installer";
  return base;
}

function safeVersionDir(version: string): string {
  const cleaned = version.trim().replace(/[^a-zA-Z0-9._-]+/g, "_");
  return cleaned && cleaned !== "." && cleaned !== ".." ? cleaned : "latest";
}

async function resolveNonOverwritingPath(dir: string, fileName: string): Promise<string> {
  const ext = path.extname(fileName);
  const stem = ext ? fileName.slice(0, -ext.length) : fileName;
  for (let i = 0; i < 100; i++) {
    const candidate = path.join(dir, i === 0 ? fileName : `${stem}-${i + 1}${ext}`);
    try {
      await stat(candidate);
    } catch {
      return candidate;
    }
  }
  return path.join(dir, `${stem}-${randomUUID()}${ext}`);
}

export interface DesktopUpdateOptions {
  platform?: string;
  arch?: string;
  versions?: Record<string, string | undefined>;
  env?: Record<string, string | undefined>;
  downloadsDir?: string;
  openFile?: (filePath: string) => void;
}

export class DesktopUpdateManager {
  private readonly target: DesktopTarget | null;
  private readonly currentVersion: string | null;
  private readonly downloadsDir: string;
  private readonly openFileImpl: (filePath: string) => void;

  private mode: "off" | "notify" | "auto_download" = "off";
  private note = "";
  private available: DesktopReleaseInfo | undefined;
  private downloadState: DesktopDownloadState = "idle";
  private downloaded = 0;
  private total = 0;
  private localPath: string | undefined;
  private error: string | undefined;
  private downloadedKey = "";
  private activeDownload: Promise<DesktopUpdateStatus> | null = null;

  constructor(
    private readonly config: Pick<PluginConfig, "base">,
    private readonly http: HttpClient,
    options: DesktopUpdateOptions = {},
  ) {
    const platform = options.platform ?? process.platform;
    const arch = options.arch ?? process.arch;
    this.target = detectDesktopTarget(platform, arch);
    this.currentVersion = detectDesktopVersion(options.versions, options.env);
    this.downloadsDir = options.downloadsDir ?? path.join(os.homedir(), "Downloads");
    this.openFileImpl = options.openFile ?? ((filePath: string) => {
      if (platform === "darwin") execFile("open", ["-R", filePath], () => {});
      else if (platform === "win32") execFile("explorer", [`/select,${filePath}`], () => {});
      else execFile("xdg-open", [path.dirname(filePath)], () => {});
    });
  }

  private releaseKey(rel?: DesktopReleaseInfo): string {
    return rel ? `${rel.target}:${rel.version}:${rel.sha512}:${rel.file}` : "";
  }

  private isUpdateAvailable(rel?: DesktopReleaseInfo): boolean {
    if (!rel || this.mode === "off") return false;
    if (!this.currentVersion) return false;
    return compareVersions(rel.version, this.currentVersion) > 0;
  }

  applyCatalog(catalog?: DesktopUpdateCatalog): void {
    if (!catalog || catalog.mode === "off") {
      this.mode = "off";
      this.note = "";
      this.available = undefined;
      return;
    }
    this.mode = catalog.mode === "auto_download" ? "auto_download" : "notify";
    this.note = (catalog.note ?? "").trim();
    const next = this.target ? catalog.releases?.[this.target] : undefined;
    const prevKey = this.releaseKey(this.available);
    const nextKey = this.releaseKey(next);
    this.available = next;
    if (prevKey !== nextKey && this.downloadedKey !== nextKey && !this.activeDownload) {
      this.downloadState = "idle";
      this.downloaded = 0;
      this.total = next?.size ?? 0;
      this.localPath = undefined;
      this.error = undefined;
    }
    if (
      this.mode === "auto_download" &&
      this.isUpdateAvailable(this.available) &&
      this.downloadedKey !== nextKey &&
      !this.activeDownload
    ) {
      void this.download().catch(() => undefined);
    }
  }

  status(): DesktopUpdateStatus {
    const updateAvailable = this.isUpdateAvailable(this.available);
    return {
      mode: this.mode,
      note: this.note,
      target: this.target,
      currentVersion: this.currentVersion,
      available: this.available,
      updateAvailable,
      downloadState: this.downloadState,
      downloaded: this.downloaded,
      total: this.total || this.available?.size || 0,
      localPath: this.localPath,
      error: this.error,
      downloadPageUrl: `${this.config.base}/dsh`,
    };
  }

  async download(): Promise<DesktopUpdateStatus> {
    if (this.activeDownload) return this.activeDownload;
    const rel = this.available;
    if (!rel) {
      this.downloadState = "error";
      this.error = t("desktopUpdate.noRelease");
      return this.status();
    }
    const key = this.releaseKey(rel);
    if (this.downloadedKey === key && this.localPath) {
      try {
        await stat(this.localPath);
        this.downloadState = "ready";
        return this.status();
      } catch {
        // 文件被删了，重新下载
      }
    }
    const task = this.runDownload(rel, key).finally(() => {
      this.activeDownload = null;
    });
    this.activeDownload = task;
    return task;
  }

  private async runDownload(rel: DesktopReleaseInfo, key: string): Promise<DesktopUpdateStatus> {
    this.downloadState = "downloading";
    this.downloaded = 0;
    this.total = rel.size || 0;
    this.error = undefined;
    const fileName = safeFileName(rel.file);
    let targetDir = path.join(this.downloadsDir, "dsh-ee-updates", safeVersionDir(rel.version));
    try {
      await mkdir(targetDir, { recursive: true, mode: 0o700 });
    } catch {
      targetDir = await mkdtemp(path.join(os.tmpdir(), "dsh-ee-updates-"));
    }
    const finalPath = await resolveNonOverwritingPath(targetDir, fileName);
    const partPath = path.join(targetDir, `.dsh-${randomUUID()}.part`);
    try {
      const dlPath = `/dl/${encodeURIComponent(rel.target)}/${encodeURIComponent(rel.file)}`;
      const response = await this.http.fetch(dlPath, {}, 0);
      if (!response.ok || !response.body) {
        throw new Error(t("desktopUpdate.httpFailed", { status: response.status }));
      }
      const headerLen = Number(response.headers.get("content-length") || 0);
      if (headerLen > 0) this.total = headerLen;

      const hash = createHash("sha512");
      const out = createWriteStream(partPath, { flags: "wx", mode: 0o600 });
      let streamErr: Error | null = null;
      out.on("error", (err) => {
        streamErr = err;
      });
      const reader = response.body.getReader();
      try {
        while (true) {
          if (streamErr) throw streamErr;
          const { done, value } = await reader.read();
          if (done) break;
          if (!value?.byteLength) continue;
          if (streamErr) throw streamErr;
          hash.update(value);
          this.downloaded += value.byteLength;
          if (!out.write(value)) {
            await new Promise<void>((resolve, reject) => {
              if (streamErr) return reject(streamErr);
              const onDrain = () => {
                out.off("error", onErr);
                resolve();
              };
              const onErr = (err: Error) => {
                out.off("drain", onDrain);
                reject(err);
              };
              out.once("drain", onDrain);
              out.once("error", onErr);
            });
          }
        }
        if (streamErr) throw streamErr;
        await new Promise<void>((resolve, reject) => {
          if (streamErr) return reject(streamErr);
          const onFinish = () => {
            out.off("error", onErr);
            resolve();
          };
          const onErr = (err: Error) => {
            out.off("finish", onFinish);
            reject(err);
          };
          out.once("finish", onFinish);
          out.once("error", onErr);
          out.end();
        });
      } catch (err) {
        out.destroy();
        throw err;
      }

      const expected = normalizeSha512(rel.sha512);
      if (expected) {
        const actual = hash.digest();
        if (!actual.equals(expected)) {
          throw new Error(t("desktopUpdate.checksumMismatch"));
        }
      }
      await rename(partPath, finalPath);
      this.localPath = finalPath;
      this.downloadedKey = key;
      this.downloadState = "ready";
      this.error = undefined;
      return this.status();
    } catch (err) {
      await rm(partPath, { force: true }).catch(() => undefined);
      this.downloadState = "error";
      this.error = err instanceof Error ? err.message : String(err);
      return this.status();
    }
  }

  async openInstaller(): Promise<{ ok: boolean; path?: string; message: string }> {
    if (!this.localPath) {
      return { ok: false, message: t("desktopUpdate.notDownloaded") };
    }
    try {
      await stat(this.localPath);
    } catch {
      this.downloadState = "idle";
      this.localPath = undefined;
      this.downloadedKey = "";
      return { ok: false, message: t("desktopUpdate.fileMissing") };
    }
    this.openFileImpl(this.localPath);
    return { ok: true, path: this.localPath, message: t("desktopUpdate.opened", { path: this.localPath }) };
  }
}
