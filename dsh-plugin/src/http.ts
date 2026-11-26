import type { TokenManager } from "./token.js";
import { detectDesktopTarget } from "./desktop-update.js";

export function dshHost(
  versions: Record<string, string | undefined> = process.versions as unknown as Record<string, string | undefined>,
  platform: string = process.platform,
  arch: string = process.arch,
): string {
  const electron = versions.electron;
  if (!electron) return "cli";
  const ver = process.env.DSH_DESKTOP_VERSION || process.env.DSH_VERSION || electron;
  const target = detectDesktopTarget(platform, arch);
  return target ? `desktop/${ver}/${target}` : `desktop/${ver}`;
}

export class HttpClient {
  constructor(private tokens: TokenManager) {}

  async fetch(path: string, init: RequestInit = {}, timeoutMs = 30_000): Promise<Response> {
    const current = await this.tokens.resolve();
    const headers = new Headers(init.headers);
    if (current?.value) headers.set("Authorization", `Bearer ${current.value}`);
    const config = (this.tokens as any).config;
    if (!headers.has("X-DshEE-Client")) headers.set("X-DshEE-Client", `dsh-connect/${config.version || "dev"}`);
    if (!headers.has("X-DshEE-Host")) headers.set("X-DshEE-Host", dshHost());
    const timeout = timeoutMs > 0 ? AbortSignal.timeout(timeoutMs) : undefined;
    const signal = init.signal && timeout ? AbortSignal.any([init.signal, timeout]) : (timeout ?? init.signal ?? undefined);
    const response = await fetch(`${config.base}${path}`, { ...init, headers, signal });
    if (response.status === 401 && current?.value) {
      const body = await response.clone().json().catch(() => null) as { code?: string } | null;
      if (body?.code?.startsWith("token_")) await this.tokens.invalidate(current.value);
    }
    return response;
  }

  async json<T>(path: string, init?: RequestInit): Promise<T> {
    const response = await this.fetch(path, init);
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    return response.json() as Promise<T>;
  }
}
