import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import App from "./App";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

test("shows health in Simplified Chinese and switches every visible label to English", async () => {
  const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    if (url.endsWith("/api/settings") && init?.method === "PUT") {
      return new Response(null, { status: 204 });
    }
    if (url.endsWith("/api/settings")) {
      return Response.json({ language: "zh-CN", installationId: "installation-1" });
    }
    if (url.endsWith("/api/upstream-subscriptions")) {
      return Response.json([]);
    }
    return Response.json({
      status: "healthy",
      backend: { status: "healthy" },
      database: { status: "healthy" },
      publishedSubscription: { status: "healthy" },
    });
  });

  render(<App />);
  expect(await screen.findByRole("heading", { name: "系统健康" })).toBeInTheDocument();
  expect(screen.getByText("后端")).toBeInTheDocument();
  expect(screen.getAllByText("健康")).toHaveLength(4);

  await userEvent.click(screen.getByRole("button", { name: "English" }));
  expect(await screen.findByRole("heading", { name: "System health" })).toBeInTheDocument();
  expect(screen.getByText("Backend")).toBeInTheDocument();
  expect(screen.queryByText("后端")).not.toBeInTheDocument();
  await waitFor(() => expect(fetchMock).toHaveBeenCalledWith("/api/settings", expect.objectContaining({ method: "PUT" })));
});

test("user imports a pasted Upstream Subscription and sees it in the list", async () => {
  const document = "proxies:\n  - name: pasted-node\n    type: ss\n    server: example.test\n    port: 443\n";
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    if (url.endsWith("/api/settings")) {
      return Response.json({ language: "zh-CN", installationId: "installation-1" });
    }
    if (url.endsWith("/api/health")) {
      return Response.json({
        status: "healthy",
        backend: { status: "healthy" },
        database: { status: "healthy" },
        publishedSubscription: { status: "healthy" },
      });
    }
    if (url.endsWith("/api/upstream-subscriptions") && init?.method === "POST") {
      expect(JSON.parse(String(init.body))).toEqual({ name: "测试来源", kind: "paste", document });
      return Response.json({
        id: "source-1",
        name: "测试来源",
        kind: "paste",
        configuredDocument: document,
        lastSuccessfulDocument: document,
        proxyNodeCount: 1,
        enabled: true,
        refreshStatus: "success",
      }, { status: 201 });
    }
    if (url.endsWith("/api/upstream-subscriptions")) {
      return Response.json([]);
    }
    throw new Error(`unexpected request: ${url}`);
  });

  render(<App />);
  expect(await screen.findByRole("heading", { name: "上游订阅" })).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "粘贴 YAML" }));
  await userEvent.type(screen.getByLabelText("名称"), "测试来源");
  await userEvent.type(screen.getByLabelText("YAML 内容"), document);
  await userEvent.click(screen.getByRole("button", { name: "添加上游订阅" }));

  expect(await screen.findByRole("heading", { name: "测试来源" })).toBeInTheDocument();
  expect(screen.getByText("1 个代理节点")).toBeInTheDocument();
  expect(screen.getByText("刷新成功")).toBeInTheDocument();
});

test("user uploads a YAML file as an Upstream Subscription", async () => {
  const document = "proxies:\n  - name: uploaded\n    type: ss\n    server: upload.example\n    port: 443\n";
  const replacement = "proxies:\n  - name: replacement\n    type: vless\n    server: replacement.example\n    port: 8443\n";
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    if (url.endsWith("/api/settings")) return Response.json({ language: "zh-CN", installationId: "installation-1" });
    if (url.endsWith("/api/health")) return Response.json({
      status: "healthy", backend: { status: "healthy" }, database: { status: "healthy" }, publishedSubscription: { status: "healthy" },
    });
    if (url.endsWith("/api/upstream-subscriptions") && init?.method === "POST") {
      expect(init.body).toBeInstanceOf(FormData);
      expect((init.body as FormData).get("name")).toBe("上传来源");
      return Response.json({ id: "upload-1", name: "上传来源", kind: "upload", configuredDocument: document, lastSuccessfulDocument: document, proxyNodeCount: 1, enabled: true, refreshStatus: "success" }, { status: 201 });
    }
    if (url.endsWith("/upload-1") && init?.method === "PUT") {
      expect(init.body).toBeInstanceOf(FormData);
      expect((init.body as FormData).get("name")).toBe("更新上传");
      expect((init.body as FormData).get("file")).toBeInstanceOf(File);
      return Response.json({ id: "upload-1", name: "更新上传", kind: "upload", configuredDocument: replacement, lastSuccessfulDocument: replacement, proxyNodeCount: 1, enabled: true, refreshStatus: "success" });
    }
    if (url.endsWith("/api/upstream-subscriptions")) return Response.json([]);
    throw new Error(`unexpected request: ${url}`);
  });
  render(<App />);
  await screen.findByRole("heading", { name: "上游订阅" });
  await userEvent.click(screen.getByRole("button", { name: "上传文件" }));
  await userEvent.type(screen.getByLabelText("名称"), "上传来源");
  await userEvent.upload(screen.getByLabelText("YAML 文件"), new File([document], "subscription.yaml", { type: "application/yaml" }));
  await userEvent.click(screen.getByRole("button", { name: "添加上游订阅" }));
  expect(await screen.findByRole("heading", { name: "上传来源" })).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "编辑" }));
  await userEvent.clear(screen.getByLabelText("名称"));
  await userEvent.type(screen.getByLabelText("名称"), "更新上传");
  await userEvent.upload(screen.getByLabelText("YAML 文件"), new File([replacement], "replacement.yaml", { type: "application/yaml" }));
  await userEvent.click(screen.getByRole("button", { name: "保存更改" }));
  expect(await screen.findByRole("heading", { name: "更新上传" })).toBeInTheDocument();
});

test("user imports a credential-bearing URL with a custom User-Agent", async () => {
  const fullURL = "https://user:secret@example.test/sub?token=visible";
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    if (url.endsWith("/api/settings")) return Response.json({ language: "zh-CN", installationId: "installation-1" });
    if (url.endsWith("/api/health")) return Response.json({
      status: "healthy", backend: { status: "healthy" }, database: { status: "healthy" }, publishedSubscription: { status: "healthy" },
    });
    if (url.endsWith("/api/upstream-subscriptions") && init?.method === "POST") {
      expect(JSON.parse(String(init.body))).toEqual({ name: "URL 订阅", kind: "url", url: fullURL, userAgent: "Clash.Meta/1.19" });
      return Response.json({ id: "url-1", name: "URL 订阅", kind: "url", url: fullURL, userAgent: "Clash.Meta/1.19", proxyNodeCount: 1, enabled: true, refreshStatus: "success" }, { status: 201 });
    }
    if (url.endsWith("/api/upstream-subscriptions")) return Response.json([]);
    throw new Error(`unexpected request: ${url}`);
  });
  render(<App />);
  await screen.findByRole("heading", { name: "上游订阅" });
  await userEvent.type(screen.getByLabelText("名称"), "URL 订阅");
  await userEvent.type(screen.getByLabelText("完整 URL"), fullURL);
  await userEvent.type(screen.getByLabelText("User-Agent"), "Clash.Meta/1.19");
  await userEvent.click(screen.getByRole("button", { name: "添加上游订阅" }));
  expect(await screen.findByText(fullURL)).toBeInTheDocument();
});

test("failed refresh shows a stale Upstream Subscription and its reason", async () => {
  let refreshAttempted = false;
  const subscription = { id: "stale-1", name: "Stale", kind: "url", url: "https://example.test/sub", proxyNodeCount: 1, enabled: true, refreshStatus: "success" };
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    if (url.endsWith("/api/settings")) return Response.json({ language: "zh-CN", installationId: "installation-1" });
    if (url.endsWith("/api/health")) return Response.json({
      status: "healthy", backend: { status: "healthy" }, database: { status: "healthy" }, publishedSubscription: { status: "healthy" },
    });
    if (url.endsWith("/stale-1/refresh") && init?.method === "POST") {
      refreshAttempted = true;
      return Response.json({ error: "connection timed out" }, { status: 502 });
    }
    if (url.endsWith("/api/upstream-subscriptions")) {
      return Response.json([refreshAttempted ? { ...subscription, refreshStatus: "stale", lastError: "fetch Upstream Subscription: connection timed out" } : subscription]);
    }
    throw new Error(`unexpected request: ${url}`);
  });
  render(<App />);
  await screen.findByRole("heading", { name: "Stale" });
  await userEvent.click(screen.getByRole("button", { name: "刷新" }));
  expect(await screen.findByText("已陈旧，使用上次成功内容")).toBeInTheDocument();
  expect(screen.getByText("fetch Upstream Subscription: connection timed out")).toBeInTheDocument();
});

test("scoring settings keep the IPSuper threshold and expose provider diagnostics", async () => {
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    if (url.endsWith("/api/settings") && init?.method === "PUT") {
      expect(JSON.parse(String(init.body))).toMatchObject({
        scoringProvider: "ipsuper",
        ipsuperThreshold: 73,
      });
      return new Response(null, { status: 204 });
    }
    if (url.endsWith("/api/settings")) {
      return Response.json({
        language: "zh-CN",
        installationId: "installation-1",
        scoringProvider: "ipsuper",
        ipsuperThreshold: 73,
        scoringProviders: [
          { name: "ipsuper", enabled: true, failureStatus: "IPSuper aggregate security score was not found" },
        ],
      });
    }
    if (url.endsWith("/api/health")) {
      return Response.json({ status: "healthy", backend: { status: "healthy" }, database: { status: "healthy" }, publishedSubscription: { status: "healthy" } });
    }
    if (url.endsWith("/api/evaluation-runs/current")) return Response.json({ status: "idle", total: 0, passed: 0, failed: 0, results: [] });
    if (url.endsWith("/api/upstream-subscriptions")) return Response.json([]);
    throw new Error(`unexpected request: ${url}`);
  });

  render(<App />);
  const thresholdInput = await screen.findByLabelText(/IPSuper (pass threshold|合格阈值)/);
  expect(thresholdInput).toHaveValue(73);
  expect(screen.getByText(/IPSuper aggregate security score was not found/)).toBeInTheDocument();
  await userEvent.clear(thresholdInput);
  await userEvent.type(thresholdInput, "73");
  await userEvent.click(screen.getByRole("button", { name: "保存评分设置" }));
});

test("evaluation results show independent stage diagnostics and provider availability", async () => {
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = String(input);
    if (url.endsWith("/api/settings")) return Response.json({
      language: "en", installationId: "installation-1", scoringProvider: "ipsuper", ipsuperThreshold: 70,
      scoringProviders: [{ name: "ipsuper", enabled: true, status: "unavailable", failureStatus: "IPSuper provider unavailable: HTTP 403" }],
    });
    if (url.endsWith("/api/health")) return Response.json({ status: "healthy", backend: { status: "healthy" }, database: { status: "healthy" }, publishedSubscription: { status: "healthy" } });
    if (url.endsWith("/api/evaluation-runs/current")) return Response.json({
      status: "completed", total: 1, passed: 0, failed: 1, publicationResult: "retained",
      reason: "all scoring attempts failed; previous Publication Snapshot retained: IPSuper provider unavailable: HTTP 403",
      results: [{ name: "Mitce · node-1", state: "failed", attempts: 3, successful: 3, medianLatencyMs: 120, exitIdentity: "203.0.113.8", addressFamily: "ipv4", reason: "provider_unavailable: IPSuper provider unavailable: HTTP 403", stages: {
        availability: { status: "passed" }, exitIdentity: { status: "passed" }, ipScore: { status: "unavailable", reason: "IPSuper provider unavailable: HTTP 403" },
      } }],
    });
    if (url.endsWith("/api/evaluation-runs")) return Response.json([]);
    if (url.endsWith("/api/logs")) return Response.json([]);
    if (url.endsWith("/api/upstream-subscriptions")) return Response.json([]);
    throw new Error(`unexpected request: ${url}`);
  });

  render(<App />);
  expect(await screen.findByText("Availability Check")).toBeInTheDocument();
  expect(screen.getByText("Exit Identity")).toBeInTheDocument();
  expect(screen.getByText("IP Score")).toBeInTheDocument();
  expect(screen.getByText("IPSuper provider unavailable: HTTP 403")).toBeInTheDocument();
  expect(screen.getByText("Scoring provider failed; previous Published Subscription retained")).toBeInTheDocument();
  expect(screen.getByText(/previous Publication Snapshot retained/)).toBeInTheDocument();
  expect(screen.getAllByText(/Unavailable/).length).toBeGreaterThan(0);
});

test("evaluation polling refreshes provider availability status", async () => {
  let settingsCalls = 0;
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = String(input);
    if (url.endsWith("/api/settings")) {
      settingsCalls += 1;
      const unavailable = settingsCalls > 2;
      return Response.json({ language: "en", installationId: "installation-1", scoringProvider: "ipsuper", scoringProviders: [{ name: "ipsuper", enabled: true, status: unavailable ? "unavailable" : "unverified", failureStatus: unavailable ? "fixture provider outage" : "" }] });
    }
    if (url.endsWith("/api/health")) return Response.json({ status: "healthy", backend: { status: "healthy" }, database: { status: "healthy" }, publishedSubscription: { status: "healthy" } });
    if (url.endsWith("/api/evaluation-runs/current")) return Response.json({ status: "idle", total: 0, passed: 0, failed: 0, results: [] });
    if (url.endsWith("/api/evaluation-runs")) return Response.json([]);
    if (url.endsWith("/api/logs")) return Response.json([]);
    if (url.endsWith("/api/upstream-subscriptions")) return Response.json([]);
    throw new Error(`unexpected request: ${url}`);
  });

  render(<App />);
  expect(await screen.findByText(/IPSuper: Unverified/)).toBeInTheDocument();
  expect(await screen.findByText(/IPSuper: Unavailable/, {}, { timeout: 2500 })).toBeInTheDocument();
});

test("History shows durable run diagnostics and links to configuration export", async () => {
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = String(input);
    if (url.endsWith("/api/settings")) return Response.json({
      language: "en", installationId: "installation-1", scoringProvider: "ipsuper", ipsuperThreshold: 70,
      evaluationIntervalMinutes: 360, historyRetentionDays: 7, scoreCacheTTLMinutes: 1440, listenPort: 9876,
      availabilityAttempts: 3, availabilityRequiredSuccesses: 2, availabilityTimeoutSeconds: 5, availabilityMaxLatencyMs: 1500,
      availabilityURLs: [], evaluationWorkerCount: 3, scoringJitterMs: 100, scoringProviders: [],
    });
    if (url.endsWith("/api/health")) return Response.json({ status: "healthy", backend: { status: "healthy" }, database: { status: "healthy" }, publishedSubscription: { status: "healthy" } });
    if (url.endsWith("/api/evaluation-runs/current")) return Response.json({ status: "idle", total: 0, passed: 0, failed: 0, results: [] });
    if (url.endsWith("/api/evaluation-runs")) return Response.json([{
      id: "run-1", trigger: "scheduled", phase: "", status: "completed", startedAt: "2026-08-29T00:00:00Z", finishedAt: "2026-08-29T00:00:02Z", durationMs: 2000,
      total: 4, passed: 3, failed: 1, publicationResult: "published", failureSummary: "one node failed",
      phases: [{ name: "refresh", status: "completed", durationMs: 200 }, { name: "publication", status: "completed", durationMs: 300 }], results: [],
    }]);
    if (url.endsWith("/api/upstream-subscriptions")) return Response.json([]);
    throw new Error(`unexpected request: ${url}`);
  });

  render(<App />);
  expect(await screen.findByRole("heading", { name: "History" })).toBeInTheDocument();
  expect(screen.getByText(/scheduled/)).toBeInTheDocument();
  expect(screen.getByText(/2s/)).toBeInTheDocument();
  expect(screen.getByText(/published/)).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Export configuration JSON" })).toHaveAttribute("href", "/api/settings/export");
});

test("listener diagnostics show local and current Published Subscription entrypoints", async () => {
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    if (url.endsWith("/api/settings") && init?.method === "PUT") {
      expect(JSON.parse(String(init.body))).toMatchObject({ listenAddress: "0.0.0.0", listenPort: 19876 });
      return new Response(null, { status: 204 });
    }
    if (url.endsWith("/api/settings")) return Response.json({
      language: "en", installationId: "installation-1", listenAddress: "0.0.0.0", listenPort: 19876,
      localSubscriptionURL: "http://127.0.0.1:19876/sub/clash.yaml", subscriptionURL: "http://lan.example:19876/sub/clash.yaml",
      lanSubscriptionURLs: ["http://192.168.1.20:19876/sub/clash.yaml"],
      listenerError: "listen for Published Subscription: address already in use",
      scoringProvider: "ipsuper", ipsuperThreshold: 70, evaluationIntervalMinutes: 360, historyRetentionDays: 7,
      scoreCacheTTLMinutes: 1440, availabilityAttempts: 3, availabilityRequiredSuccesses: 2, availabilityTimeoutSeconds: 5,
      availabilityMaxLatencyMs: 1500, availabilityURLs: [], evaluationWorkerCount: 3, scoringJitterMs: 100, scoringProviders: [],
    });
    if (url.endsWith("/api/health")) return Response.json({ status: "healthy", backend: { status: "healthy" }, database: { status: "healthy" }, publishedSubscription: { status: "healthy" } });
    if (url.endsWith("/api/evaluation-runs/current")) return Response.json({ status: "idle", total: 0, passed: 0, failed: 0, results: [] });
    if (url.endsWith("/api/evaluation-runs")) return Response.json([]);
    if (url.endsWith("/api/logs")) return Response.json([]);
    if (url.endsWith("/api/upstream-subscriptions")) return Response.json([]);
    throw new Error(`unexpected request: ${url}`);
  });

  render(<App />);
  expect(await screen.findByText("Local subscription URL")).toBeInTheDocument();
  expect(screen.getByText("http://127.0.0.1:19876/sub/clash.yaml")).toBeInTheDocument();
  expect(screen.getByText("http://lan.example:19876/sub/clash.yaml")).toBeInTheDocument();
  expect(screen.getByText("http://192.168.1.20:19876/sub/clash.yaml")).toBeInTheDocument();
  expect(screen.getByText(/0\.0\.0\.0:19876/)).toBeInTheDocument();
  expect(screen.getByRole("alert")).toHaveTextContent("address already in use");
  await userEvent.click(screen.getByRole("button", { name: "Save scoring settings" }));
});

test("WebUI blocks an eleventh Upstream Subscription with a clear message", async () => {
  const sources = Array.from({ length: 10 }, (_, index) => ({
    id: `source-${index}`,
    name: `Source ${index + 1}`,
    kind: "paste",
    configuredDocument: "proxies: []",
    lastSuccessfulDocument: "proxies: []",
    proxyNodeCount: 1,
    enabled: true,
    refreshStatus: "success",
  }));
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = String(input);
    if (url.endsWith("/api/settings")) return Response.json({ language: "zh-CN", installationId: "installation-1" });
    if (url.endsWith("/api/health")) return Response.json({
      status: "healthy", backend: { status: "healthy" }, database: { status: "healthy" }, publishedSubscription: { status: "healthy" },
    });
    if (url.endsWith("/api/upstream-subscriptions")) return Response.json(sources);
    throw new Error(`unexpected request: ${url}`);
  });
  render(<App />);
  expect(await screen.findByText("最多只能添加 10 个上游订阅。请先删除一个上游订阅。")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "添加上游订阅" })).toBeDisabled();
});

test("user edits, disables, and deletes an Upstream Subscription", async () => {
  const document = "proxies:\n  - name: managed\n    type: ss\n    server: managed.example\n    port: 443\n";
  let source = { id: "managed-1", name: "Managed", kind: "paste", configuredDocument: document, lastSuccessfulDocument: document, proxyNodeCount: 1, enabled: true, refreshStatus: "success" };
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    if (url.endsWith("/api/settings")) return Response.json({ language: "zh-CN", installationId: "installation-1" });
    if (url.endsWith("/api/health")) return Response.json({
      status: "healthy", backend: { status: "healthy" }, database: { status: "healthy" }, publishedSubscription: { status: "healthy" },
    });
    if (url.endsWith("/api/upstream-subscriptions")) return Response.json([source]);
    if (url.endsWith("/managed-1") && init?.method === "PUT") {
      const update = JSON.parse(String(init.body));
      source = { ...source, name: update.name, configuredDocument: update.document, lastSuccessfulDocument: update.document };
      return Response.json(source);
    }
    if (url.endsWith("/managed-1") && init?.method === "PATCH") {
      source = { ...source, enabled: false };
      return Response.json(source);
    }
    if (url.endsWith("/managed-1") && init?.method === "DELETE") return new Response(null, { status: 204 });
    throw new Error(`unexpected request: ${url}`);
  });
  render(<App />);
  await screen.findByRole("heading", { name: "Managed" });
  await userEvent.click(screen.getByRole("button", { name: "编辑" }));
  const nameInput = screen.getByLabelText("名称");
  await userEvent.clear(nameInput);
  await userEvent.type(nameInput, "Edited");
  await userEvent.click(screen.getByRole("button", { name: "保存更改" }));
  expect(await screen.findByRole("heading", { name: "Edited" })).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "禁用" }));
  expect(await screen.findByRole("button", { name: "启用" })).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "删除" }));
  await waitFor(() => expect(screen.queryByRole("heading", { name: "Edited" })).not.toBeInTheDocument());
});
