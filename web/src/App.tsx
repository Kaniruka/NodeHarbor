import { useEffect, useState } from "react";
import "./styles.css";
import UpstreamSubscriptions from "./UpstreamSubscriptions";

type Locale = "zh-CN" | "en";
type HealthState = "loading" | "healthy" | "unhealthy";

type Health = {
  status: HealthState;
  backend: { status: HealthState };
  database: { status: HealthState };
  publishedSubscription: { status: HealthState };
};

type ListenerSettings = {
  listenAddress?: string;
  listenPort?: number;
  localSubscriptionURL?: string;
  subscriptionURL?: string;
  lanSubscriptionURLs?: string[];
  listenerError?: string;
};

const messages = {
  "zh-CN": {
    eyebrow: "本地代理订阅管家",
    title: "NodeHarbor 控制台",
    subtitle: "从这里确认系统已准备好安全地整理与发布代理订阅。",
    health: "系统健康",
    healthHint: "实时检查本地服务的关键环节",
    backend: "后端",
    database: "数据库",
    publication: "已发布订阅",
    healthy: "健康",
    unhealthy: "异常",
    loading: "检查中",
    subscription: "订阅地址",
    subscriptionHint: "即使尚无代理节点，该地址也始终提供有效配置。",
    localSubscription: "本地订阅地址",
    currentSubscription: "当前访问入口",
    lanSubscriptions: "局域网订阅地址",
    noLanSubscriptions: "当前监听地址未提供可识别的 LAN 地址。",
    listener: "监听诊断",
    listenerHint: "管理路由仅接受回环请求；Published Subscription 可按监听地址提供给可信局域网。",
    copy: "复制地址",
    copied: "已复制",
    language: "语言",
    switchLanguage: "English",
    loadError: "无法读取系统健康状态，请确认后端正在运行。",
    evaluation: "评估运行",
    startEvaluation: "开始评估",
    ignoreCache: "本轮忽略评分缓存",
    scoreCache: "复用 24 小时内缓存评分",
    scoreProvider: "本轮请求评分源",
    running: "运行中",
    completed: "已完成",
    failed: "失败",
    paused: "已暂停",
    idle: "尚未运行",
    summary: (passed: number, total: number) => `${passed} / ${total} 个节点通过`,
    provider: "评分源",
    threshold: "合格阈值",
    iplarkThreshold: "IPLark 合格阈值",
    ipcheckThreshold: "IPCheck.ing 合格阈值",
    ipcheck: "IPCheck.ing",
    iplark: "IPLark",
    providerReady: "可用",
    providerUnavailable: "不可用",
    providerDisabled: "已禁用",
    saveSettings: "保存评分设置",
    interval: "自动运行间隔（分钟）",
    retention: "历史保留天数",
    availabilityAttempts: "探测次数",
    availabilityRequired: "最低成功次数",
    availabilityTimeout: "单次超时（秒）",
    availabilityMaxLatency: "最大中位延迟（毫秒）",
    availabilityURLs: "探测地址（逗号分隔）",
    evaluationWorkers: "并发节点数",
    scoringJitter: "评分抖动上限（毫秒）",
    saveAvailabilitySettings: "保存可用性设置",
    probeSuccess: (successful: number, attempts: number) => `${successful} / ${attempts} 次探测成功`,
    history: "运行历史",
    historyHint: "查看保留窗口内的 Evaluation Run 和诊断信息",
    exportConfig: "导出配置 JSON",
    trigger: "触发",
    phase: "阶段",
    duration: "耗时",
    publicationResult: "发布结果",
    failureSummary: "失败摘要",
    published: "已发布",
    retained: "保留上一版本",
    notAttempted: "未尝试",
    logs: "日志与诊断",
    cacheTTL: "评分缓存期限（分钟）",
    listenPort: "管理端口",
    listenAddress: "监听地址",
    restartRequired: "监听配置将在 NodeHarbor 重启后生效。",
  },
  en: {
    eyebrow: "Local proxy subscription curator",
    title: "NodeHarbor console",
    subtitle: "Confirm that the system is ready to curate and publish proxy subscriptions safely.",
    health: "System health",
    healthHint: "Live checks for each critical part of the local system",
    backend: "Backend",
    database: "Database",
    publication: "Published Subscription",
    healthy: "Healthy",
    unhealthy: "Unavailable",
    loading: "Checking",
    subscription: "Subscription URL",
    subscriptionHint: "This address always serves a valid configuration, even with no Proxy Nodes.",
    localSubscription: "Local subscription URL",
    currentSubscription: "Current access URL",
    lanSubscriptions: "LAN subscription URLs",
    noLanSubscriptions: "No LAN address is currently available for this listener.",
    listener: "Listener diagnostics",
    listenerHint: "Management routes accept loopback requests only; the Published Subscription follows the configured listener address.",
    copy: "Copy URL",
    copied: "Copied",
    language: "Language",
    switchLanguage: "简体中文",
    loadError: "System health could not be loaded. Confirm that the backend is running.",
    evaluation: "Evaluation Run",
    startEvaluation: "Start evaluation",
    ignoreCache: "Ignore score cache for this run",
    scoreCache: "Reused score cache within 24 hours",
    scoreProvider: "Requested from scoring provider",
    running: "Running",
    completed: "Completed",
    failed: "Failed",
    paused: "Paused",
    idle: "Not run yet",
    summary: (passed: number, total: number) => `${passed} / ${total} Proxy Nodes passed`,
    provider: "Scoring Provider",
    threshold: "Pass threshold",
    iplarkThreshold: "IPLark pass threshold",
    ipcheckThreshold: "IPCheck.ing pass threshold",
    ipcheck: "IPCheck.ing",
    iplark: "IPLark",
    providerReady: "Available",
    providerUnavailable: "Unavailable",
    providerDisabled: "Disabled",
    saveSettings: "Save scoring settings",
    interval: "Automatic interval (minutes)",
    retention: "History retention (days)",
    availabilityAttempts: "Probe attempts",
    availabilityRequired: "Required successes",
    availabilityTimeout: "Attempt timeout (seconds)",
    availabilityMaxLatency: "Maximum median latency (ms)",
    availabilityURLs: "Probe URLs (comma-separated)",
    evaluationWorkers: "Concurrent Proxy Nodes",
    scoringJitter: "Scoring jitter limit (ms)",
    saveAvailabilitySettings: "Save Availability Check settings",
    probeSuccess: (successful: number, attempts: number) => `${successful} / ${attempts} probes succeeded`,
    history: "History",
    historyHint: "Retained Evaluation Runs and diagnostics",
    exportConfig: "Export configuration JSON",
    trigger: "Trigger",
    phase: "Phase",
    duration: "Duration",
    publicationResult: "Publication",
    failureSummary: "Failure summary",
    published: "published",
    retained: "previous snapshot retained",
    notAttempted: "not attempted",
    logs: "Logs and diagnostics",
    cacheTTL: "Score cache TTL (minutes)",
    listenPort: "Management port",
    listenAddress: "Listen address",
    restartRequired: "Listener changes take effect after restarting NodeHarbor.",
  },
} as const;

const initialHealth: Health = {
  status: "loading",
  backend: { status: "loading" },
  database: { status: "loading" },
  publishedSubscription: { status: "loading" },
};

export default function App() {
  const [locale, setLocale] = useState<Locale>(browserLocale());
  const [health, setHealth] = useState<Health>(initialHealth);
  const [listener, setListener] = useState<ListenerSettings | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [copied, setCopied] = useState(false);
  const text = messages[locale];

  useEffect(() => {
    let active = true;
    Promise.all([
      fetch("/api/settings").then((response) => requireOK(response).json()),
      fetch("/api/health").then((response) => requireOK(response).json()),
    ])
      .then(([settings, currentHealth]) => {
        if (!active) return;
        setLocale(settings.language === "en" || settings.language === "zh-CN" ? settings.language : browserLocale());
        setListener(settings);
        setHealth(currentHealth);
      })
      .catch(() => {
        if (!active) return;
        setLoadFailed(true);
        setHealth({
          status: "unhealthy",
          backend: { status: "unhealthy" },
          database: { status: "unhealthy" },
          publishedSubscription: { status: "unhealthy" },
        });
      });
    return () => {
      active = false;
    };
  }, []);

  async function switchLocale() {
    const nextLocale: Locale = locale === "zh-CN" ? "en" : "zh-CN";
    setLocale(nextLocale);
    document.documentElement.lang = nextLocale;
    try {
      await fetch("/api/settings", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ language: nextLocale }),
      }).then(requireOK);
    } catch {
      setLoadFailed(true);
    }
  }

  async function copySubscriptionURL() {
    await navigator.clipboard.writeText(listener?.subscriptionURL || subscriptionURL());
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1600);
  }

  return (
    <main className="shell">
      <header className="topbar">
        <a className="brand" href="/" aria-label="NodeHarbor">
          <span className="brandMark" aria-hidden="true">N</span>
          <span>NodeHarbor</span>
        </a>
        <div className="languageControl">
          <span>{text.language}</span>
          <button type="button" onClick={switchLocale}>{text.switchLanguage}</button>
        </div>
      </header>

      <section className="hero">
        <p className="eyebrow">{text.eyebrow}</p>
        <h1>{text.title}</h1>
        <p className="subtitle">{text.subtitle}</p>
      </section>

      {loadFailed && <p className="notice" role="alert">{text.loadError}</p>}

      <section className="panel" aria-labelledby="health-heading">
        <div className="panelHeading">
          <div>
            <h2 id="health-heading">{text.health}</h2>
            <p>{text.healthHint}</p>
          </div>
          <StatusPill state={health.status} label={text[health.status]} />
        </div>
        <div className="healthGrid">
          <HealthCard label={text.backend} state={health.backend.status} statusLabel={text[health.backend.status]} />
          <HealthCard label={text.database} state={health.database.status} statusLabel={text[health.database.status]} />
          <HealthCard label={text.publication} state={health.publishedSubscription.status} statusLabel={text[health.publishedSubscription.status]} />
        </div>
      </section>

      <UpstreamSubscriptions locale={locale} />

      <EvaluationRun locale={locale} text={text} />

      <EvaluationHistory text={text} />

      <section className="subscriptionCard" aria-labelledby="subscription-heading">
        <div>
          <h2 id="subscription-heading">{text.subscription}</h2>
          <p>{text.subscriptionHint}</p>
          <div className="listenerDiagnostics">
            <span>{text.localSubscription}</span>
            <code>{listener?.localSubscriptionURL || subscriptionURL()}</code>
            <span>{text.currentSubscription}</span>
            <code>{listener?.subscriptionURL || subscriptionURL()}</code>
            <span>{text.lanSubscriptions}</span>
            {listener?.lanSubscriptionURLs?.length ? listener.lanSubscriptionURLs.map((url) => <code key={url}>{url}</code>) : <small>{text.noLanSubscriptions}</small>}
            <small>{text.listener}: {listener?.listenAddress && listener.listenPort ? `${listener.listenAddress}:${listener.listenPort}` : "—"}</small>
            {listener?.listenerError && <small role="alert">{listener.listenerError}</small>}
            <small>{text.listenerHint}</small>
            <small>{text.restartRequired}</small>
          </div>
        </div>
        <button className="primaryButton" type="button" onClick={copySubscriptionURL}>
          {copied ? text.copied : text.copy}
        </button>
      </section>
    </main>
  );
}

type EvaluationText = typeof messages[Locale];
type EvaluationState = { status: "idle" | "running" | "completed" | "failed" | "paused"; total: number; passed: number; failed: number; reason?: string; results: Array<{ name: string; state: string; attempts: number; successful: number; medianLatencyMs: number; exitIdentity?: string; addressFamily?: string; ipScore?: number; scoreSource?: string; reason?: string }> };
type ProviderName = "iplark" | "ipcheck";
type ProviderStatus = { name: ProviderName; enabled: boolean; failureStatus?: string };

function EvaluationRun({ locale, text }: { locale: Locale; text: EvaluationText }) {
  const [run, setRun] = useState<EvaluationState>({ status: "idle", total: 0, passed: 0, failed: 0, results: [] });
  const [busy, setBusy] = useState(false);
  const [ignoreCache, setIgnoreCache] = useState(false);
  const [provider, setProvider] = useState<ProviderName>("iplark");
  const [thresholds, setThresholds] = useState<Record<ProviderName, number>>({ iplark: 70, ipcheck: 70 });
  const [providerStatuses, setProviderStatuses] = useState<ProviderStatus[]>([]);
  const [interval, setInterval] = useState(360);
  const [retention, setRetention] = useState(7);
  const [attempts, setAttempts] = useState(3);
  const [requiredSuccesses, setRequiredSuccesses] = useState(2);
  const [timeoutSeconds, setTimeoutSeconds] = useState(5);
  const [maxLatency, setMaxLatency] = useState(1500);
  const [availabilityURLs, setAvailabilityURLs] = useState("");
  const [workers, setWorkers] = useState(3);
  const [scoringJitter, setScoringJitter] = useState(100);
  const [cacheTTL, setCacheTTL] = useState(1440);
  const [listenAddress, setListenAddress] = useState("127.0.0.1");
  const [listenPort, setListenPort] = useState(9876);
  useEffect(() => {
    let active = true;
    const load = () => fetch("/api/evaluation-runs/current").then(requireOK).then((response) => response.json()).then((value) => { if (active && Array.isArray(value.results) && typeof value.status === "string") setRun({ ...value, results: value.results }); }).catch(() => undefined);
    load();
    const timer = window.setInterval(load, 1000);
    return () => { active = false; window.clearInterval(timer); };
  }, []);
  useEffect(() => { fetch("/api/settings").then(requireOK).then((response) => response.json()).then((settings) => { const selectedProvider: ProviderName = settings.scoringProvider === "ipcheck" ? "ipcheck" : "iplark"; setProvider(selectedProvider); setThresholds({ iplark: typeof settings.iplarkThreshold === "number" ? settings.iplarkThreshold : 70, ipcheck: typeof settings.ipcheckThreshold === "number" ? settings.ipcheckThreshold : 70 }); setProviderStatuses(Array.isArray(settings.scoringProviders) ? settings.scoringProviders : []); setInterval(typeof settings.evaluationIntervalMinutes === "number" ? settings.evaluationIntervalMinutes : 360); setRetention(settings.historyRetentionDays || 7); setAttempts(settings.availabilityAttempts || 3); setRequiredSuccesses(settings.availabilityRequiredSuccesses || 2); setTimeoutSeconds(settings.availabilityTimeoutSeconds || 5); setMaxLatency(settings.availabilityMaxLatencyMs || 1500); setAvailabilityURLs(Array.isArray(settings.availabilityURLs) ? settings.availabilityURLs.join(", ") : ""); setWorkers(settings.evaluationWorkerCount || 3); setScoringJitter(typeof settings.scoringJitterMs === "number" ? settings.scoringJitterMs : 100); setCacheTTL(settings.scoreCacheTTLMinutes || 1440); setListenAddress(typeof settings.listenAddress === "string" ? settings.listenAddress : "127.0.0.1"); setListenPort(settings.listenPort || 9876); }).catch(() => undefined); }, []);
  async function start() {
    setBusy(true);
    try { const response = await fetch("/api/evaluation-runs", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ ignoreCache }) }); if (!response.ok) throw new Error(`HTTP ${response.status}`); setRun(await response.json()); } finally { setBusy(false); }
  }
  async function saveScoringSettings() {
    await fetch("/api/settings", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ scoringProvider: provider, iplarkThreshold: thresholds.iplark, ipcheckThreshold: thresholds.ipcheck, evaluationIntervalMinutes: interval, historyRetentionDays: retention, scoreCacheTTLMinutes: cacheTTL, listenAddress, listenPort }) }).then(requireOK);
  }
  async function saveAvailabilitySettings() {
    await fetch("/api/settings", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ availabilityAttempts: attempts, availabilityRequiredSuccesses: requiredSuccesses, availabilityTimeoutSeconds: timeoutSeconds, availabilityMaxLatencyMs: maxLatency, availabilityURLs: availabilityURLs.split(",").map((value) => value.trim()).filter(Boolean), evaluationWorkerCount: workers, scoringJitterMs: scoringJitter }) }).then(requireOK);
  }
  const statusLabel = text[run.status];
  return <section className="panel evaluationPanel" aria-labelledby="evaluation-heading">
    <div className="panelHeading"><div><h2 id="evaluation-heading">{text.evaluation}</h2><p>{text.summary(run.passed, run.total)}</p></div><div><span className={`statusPill statusPill--${run.status}`}>{statusLabel}</span><button className="primaryButton" type="button" onClick={start} disabled={busy || run.status === "running"}>{text.startEvaluation}</button></div></div>
    <div className="evaluationSettings"><label><span>{text.provider}</span><select value={provider} onChange={(event) => setProvider(event.target.value as ProviderName)}><option value="iplark">{text.iplark}</option><option value="ipcheck">{text.ipcheck}</option></select></label><label><span>{text.iplarkThreshold}</span><input type="number" min="0" max="100" value={thresholds.iplark} onChange={(event) => setThresholds((current) => ({ ...current, iplark: Number(event.target.value) }))} /></label><label><span>{text.ipcheckThreshold}</span><input type="number" min="0" max="100" value={thresholds.ipcheck} onChange={(event) => setThresholds((current) => ({ ...current, ipcheck: Number(event.target.value) }))} /></label><label><input type="checkbox" checked={ignoreCache} onChange={(event) => setIgnoreCache(event.target.checked)} />{text.ignoreCache}</label><button type="button" onClick={saveScoringSettings}>{text.saveSettings}</button></div>
    {providerStatuses.length > 0 && <div className="providerStatuses" aria-label={text.provider}><strong>{text.provider}</strong>{providerStatuses.map((status) => <small key={status.name}>{status.name === "ipcheck" ? text.ipcheck : text.iplark}: {status.enabled ? (status.failureStatus ? text.providerUnavailable : text.providerReady) : text.providerDisabled}{status.failureStatus ? ` · ${status.failureStatus}` : ""}</small>)}</div>}
     <div className="evaluationSettings"><label><span>{text.interval}</span><input type="number" min="0" value={interval} onChange={(event) => setInterval(Number(event.target.value))} /></label><label><span>{text.retention}</span><input type="number" min="3" max="7" value={retention} onChange={(event) => setRetention(Number(event.target.value))} /></label><label><span>{text.cacheTTL}</span><input type="number" min="1" value={cacheTTL} onChange={(event) => setCacheTTL(Number(event.target.value))} /></label><label><span>{text.listenAddress}</span><input type="text" value={listenAddress} onChange={(event) => setListenAddress(event.target.value)} /></label><label><span>{text.listenPort}</span><input type="number" min="1024" max="65535" value={listenPort} onChange={(event) => setListenPort(Number(event.target.value))} /></label></div>
     <div className="evaluationSettings"><label><span>{text.availabilityAttempts}</span><input type="number" min="1" max="10" value={attempts} onChange={(event) => setAttempts(Number(event.target.value))} /></label><label><span>{text.availabilityRequired}</span><input type="number" min="1" max="10" value={requiredSuccesses} onChange={(event) => setRequiredSuccesses(Number(event.target.value))} /></label><label><span>{text.availabilityTimeout}</span><input type="number" min="1" max="300" value={timeoutSeconds} onChange={(event) => setTimeoutSeconds(Number(event.target.value))} /></label></div>
     <div className="evaluationSettings"><label><span>{text.availabilityMaxLatency}</span><input type="number" min="1" max="60000" value={maxLatency} onChange={(event) => setMaxLatency(Number(event.target.value))} /></label><label><span>{text.availabilityURLs}</span><input type="text" value={availabilityURLs} onChange={(event) => setAvailabilityURLs(event.target.value)} /></label><label><span>{text.evaluationWorkers}</span><input type="number" min="1" max="3" value={workers} onChange={(event) => setWorkers(Number(event.target.value))} /></label><label><span>{text.scoringJitter}</span><input type="number" min="0" max="1000" value={scoringJitter} onChange={(event) => setScoringJitter(Number(event.target.value))} /></label><button type="button" onClick={saveAvailabilitySettings}>{text.saveAvailabilitySettings}</button></div>
     {run.reason && <p className="sourceError" role="alert">{run.reason}</p>}
    {run.results.length > 0 && <div className="nodeResults">{run.results.map((result) => <div className={`nodeResult nodeResult--${result.state === "passed" ? "accepted" : "rejected"}`} key={result.name}><span>{result.name}</span><strong>{result.state === "passed" ? `${result.ipScore?.toFixed(0) ?? "?"} · ${result.addressFamily ?? "?"}` : text.failed}</strong><small>{text.probeSuccess(result.successful, result.attempts)}</small>{result.scoreSource && <small>{result.scoreSource === "cache" ? text.scoreCache : text.scoreProvider}</small>}{result.reason && <small>{result.reason}</small>}{result.exitIdentity && <small>{result.exitIdentity} · {result.medianLatencyMs.toFixed(0)} ms</small>}</div>)}</div>}
  </section>;
}

type HistoryRun = {
  id: string;
  trigger: string;
  status: string;
  durationMs: number;
  total: number;
  passed: number;
  failed: number;
  publicationResult?: string;
  failureSummary?: string;
  phases: Array<{ name: string; status: string; durationMs: number }>;
};

function EvaluationHistory({ text }: { text: EvaluationText }) {
  const [runs, setRuns] = useState<HistoryRun[]>([]);
  const [logs, setLogs] = useState<Array<{ timestamp: string; level: string; message: string }>>([]);
  useEffect(() => {
    let active = true;
    fetch("/api/evaluation-runs").then(requireOK).then((response) => response.json()).then((history) => {
      if (active && Array.isArray(history)) setRuns(history);
    }).catch(() => undefined);
    fetch("/api/logs").then(requireOK).then((response) => response.json()).then((diagnostics) => {
      if (active && Array.isArray(diagnostics)) setLogs(diagnostics.slice(0, 8));
    }).catch(() => undefined);
    return () => { active = false; };
  }, []);
  return <section className="panel" aria-labelledby="history-heading">
    <div className="panelHeading"><div><h2 id="history-heading">{text.history}</h2><p>{text.historyHint}</p></div><a className="secondaryButton" href="/api/settings/export" download="nodeharbor-settings.json">{text.exportConfig}</a></div>
    {runs.length === 0 ? <p className="emptyState">—</p> : <div className="historyList">{runs.map((run) => <article className="historyItem" key={run.id}>
      <div className="historySummary"><strong>{run.trigger}</strong><span>{run.status}</span><span>{formatDuration(run.durationMs)}</span><span>{run.passed} / {run.total}</span><span>{run.publicationResult ?? text.notAttempted}</span></div>
      <small>{text.phase}: {run.phases.map((phase) => `${phase.name} (${formatDuration(phase.durationMs)})`).join(" · ") || "—"}</small>
      {run.failureSummary && <small>{text.failureSummary}: {run.failureSummary}</small>}
    </article>)}</div>}
    <div className="historyLogs"><h3>{text.logs}</h3>{logs.length === 0 ? <p className="emptyState">—</p> : logs.map((log, index) => <small key={`${log.timestamp}-${index}`}>{log.level}: {log.message}</small>)}</div>
  </section>;
}

function formatDuration(durationMs: number): string {
  if (!Number.isFinite(durationMs) || durationMs < 0) return "—";
  if (durationMs < 1000) return `${Math.round(durationMs)}ms`;
  return `${Math.round(durationMs / 1000)}s`;
}

function HealthCard({ label, state, statusLabel }: { label: string; state: HealthState; statusLabel: string }) {
  return (
    <article className="healthCard">
      <span className={`statusDot statusDot--${state}`} aria-hidden="true" />
      <div>
        <h3>{label}</h3>
        <p>{statusLabel}</p>
      </div>
    </article>
  );
}

function StatusPill({ state, label }: { state: HealthState; label: string }) {
  return <span className={`statusPill statusPill--${state}`}>{label}</span>;
}

function browserLocale(): Locale {
  return navigator.language.toLowerCase().startsWith("zh") ? "zh-CN" : "en";
}

function subscriptionURL(): string {
  return `${window.location.origin}/sub/clash.yaml`;
}

function requireOK(response: Response): Response {
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  return response;
}
