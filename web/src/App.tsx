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
    copy: "复制地址",
    copied: "已复制",
    language: "语言",
    switchLanguage: "English",
    loadError: "无法读取系统健康状态，请确认后端正在运行。",
    evaluation: "评估运行",
    startEvaluation: "开始评估",
    running: "运行中",
    completed: "已完成",
    failed: "失败",
    paused: "已暂停",
    idle: "尚未运行",
    summary: (passed: number, total: number) => `${passed} / ${total} 个节点通过`,
    provider: "评分源",
    threshold: "合格阈值",
    ipcheck: "IPCheck.ing",
    iplark: "IPLark",
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
    copy: "Copy URL",
    copied: "Copied",
    language: "Language",
    switchLanguage: "简体中文",
    loadError: "System health could not be loaded. Confirm that the backend is running.",
    evaluation: "Evaluation Run",
    startEvaluation: "Start evaluation",
    running: "Running",
    completed: "Completed",
    failed: "Failed",
    paused: "Paused",
    idle: "Not run yet",
    summary: (passed: number, total: number) => `${passed} / ${total} Proxy Nodes passed`,
    provider: "Scoring Provider",
    threshold: "Pass threshold",
    ipcheck: "IPCheck.ing",
    iplark: "IPLark",
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
    await navigator.clipboard.writeText(subscriptionURL());
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

      <section className="subscriptionCard" aria-labelledby="subscription-heading">
        <div>
          <h2 id="subscription-heading">{text.subscription}</h2>
          <p>{text.subscriptionHint}</p>
          <code>{subscriptionURL()}</code>
        </div>
        <button className="primaryButton" type="button" onClick={copySubscriptionURL}>
          {copied ? text.copied : text.copy}
        </button>
      </section>
    </main>
  );
}

type EvaluationText = typeof messages[Locale];
type EvaluationState = { status: "idle" | "running" | "completed" | "failed" | "paused"; total: number; passed: number; failed: number; reason?: string; results: Array<{ name: string; state: string; attempts: number; successful: number; medianLatencyMs: number; exitIdentity?: string; addressFamily?: string; ipScore?: number; reason?: string }> };

function EvaluationRun({ locale, text }: { locale: Locale; text: EvaluationText }) {
  const [run, setRun] = useState<EvaluationState>({ status: "idle", total: 0, passed: 0, failed: 0, results: [] });
  const [busy, setBusy] = useState(false);
  const [provider, setProvider] = useState<"iplark" | "ipcheck">("iplark");
  const [threshold, setThreshold] = useState(70);
  const [interval, setInterval] = useState(360);
  const [retention, setRetention] = useState(7);
  const [attempts, setAttempts] = useState(3);
  const [requiredSuccesses, setRequiredSuccesses] = useState(2);
  const [timeoutSeconds, setTimeoutSeconds] = useState(5);
  const [maxLatency, setMaxLatency] = useState(1500);
  const [availabilityURLs, setAvailabilityURLs] = useState("");
  const [workers, setWorkers] = useState(3);
  const [scoringJitter, setScoringJitter] = useState(100);
  useEffect(() => {
    let active = true;
    const load = () => fetch("/api/evaluation-runs/current").then(requireOK).then((response) => response.json()).then((value) => { if (active && Array.isArray(value.results) && typeof value.status === "string") setRun({ ...value, results: value.results }); }).catch(() => undefined);
    load();
    const timer = window.setInterval(load, 1000);
    return () => { active = false; window.clearInterval(timer); };
  }, []);
  useEffect(() => { fetch("/api/settings").then(requireOK).then((response) => response.json()).then((settings) => { setProvider(settings.scoringProvider === "ipcheck" ? "ipcheck" : "iplark"); setThreshold(settings[settings.scoringProvider === "ipcheck" ? "ipcheckThreshold" : "iplarkThreshold"] || 70); setInterval(settings.evaluationIntervalMinutes || 360); setRetention(settings.historyRetentionDays || 7); setAttempts(settings.availabilityAttempts || 3); setRequiredSuccesses(settings.availabilityRequiredSuccesses || 2); setTimeoutSeconds(settings.availabilityTimeoutSeconds || 5); setMaxLatency(settings.availabilityMaxLatencyMs || 1500); setAvailabilityURLs(Array.isArray(settings.availabilityURLs) ? settings.availabilityURLs.join(", ") : ""); setWorkers(settings.evaluationWorkerCount || 3); setScoringJitter(typeof settings.scoringJitterMs === "number" ? settings.scoringJitterMs : 100); }).catch(() => undefined); }, []);
  async function start() {
    setBusy(true);
    try { const response = await fetch("/api/evaluation-runs", { method: "POST" }); if (!response.ok) throw new Error(`HTTP ${response.status}`); setRun(await response.json()); } finally { setBusy(false); }
  }
  async function saveScoringSettings() {
    await fetch("/api/settings", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ scoringProvider: provider, [provider === "ipcheck" ? "ipcheckThreshold" : "iplarkThreshold"]: threshold, evaluationIntervalMinutes: interval, historyRetentionDays: retention }) });
  }
  async function saveAvailabilitySettings() {
    await fetch("/api/settings", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ availabilityAttempts: attempts, availabilityRequiredSuccesses: requiredSuccesses, availabilityTimeoutSeconds: timeoutSeconds, availabilityMaxLatencyMs: maxLatency, availabilityURLs: availabilityURLs.split(",").map((value) => value.trim()).filter(Boolean), evaluationWorkerCount: workers, scoringJitterMs: scoringJitter }) }).then(requireOK);
  }
  const statusLabel = text[run.status];
  return <section className="panel evaluationPanel" aria-labelledby="evaluation-heading">
    <div className="panelHeading"><div><h2 id="evaluation-heading">{text.evaluation}</h2><p>{text.summary(run.passed, run.total)}</p></div><div><span className={`statusPill statusPill--${run.status}`}>{statusLabel}</span><button className="primaryButton" type="button" onClick={start} disabled={busy || run.status === "running"}>{text.startEvaluation}</button></div></div>
    <div className="evaluationSettings"><label><span>{text.provider}</span><select value={provider} onChange={(event) => setProvider(event.target.value as "iplark" | "ipcheck")}><option value="iplark">{text.iplark}</option><option value="ipcheck">{text.ipcheck}</option></select></label><label><span>{text.threshold}</span><input type="number" min="0" max="100" value={threshold} onChange={(event) => setThreshold(Number(event.target.value))} /></label><button type="button" onClick={saveScoringSettings}>{text.saveSettings}</button></div>
     <div className="evaluationSettings"><label><span>{text.interval}</span><input type="number" min="0" value={interval} onChange={(event) => setInterval(Number(event.target.value))} /></label><label><span>{text.retention}</span><input type="number" min="3" max="7" value={retention} onChange={(event) => setRetention(Number(event.target.value))} /></label></div>
     <div className="evaluationSettings"><label><span>{text.availabilityAttempts}</span><input type="number" min="1" max="10" value={attempts} onChange={(event) => setAttempts(Number(event.target.value))} /></label><label><span>{text.availabilityRequired}</span><input type="number" min="1" max="10" value={requiredSuccesses} onChange={(event) => setRequiredSuccesses(Number(event.target.value))} /></label><label><span>{text.availabilityTimeout}</span><input type="number" min="1" max="300" value={timeoutSeconds} onChange={(event) => setTimeoutSeconds(Number(event.target.value))} /></label></div>
     <div className="evaluationSettings"><label><span>{text.availabilityMaxLatency}</span><input type="number" min="1" max="60000" value={maxLatency} onChange={(event) => setMaxLatency(Number(event.target.value))} /></label><label><span>{text.availabilityURLs}</span><input type="text" value={availabilityURLs} onChange={(event) => setAvailabilityURLs(event.target.value)} /></label><label><span>{text.evaluationWorkers}</span><input type="number" min="1" max="3" value={workers} onChange={(event) => setWorkers(Number(event.target.value))} /></label><label><span>{text.scoringJitter}</span><input type="number" min="0" max="1000" value={scoringJitter} onChange={(event) => setScoringJitter(Number(event.target.value))} /></label><button type="button" onClick={saveAvailabilitySettings}>{text.saveAvailabilitySettings}</button></div>
     {run.reason && <p className="sourceError" role="alert">{run.reason}</p>}
     {run.results.length > 0 && <div className="nodeResults">{run.results.map((result) => <div className={`nodeResult nodeResult--${result.state === "passed" ? "accepted" : "rejected"}`} key={result.name}><span>{result.name}</span><strong>{result.state === "passed" ? `${result.ipScore?.toFixed(0) ?? "?"} · ${result.addressFamily ?? "?"}` : text.failed}</strong><small>{text.probeSuccess(result.successful, result.attempts)}</small>{result.reason && <small>{result.reason}</small>}{result.exitIdentity && <small>{result.exitIdentity} · {result.medianLatencyMs.toFixed(0)} ms</small>}</div>)}</div>}
  </section>;
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
