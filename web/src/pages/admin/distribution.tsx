import { useEffect, useState } from "react";
import { RefreshCw, PackageOpen, Send, Laptop } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { api, formatBytes, formatTime } from "../../lib/api";
import { useApi } from "../../lib/use-api";
import { useStore } from "../../lib/store";
import type { DshFleetClient, DshPushMode, DshRelease, DshTarget } from "../../lib/types";
import { Badge, Button, Card, PageHeader, ToggleRow, Notice, Empty } from "../../components/ui";
import { LoadState } from "../../components/api-ui";
import { PageLink } from "../../lib/menus";
import { ImportReleaseDialog } from "./distribution-import";

function ReleaseTable({ target, title, rows, busy, highlighted, act }: { target: DshTarget; title: string; rows: DshRelease[]; busy: number; highlighted: number; act: (id: number, action: "mirror" | "recommend" | "delete") => void }) {
  const { t } = useTranslation();
  return (
    <Card className="overflow-hidden">
      <div className="border-b border-neutral-100 px-5 py-3 text-[15px] leading-6 font-semibold text-neutral-900">{title}</div>
      <div className="overflow-x-auto">
        <table className="tbl max-sm:min-w-[34rem]">
          <thead>
            <tr>
              <th className="px-5 py-2">{t("distributionAdmin.colRelease")}</th>
              <th className="px-5 py-2">{t("distributionAdmin.colDate")}</th>
              <th className="px-5 py-2">{t("distributionAdmin.colSize")}</th>
              <th className="px-5 py-2">{t("distributionAdmin.colState")}</th>
              <th className="px-5 py-2">{t("distributionAdmin.colActions")}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-neutral-100">
            {rows.filter((r) => r.target === target).map((r) => {
              const progress = r.size > 0 ? Math.min(100, ((r.downloaded ?? 0) / r.size) * 100) : 0;
              return (
                <tr key={r.id} id={`release-${r.id}`} className={highlighted === r.id ? "bg-emerald-50 ring-2 ring-inset ring-emerald-300" : "transition-colors"}>
                  <td className="px-5 py-3">
                    <div className="font-mono font-medium text-neutral-800">{r.version} · {r.kind}</div>
                    {r.recommended && <Badge tone="brand">{t("download.recommended")}</Badge>}
                    {r.source === "import" && <Badge tone="slate">{t("distributionAdmin.imported")}</Badge>}
                    {r.error && <div className="mt-1 max-w-md text-xs text-red-600">{r.error}</div>}
                  </td>
                  <td className="px-5 py-3 text-neutral-500">{formatTime(r.released_at)}</td>
                  <td className="px-5 py-3 text-neutral-500">{r.size > 0 ? formatBytes(r.size) : "—"}</td>
                  <td className="px-5 py-3">
                    <Badge tone={(r.state ?? "available") === "mirrored" ? "green" : (r.state ?? "available") === "failed" ? "red" : (r.state ?? "available") === "downloading" ? "amber" : "slate"}>
                      {t(`distributionAdmin.state.${(r.state ?? "available")}`)}
                    </Badge>
                    {(r.state ?? "available") === "downloading" && <div className="mt-1 text-xs text-neutral-500">{t("distributionAdmin.progress", { downloaded: formatBytes(r.downloaded ?? 0), pct: progress.toFixed(0) })}</div>}
                  </td>
                  <td className="px-5 py-3">
                    <div className="flex flex-wrap gap-2">
                      {r.source !== "import" && ((r.state ?? "available") === "available" || (r.state ?? "available") === "failed" || r.state === "removed") && (
                        <Button size="sm" variant="secondary" disabled={busy === r.id} onClick={() => act(r.id, "mirror")}>{t("distributionAdmin.mirror")}</Button>
                      )}
                      {(r.state ?? "available") === "mirrored" && !r.recommended && (
                        <>
                          <Button size="sm" variant="secondary" disabled={busy === r.id} onClick={() => act(r.id, "recommend")}>{t("distributionAdmin.recommend")}</Button>
                          <Button size="sm" variant="danger" disabled={busy === r.id} onClick={() => act(r.id, "delete")}>{t("common.delete")}</Button>
                        </>
                      )}
                    </div>
                  </td>
                </tr>
              );
            })}
            {!rows.some((r) => r.target === target) && (
              <tr><td colSpan={5}><div className="tbl-empty"><Empty compact icon={PackageOpen}>{t("distributionAdmin.empty")}</Empty></div></td></tr>
            )}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

export default function AdminDistribution() {
  const { t } = useTranslation();
  const { settings, updateSettings, notify } = useStore();
  const { data, loading, error, reload } = useApi(api.adminDshReleases);
  const fleet = useApi(api.adminDshReleaseClients);
  const [busy, setBusy] = useState(0);
  const [importOpen, setImportOpen] = useState(false);
  const [highlighted, setHighlighted] = useState(0);
  const [importNotice, setImportNotice] = useState("");
  const [pushMode, setPushMode] = useState<DshPushMode>(settings.distribution.pushMode ?? "notify");
  const [pushNote, setPushNote] = useState(settings.distribution.pushNote ?? "");
  const [savingPush, setSavingPush] = useState(false);
  const rows = data ?? [];
  const downloading = rows.some((r) => (r.state ?? "available") === "downloading");

  useEffect(() => {
    setPushMode(settings.distribution.pushMode ?? "notify");
    setPushNote(settings.distribution.pushNote ?? "");
  }, [settings.distribution.pushMode, settings.distribution.pushNote]);

  const savePush = async () => {
    setSavingPush(true);
    try {
      const res = await api.updateDshReleasePush({ push_mode: pushMode, push_note: pushNote.trim() });
      setPushMode(res.push_mode);
      setPushNote(res.push_note);
      notify(t("distributionAdmin.pushSaved"));
      void fleet.reload();
    } catch (e) {
      notify(e instanceof Error ? e.message : String(e));
    } finally {
      setSavingPush(false);
    }
  };

  useEffect(() => {
    if (!highlighted || !data?.some((r) => r.id === highlighted)) return;
    document.getElementById(`release-${highlighted}`)?.scrollIntoView({ behavior: "smooth", block: "center" });
    const timer = setTimeout(() => setHighlighted(0), 6000);
    return () => clearTimeout(timer);
  }, [highlighted, data]);

  useEffect(() => {
    if (!downloading) return;
    const timer = setInterval(reload, 2000);
    return () => clearInterval(timer);
  }, [downloading, reload]);

  const run = async (id: number, action: "mirror" | "recommend" | "delete") => {
    setBusy(id);
    try {
      if (action === "mirror") await api.mirrorDshRelease(id);
      if (action === "recommend") await api.recommendDshRelease(id);
      if (action === "delete") await api.deleteDshRelease(id);
      notify(t(`distributionAdmin.done.${action}`));
      await reload();
      if (action === "recommend") void fleet.reload();
    } catch (e) {
      notify(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(0);
    }
  };

  return (
    <div>
      <PageHeader
        title={t("distributionAdmin.title")}
        desc={t("distributionAdmin.desc")}
        actions={<><Button onClick={() => setImportOpen(true)}><PackageOpen size={14} />{t("distributionAdmin.importOffline")}</Button><Button variant="secondary" onClick={async () => { await api.checkDshReleases(); notify(t("distributionAdmin.checkStarted")); }}><RefreshCw size={14} /> {t("distributionAdmin.checkOfficial")}</Button></>}
      />
      {!settings.distribution.enabled && (
        <Notice tone="warn" className="mb-6">
          <Trans i18nKey="distributionAdmin.disabledNote" components={{ link: <PageLink page="admin-settings" suffix="#settings-distribution" className="mx-1 font-medium underline underline-offset-2" /> }} />
          <PageLink page="admin-settings" suffix="#settings-distribution" className="mt-2 block w-fit rounded-md border border-amber-300 px-3 py-1.5 text-sm font-medium">{t("distributionAdmin.enableDistribution")}</PageLink>
        </Notice>
      )}
      {importNotice && <Notice className="mb-6">{importNotice}</Notice>}
      <Card className="mb-6 grid gap-5 p-5 md:grid-cols-3">
        <label className="text-sm">
          <span className="mb-1 block font-medium text-neutral-800">{t("distributionAdmin.autoSync")}</span>
          <select
            aria-label={t("distributionAdmin.autoSync")}
            className="h-9 w-full rounded-md border border-neutral-300 bg-card px-3"
            value={settings.distribution.autoSync}
            onChange={(e) => updateSettings((s) => ({ ...s, distribution: { ...s.distribution, autoSync: e.target.value as typeof s.distribution.autoSync } }))}
          >
            <option value="off">{t("distributionAdmin.sync.off")}</option>
            <option value="notify">{t("distributionAdmin.sync.notify")}</option>
            <option value="mirror">{t("distributionAdmin.sync.mirror")}</option>
          </select>
        </label>
        <label className="text-sm">
          <span className="mb-1 block font-medium text-neutral-800">{t("distributionAdmin.keepVersions")}</span>
          <input
            aria-label={t("distributionAdmin.keepVersions")}
            type="number"
            min={1}
            max={10}
            className="h-9 w-full rounded-md border border-neutral-300 bg-card px-3"
            value={settings.distribution.keepVersions}
            onChange={(e) => updateSettings((s) => ({ ...s, distribution: { ...s.distribution, keepVersions: Number(e.target.value) } }))}
          />
        </label>
        <ToggleRow
          label={t("distributionAdmin.offerDmg")}
          desc={t("distributionAdmin.offerDmgDesc")}
          checked={settings.distribution.offerDmg}
          onChange={(v) => updateSettings((s) => ({ ...s, distribution: { ...s.distribution, offerDmg: v } }))}
        />
      </Card>
      <Card className="mb-6 p-5">
        <div className="mb-4 flex flex-wrap items-start justify-between gap-3">
          <div>
            <h2 className="flex items-center gap-2 text-[15px] leading-6 font-semibold text-neutral-900">
              <Send size={15} /> {t("distributionAdmin.pushTitle")}
            </h2>
            <p className="mt-1 text-sm text-neutral-500">{t("distributionAdmin.pushDesc")}</p>
          </div>
          <Button size="sm" disabled={savingPush} onClick={() => void savePush()}>
            {savingPush ? t("common.saving") : t("distributionAdmin.pushSave")}
          </Button>
        </div>
        <div className="grid gap-4 md:grid-cols-3">
          <label className="text-sm">
            <span className="mb-1 block font-medium text-neutral-800">{t("distributionAdmin.pushModeLabel")}</span>
            <select
              aria-label={t("distributionAdmin.pushModeLabel")}
              className="h-9 w-full rounded-md border border-neutral-300 bg-card px-3"
              value={pushMode}
              onChange={(e) => setPushMode(e.target.value as DshPushMode)}
            >
              <option value="off">{t("distributionAdmin.pushMode.off")}</option>
              <option value="notify">{t("distributionAdmin.pushMode.notify")}</option>
              <option value="auto_download">{t("distributionAdmin.pushMode.auto_download")}</option>
            </select>
          </label>
          <label className="text-sm md:col-span-2">
            <span className="mb-1 block font-medium text-neutral-800">{t("distributionAdmin.pushNoteLabel")}</span>
            <input
              aria-label={t("distributionAdmin.pushNoteLabel")}
              type="text"
              maxLength={200}
              placeholder={t("distributionAdmin.pushNotePlaceholder")}
              className="h-9 w-full rounded-md border border-neutral-300 bg-card px-3"
              value={pushNote}
              onChange={(e) => setPushNote(e.target.value)}
            />
          </label>
        </div>
      </Card>
      <LoadState loading={loading && !data} error={error} onRetry={reload}>
        <div className="space-y-6">
          <ReleaseTable target="mac-arm64" title={t("distributionAdmin.arm64")} rows={rows} busy={busy} highlighted={highlighted} act={run} />
          <ReleaseTable target="mac-x64" title={t("distributionAdmin.x64")} rows={rows} busy={busy} highlighted={highlighted} act={run} />
          <ReleaseTable target="win-x64" title={t("distributionAdmin.windows")} rows={rows} busy={busy} highlighted={highlighted} act={run} />
        </div>
      </LoadState>
      <Card className="mt-6 overflow-hidden">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-neutral-100 px-5 py-3">
          <div>
            <div className="flex items-center gap-2 text-[15px] leading-6 font-semibold text-neutral-900">
              <Laptop size={15} /> {t("distributionAdmin.fleetTitle")}
            </div>
            <div className="mt-0.5 text-xs text-neutral-500">{t("distributionAdmin.fleetDesc")}</div>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {fleet.data && (
              <>
                <Badge tone="slate">{t("distributionAdmin.fleetSummaryTotal", { n: fleet.data.summary.total })}</Badge>
                <Badge tone="green">{t("distributionAdmin.fleetSummaryUpToDate", { n: fleet.data.summary.up_to_date })}</Badge>
                <Badge tone="amber">{t("distributionAdmin.fleetSummaryOutdated", { n: fleet.data.summary.outdated })}</Badge>
                {fleet.data.summary.unknown > 0 && <Badge tone="slate">{t("distributionAdmin.fleetSummaryUnknown", { n: fleet.data.summary.unknown })}</Badge>}
              </>
            )}
            <Button size="sm" variant="secondary" onClick={() => void fleet.reload()}>
              <RefreshCw size={13} /> {t("distributionAdmin.fleetRefresh")}
            </Button>
          </div>
        </div>
        <div className="overflow-x-auto">
          <table className="tbl max-sm:min-w-[38rem]">
            <thead>
              <tr>
                <th className="px-5 py-2">{t("distributionAdmin.fleetColUser")}</th>
                <th className="px-5 py-2">{t("distributionAdmin.fleetColClient")}</th>
                <th className="px-5 py-2">{t("distributionAdmin.fleetColVersion")}</th>
                <th className="px-5 py-2">{t("distributionAdmin.fleetColRecommended")}</th>
                <th className="px-5 py-2">{t("distributionAdmin.fleetColStatus")}</th>
                <th className="px-5 py-2">{t("distributionAdmin.fleetColLastSeen")}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-neutral-100">
              {(fleet.data?.clients ?? []).map((c: DshFleetClient, idx: number) => (
                <tr key={`${c.username}-${c.client_name}-${idx}`}>
                  <td className="px-5 py-3">
                    <div className="font-medium text-neutral-800">{c.username}</div>
                    {c.department && <div className="text-xs text-neutral-500">{c.department}</div>}
                  </td>
                  <td className="px-5 py-3 text-neutral-700">
                    <div>{c.client_name || "DSH"}</div>
                    {c.last_client && <div className="font-mono text-xs text-neutral-400">{c.last_client}</div>}
                  </td>
                  <td className="px-5 py-3 font-mono text-xs text-neutral-700">
                    {c.host_kind === "desktop" ? (
                      <>
                        <div>{c.version || "—"}</div>
                        {c.target && <div className="text-neutral-400">{c.target}</div>}
                      </>
                    ) : c.last_host || "—"}
                  </td>
                  <td className="px-5 py-3 font-mono text-xs text-neutral-600">{c.recommended_version || "—"}</td>
                  <td className="px-5 py-3">
                    <Badge tone={c.status === "up_to_date" ? "green" : c.status === "outdated" ? "amber" : "slate"}>
                      {t(`distributionAdmin.fleetStatus.${c.status}`)}
                    </Badge>
                  </td>
                  <td className="px-5 py-3 text-xs text-neutral-500">{formatTime(c.last_used_at)}</td>
                </tr>
              ))}
              {!fleet.loading && !(fleet.data?.clients?.length) && (
                <tr><td colSpan={6}><div className="tbl-empty"><Empty compact icon={Laptop}>{t("distributionAdmin.fleetEmpty")}</Empty></div></td></tr>
              )}
            </tbody>
          </table>
        </div>
      </Card>
      {importOpen && <ImportReleaseDialog releases={rows} onClose={() => setImportOpen(false)} onSuccess={(release) => {
        setImportOpen(false);
        const message = t("distributionAdmin.importSuccess", { platform: t(`download.target.${release.target}`), version: release.version });
        setImportNotice(message);
        notify(message);
        setHighlighted(release.id);
        void reload();
      }} />}
    </div>
  );
}
