import { useEffect, useState, type FormEvent } from "react";
import { Link, Navigate, useNavigate, useSearchParams } from "react-router";
import { WhaleMark } from "../components/whale-mark";
import { useAuth } from "../lib/auth";
import { api } from "../lib/api";
import { useApi } from "../lib/use-api";
import { Button, buttonCls, inputCls, Notice, PasswordInput, Spinner } from "../components/ui";
import { ErrorNote } from "../components/api-ui";
import { LangSwitch } from "../components/lang-switch";
import { ThemeToggle } from "../components/theme-toggle";
import { useTranslation } from "react-i18next";

/** 只允许站内相对路径，防止 next 参数被拿来做开放重定向。 */
function safeNext(raw: string | null) {
  return raw && raw.startsWith("/") && !raw.startsWith("//") ? raw : "/go";
}

/** 每 30 秒刷新一次当前时间，给演示站「下次重置」倒计时用。 */
function useNow(intervalMs: number) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), intervalMs);
    return () => window.clearInterval(id);
  }, [intervalMs]);
  return now;
}

/** 把剩余毫秒说成人话，例如「2 小时 15 分钟」/「2 hours 15 minutes」。 */
function remainingLabel(ms: number, lang: string) {
  const minutes = Math.max(1, Math.ceil(ms / 60000));
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  const fmt = (n: number, unit: "hour" | "minute") =>
    new Intl.NumberFormat(lang, { style: "unit", unit, unitDisplay: "long" }).format(n);
  if (hours > 0 && rest > 0) return `${fmt(hours, "hour")} ${fmt(rest, "minute")}`;
  if (hours > 0) return fmt(hours, "hour");
  return fmt(rest, "minute");
}

/** 只显示时钟（服务端给的是 RFC3339，会带上本地时区）。 */
function clockLabel(date: Date, lang: string) {
  return date.toLocaleTimeString(lang, { hour: "2-digit", minute: "2-digit" });
}

export default function Login() {
  const { t, i18n } = useTranslation();
  const { user, loading, error: bootError, login } = useAuth();
  const now = useNow(30_000);
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const next = safeNext(params.get("next"));
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const methods = useApi(api.authMethods);
  const ldap = !!methods.data?.ldap;
  const sso = methods.data?.oidc ?? null;
  const demo = methods.data?.demo ?? null;
  const resetAt = demo?.next_reset_at ? new Date(demo.next_reset_at) : null;
  const resetIn = resetAt && !Number.isNaN(resetAt.getTime()) ? resetAt.getTime() - now : null;
  // 企业统一登录失败时，回调把原因放在 oidc_error 里带回来
  const ssoError = params.get("oidc_error");
  // 从 DSH 自动登录的会话只带员工权限：带 reauth=1 进来时照常显示登录表单
  const reauth = params.get("reauth") === "1" && !!user?.limited;
  const handoffExpired = params.get("handoff") === "expired";
  useEffect(() => {
    if (reauth && user) setUsername((current) => current || user.username);
  }, [reauth, user]);

  if (!loading && user && !reauth) return <Navigate to={next} replace />;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await login(username.trim(), password);
      navigate(next, { replace: true });
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="auth-bg relative flex min-h-screen flex-col items-center justify-center overflow-hidden px-4 py-10">
      <div aria-hidden className="auth-stripe pointer-events-none absolute inset-x-0 top-0 h-[52vh] min-h-80 dark:opacity-80" />
      <Link to="/" className="relative mb-7 flex items-center gap-2.5 rounded-md text-white">
        <WhaleMark size={36} />
        <span className="text-xl font-bold tracking-[-0.01em]">dsh-ee</span>
      </Link>
      <form onSubmit={submit} className="relative w-full max-w-[400px] rounded-xl bg-card p-7 shadow-modal sm:p-8">
        <h1 className="text-[22px] leading-7 font-bold tracking-[-0.02em] text-neutral-900">{t("login.title")}</h1>
        <p className="mt-1.5 text-sm leading-6 text-neutral-600">{ldap ? t("login.ldapHint") : t("login.subtitle")}</p>
        {(reauth || handoffExpired) && (
          <Notice role="status" className="mt-4">
            {t(reauth ? "login.reauth" : "login.handoffExpired")}
          </Notice>
        )}
        {demo && (
          <Notice role="status" className="mt-4">
            <p className="font-medium">{t("login.demo.title")}</p>
            <p className="mt-1 font-mono text-[13px]">{t("login.demo.account", { username: demo.username, password: demo.password })}</p>
            {demo.reset && <p className="mt-1">{t("login.demo.reset", { reset: demo.reset })}</p>}
            {resetAt && resetIn !== null && (
              <p className="mt-1">
                {resetIn > 0
                  ? t("login.demo.nextReset", { time: remainingLabel(resetIn, i18n.language), at: clockLabel(resetAt, i18n.language) })
                  : t("login.demo.resetting")}
              </p>
            )}
            <Button
              type="button"
              size="sm"
              variant="secondary"
              className="mt-2"
              onClick={() => {
                setUsername(demo.username);
                setPassword(demo.password);
              }}
            >
              {t("login.demo.fill")}
            </Button>
          </Notice>
        )}
        {sso && (
          <>
            <a href={`/api/auth/oidc/start?next=${encodeURIComponent(next)}`} className={buttonCls("primary", "lg", "mt-6 w-full")}>
              {t("ssoLogin.button", { name: sso.name })}
            </a>
            <div className="mt-5 flex items-center gap-3 text-xs text-neutral-500">
              <span className="h-px flex-1 bg-neutral-200" />
              {t("ssoLogin.or")}
              <span className="h-px flex-1 bg-neutral-200" />
            </div>
          </>
        )}
        <div className={sso ? "mt-4 space-y-4" : "mt-6 space-y-4"}>
          <label className="block">
            <span className="mb-1.5 block text-[13px] font-medium text-neutral-700">{t("login.username")}</span>
            <input
              className={inputCls}
              autoComplete="username"
              autoFocus
              required
              value={username}
              onChange={(e) => setUsername(e.target.value)}
            />
          </label>
          <label className="block">
            <span className="mb-1.5 block text-[13px] font-medium text-neutral-700">{t("login.password")}</span>
            <PasswordInput
              label={t("login.password")}
              className={inputCls}
              autoComplete="current-password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </label>
        </div>
        {(error ?? bootError ?? ssoError) && (
          <div className="mt-4">
            <ErrorNote>{error ?? bootError ?? t("ssoLogin.failed", { error: ssoError })}</ErrorNote>
          </div>
        )}
        <Button type="submit" size="lg" variant={sso ? "secondary" : "primary"} className="mt-6 w-full" disabled={busy || !username || !password}>
          {busy && <Spinner />}
          {busy ? t("login.submitting") : t("login.submit")}
        </Button>
      </form>
      <p className="relative mt-6 text-center text-[13px] text-neutral-500">{ldap ? t("login.ldapForgot") : t("login.forgot")}</p>
      <div className="relative mt-3 flex items-center justify-center gap-2">
        <LangSwitch />
        <ThemeToggle />
      </div>
    </div>
  );
}
