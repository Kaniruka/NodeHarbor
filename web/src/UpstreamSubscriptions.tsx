import { FormEvent, useEffect, useState } from "react";

type Locale = "zh-CN" | "en";
type ImportKind = "url" | "upload" | "paste";

type UpstreamSubscription = {
  id: string;
  name: string;
  kind: ImportKind;
  url?: string;
  userAgent?: string;
  configuredDocument?: string;
  lastSuccessfulDocument?: string;
  proxyNodeCount: number;
  enabled: boolean;
  refreshStatus: "pending" | "success" | "stale";
  lastError?: string;
};

const copy = {
  "zh-CN": {
    heading: "上游订阅",
    hint: "导入、刷新并管理上游订阅",
    count: (value: number) => `${value} / 10 个上游订阅`,
    url: "订阅 URL",
    upload: "上传文件",
    paste: "粘贴 YAML",
    name: "名称",
    urlLabel: "完整 URL",
    userAgent: "User-Agent",
    file: "YAML 文件",
    yaml: "YAML 内容",
    add: "添加上游订阅",
    save: "保存更改",
    cancel: "取消编辑",
    empty: "还没有上游订阅。选择一种方式添加第一个上游订阅。",
    success: "刷新成功",
    stale: "已过期，使用上次成功内容",
    pending: "等待刷新",
    nodes: (value: number) => `${value} 个代理节点`,
    refresh: "刷新",
    edit: "编辑",
    disable: "禁用",
    enable: "启用",
    remove: "删除",
    limit: "最多只能添加 10 个上游订阅。请先删除一个上游订阅。",
    loadError: "无法读取上游订阅列表。",
    actionError: "操作失败，请重试。",
    defaultUserAgent: "留空时使用 Mihomo 兼容标识",
    requiredFile: "请选择 YAML 文件。",
  },
  en: {
    heading: "Upstream Subscriptions",
    hint: "Import, refresh, and manage Upstream Subscriptions",
    count: (value: number) => `${value} / 10 Upstream Subscriptions`,
    url: "Subscription URL",
    upload: "Upload file",
    paste: "Paste YAML",
    name: "Name",
    urlLabel: "Full URL",
    userAgent: "User-Agent",
    file: "YAML file",
    yaml: "YAML content",
    add: "Add Upstream Subscription",
    save: "Save changes",
    cancel: "Cancel edit",
    empty: "No Upstream Subscriptions yet. Choose an import method to add the first Upstream Subscription.",
    success: "Refresh successful",
    stale: "Stale — using last successful content",
    pending: "Waiting to refresh",
    nodes: (value: number) => `${value} Proxy Nodes`,
    refresh: "Refresh",
    edit: "Edit",
    disable: "Disable",
    enable: "Enable",
    remove: "Delete",
    limit: "At most 10 Upstream Subscriptions are allowed. Delete an Upstream Subscription first.",
    loadError: "Upstream Subscriptions could not be loaded.",
    actionError: "The operation failed. Try again.",
    defaultUserAgent: "Leave blank to use a Mihomo-compatible identifier",
    requiredFile: "Choose a YAML file.",
  },
} as const;

export default function UpstreamSubscriptions({ locale }: { locale: Locale }) {
  const text = copy[locale];
  const [subscriptions, setSubscriptions] = useState<UpstreamSubscription[]>([]);
  const [kind, setKind] = useState<ImportKind>("url");
  const [name, setName] = useState("");
  const [url, setURL] = useState("");
  const [userAgent, setUserAgent] = useState("");
  const [document, setDocument] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [editingID, setEditingID] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let active = true;
    fetch("/api/upstream-subscriptions")
      .then(requireOK)
      .then((response) => response.json())
      .then((items) => {
        if (active) setSubscriptions(items);
      })
      .catch(() => {
        if (active) setError(text.loadError);
      });
    return () => {
      active = false;
    };
  }, [text.loadError]);

  const atLimit = subscriptions.length >= 10 && editingID === null;

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (atLimit) return;
    setBusy(true);
    setError("");
    try {
      let response: Response;
      if (kind === "upload") {
        if (!file) {
          setError(text.requiredFile);
          return;
        }
        const form = new FormData();
        form.set("name", name);
        form.set("file", file);
        response = await fetch(editingID ? `/api/upstream-subscriptions/${editingID}` : "/api/upstream-subscriptions", {
          method: editingID ? "PUT" : "POST",
          body: form,
        });
      } else if (editingID) {
        response = await fetch(`/api/upstream-subscriptions/${editingID}`, {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ name, url, userAgent, document }),
        });
      } else {
        response = await fetch("/api/upstream-subscriptions", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(kind === "url" ? { name, kind, url, userAgent } : { name, kind, document }),
        });
      }
      const result = await readResult(response);
      if (!response.ok) {
        if (editingID) await reload();
        throw new Error(result.error || text.actionError);
      }
      setSubscriptions((current) => editingID ? current.map((item) => item.id === result.id ? result : item) : [...current, result]);
      resetForm();
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : text.actionError);
    } finally {
      setBusy(false);
    }
  }

  function beginEdit(subscription: UpstreamSubscription) {
    setEditingID(subscription.id);
    setKind(subscription.kind);
    setName(subscription.name);
    setURL(subscription.url || "");
    setUserAgent(subscription.userAgent || "");
    setDocument(subscription.configuredDocument || "");
    setFile(null);
    setError("");
  }

  async function refresh(subscription: UpstreamSubscription) {
    await mutate(subscription.id, `/api/upstream-subscriptions/${subscription.id}/refresh`, { method: "POST" });
  }

  async function toggle(subscription: UpstreamSubscription) {
    await mutate(subscription.id, `/api/upstream-subscriptions/${subscription.id}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ enabled: !subscription.enabled }),
    });
  }

  async function remove(subscription: UpstreamSubscription) {
    setError("");
    try {
      const response = await fetch(`/api/upstream-subscriptions/${subscription.id}`, { method: "DELETE" });
      if (!response.ok) throw new Error(await errorMessage(response, text.actionError));
      setSubscriptions((current) => current.filter((item) => item.id !== subscription.id));
      if (editingID === subscription.id) resetForm();
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : text.actionError);
    }
  }

  async function mutate(id: string, endpoint: string, init: RequestInit) {
    setError("");
    try {
      const response = await fetch(endpoint, init);
      if (!response.ok) {
        const message = await errorMessage(response, text.actionError);
        await reload();
        throw new Error(message);
      }
      const updated = await response.json();
      setSubscriptions((current) => current.map((item) => item.id === id ? updated : item));
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : text.actionError);
    }
  }

  async function reload() {
    const response = await fetch("/api/upstream-subscriptions").then(requireOK);
    setSubscriptions(await response.json());
  }

  function resetForm() {
    setEditingID(null);
    setName("");
    setURL("");
    setUserAgent("");
    setDocument("");
    setFile(null);
  }

  return (
    <section className="upstreamPanel" aria-labelledby="upstream-heading">
      <div className="panelHeading">
        <div>
          <h2 id="upstream-heading">{text.heading}</h2>
          <p>{text.hint}</p>
        </div>
        <span className="sourceCount">{text.count(subscriptions.length)}</span>
      </div>

      <form className="sourceForm" onSubmit={submit}>
        <div className="importTabs" role="group" aria-label={text.heading}>
          {(["url", "upload", "paste"] as ImportKind[]).map((value) => (
            <button key={value} type="button" className={kind === value ? "active" : ""} disabled={editingID !== null} onClick={() => setKind(value)}>
              {value === "url" ? text.url : value === "upload" ? text.upload : text.paste}
            </button>
          ))}
        </div>
        <div className="formGrid">
          <label>
            <span>{text.name}</span>
            <input value={name} onChange={(event) => setName(event.target.value)} required disabled={atLimit} />
          </label>
          {kind === "url" && (
            <>
              <label className="wideField">
                <span>{text.urlLabel}</span>
                <input type="url" value={url} onChange={(event) => setURL(event.target.value)} required disabled={atLimit} />
              </label>
              <label className="wideField">
                <span>{text.userAgent}</span>
                <input value={userAgent} onChange={(event) => setUserAgent(event.target.value)} placeholder={text.defaultUserAgent} disabled={atLimit} />
              </label>
            </>
          )}
          {kind === "upload" && (
            <label className="wideField">
              <span>{text.file}</span>
              <input type="file" accept=".yaml,.yml,application/yaml,text/yaml,text/plain" onChange={(event) => setFile(event.target.files?.[0] || null)} disabled={atLimit} />
            </label>
          )}
          {kind === "paste" && (
            <label className="wideField">
              <span>{text.yaml}</span>
              <textarea value={document} onChange={(event) => setDocument(event.target.value)} rows={7} required disabled={atLimit} />
            </label>
          )}
        </div>
        {atLimit && <p className="formError" role="alert">{text.limit}</p>}
        {error && <p className="formError" role="alert">{error}</p>}
        <div className="formActions">
          <button className="primaryButton" type="submit" disabled={busy || atLimit}>{editingID ? text.save : text.add}</button>
          {editingID && <button className="secondaryButton" type="button" onClick={resetForm}>{text.cancel}</button>}
        </div>
      </form>

      <div className="sourceList">
        {subscriptions.length === 0 && <p className="emptyState">{text.empty}</p>}
        {subscriptions.map((subscription) => (
          <article className={`sourceCard ${subscription.enabled ? "" : "sourceCard--disabled"}`} key={subscription.id}>
            <div className="sourceCardMain">
              <div>
                <h3>{subscription.name}</h3>
                <p className="sourceMeta">
                  <span>{text.nodes(subscription.proxyNodeCount)}</span>
                  <span>{subscription.kind === "url" ? text.url : subscription.kind === "upload" ? text.upload : text.paste}</span>
                </p>
              </div>
              <span className={`refreshState refreshState--${subscription.refreshStatus}`}>
                {text[subscription.refreshStatus]}
              </span>
            </div>
            {subscription.url && <code className="sourceURL">{subscription.url}</code>}
            {subscription.lastError && <p className="sourceError" role="alert">{subscription.lastError}</p>}
            <div className="sourceActions">
              <button type="button" onClick={() => refresh(subscription)}>{text.refresh}</button>
              <button type="button" onClick={() => beginEdit(subscription)}>{text.edit}</button>
              <button type="button" onClick={() => toggle(subscription)}>{subscription.enabled ? text.disable : text.enable}</button>
              <button type="button" className="dangerButton" onClick={() => remove(subscription)}>{text.remove}</button>
            </div>
          </article>
        ))}
      </div>
    </section>
  );
}

async function readResult(response: Response) {
  return response.json().catch(() => ({}));
}

async function errorMessage(response: Response, fallback: string) {
  const result = await readResult(response);
  return result.error || fallback;
}

function requireOK(response: Response): Response {
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  return response;
}
