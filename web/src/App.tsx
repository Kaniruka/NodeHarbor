import { useEffect, useState } from "react";
import "./styles.css";

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
