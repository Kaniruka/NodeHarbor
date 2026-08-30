import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import App from "./App";

const settings = { language: "zh-CN", subscriptionURL: "http://127.0.0.1:9876/sub/clash.yaml", ipsuperThreshold: 70, scoringProviders: [{ name: "ipsuper", enabled: true, status: "available" }], availabilityURLs: [] };
const source = { id: "source-1", name: "家庭来源", kind: "paste", configuredDocument: "proxies: []", proxyNodeCount: 1, enabled: true, refreshStatus: "success" };
const currentRun = { status: "completed", total: 1, passed: 1, failed: 0, startedAt: "2026-08-30T10:00:00Z", finishedAt: "2026-08-30T10:01:00Z", results: [{ nodeId: "node-1", sourceId: "source-1", sourceName: "家庭来源", name: "家庭来源 · Tokyo", state: "passed", config: { type: "vless", network: "ws", udp: true }, medianLatencyMs: 120, ipScore: 88, scoreSource: "cache" }] };

afterEach(() => { cleanup(); window.location.hash = ""; vi.restoreAllMocks(); });

function mockAPI() {
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    if (url.endsWith("/api/settings") && init?.method === "PUT") return new Response(null, { status: 204 });
    if (url.endsWith("/api/settings")) return Response.json(settings);
    if (url.endsWith("/api/upstream-subscriptions/source-1/nodes")) return Response.json([{ id: "node-1", name: "家庭来源 · Tokyo", config: { type: "vless", network: "ws", udp: true }, state: "accepted" }]);
    if (url.endsWith("/api/upstream-subscriptions")) return Response.json([source]);
    if (url.endsWith("/api/evaluation-runs/current")) return Response.json(currentRun);
    if (url.endsWith("/api/publication")) return Response.json({ status: "published", publishedAt: "2026-08-30T10:01:00Z", groups: [{ subscriptionId: "source-1", subscriptionName: "家庭来源", nodes: currentRun.results.map((item) => ({ nodeId: item.nodeId, name: item.name, config: item.config, medianLatencyMs: item.medianLatencyMs, ipScore: item.ipScore, scoreSource: item.scoreSource })) }] });
    if (url.endsWith("/api/logs")) return Response.json([{ timestamp: "2026-08-30T10:01:00Z", level: "info", runId: "run-1", message: "Evaluation Run completed" }]);
    throw new Error(`unexpected request: ${url}`);
  });
}

test("renders six destinations and keeps evaluation out of Overview", async () => {
  mockAPI(); const writeText = vi.fn().mockResolvedValue(undefined); Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } }); render(<App />);
  expect(await screen.findByRole("heading", { name: "概览" })).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "订阅" })).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "节点" })).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "发布" })).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "设置" })).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "日志" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "开始评估" })).not.toBeInTheDocument();
  expect(screen.getByText("达标节点")).toBeInTheDocument();
  expect(screen.queryByText("家庭来源")).not.toBeInTheDocument();
  expect(await screen.findByText("可用", { selector: "strong" })).toBeInTheDocument();
  expect(screen.getByText("http://127.0.0.1:9876/sub/clash.yaml")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "复制稳定订阅链接" }));
  expect(writeText).toHaveBeenCalledWith("http://127.0.0.1:9876/sub/clash.yaml");
  expect(await screen.findByRole("button", { name: "已复制" })).toBeInTheDocument();
});

test("Subscriptions owns source management and shows unknown metadata without node results", async () => {
  mockAPI(); render(<App />); await userEvent.click(await screen.findByRole("link", { name: "订阅" }));
  expect(await screen.findByRole("heading", { name: "订阅" })).toBeInTheDocument();
  expect(screen.getByRole("heading", { name: "家庭来源" })).toBeInTheDocument();
  expect(screen.getAllByText("未知").length).toBeGreaterThanOrEqual(2);
  expect(screen.queryByText("Tokyo")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "开始评估" })).not.toBeInTheDocument();
});

test("Subscriptions supports import, edit, refresh, enable/disable, and delete", async () => {
  let subscriptions: Array<Record<string, any>> = [{ ...source, remainingTrafficBytes: undefined, trafficTotalBytes: undefined, expiresAt: undefined }];
  const calls: string[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    if (url.endsWith("/api/settings")) return Response.json(settings);
    if (url.endsWith("/api/upstream-subscriptions") && !init?.method) return Response.json(subscriptions);
    if (url.endsWith("/api/upstream-subscriptions") && init?.method === "POST") {
      const body = JSON.parse(String(init.body));
      const created = { ...source, id: "source-2", name: body.name, kind: body.kind, configuredDocument: body.document, proxyNodeCount: 1 };
      subscriptions = [...subscriptions, created]; calls.push("create");
      return Response.json(created, { status: 201 });
    }
    const match = url.match(/\/api\/upstream-subscriptions\/(source-[12])(?:\/([^/]+))?$/);
    if (!match) throw new Error(`unexpected request: ${url}`);
    const id = match[1]; const action = match[2];
    if (init?.method === "PUT") {
      const body = JSON.parse(String(init.body)); subscriptions = subscriptions.map((item) => item.id === id ? { ...item, name: body.name, configuredDocument: body.document } : item); calls.push("edit");
      return Response.json(subscriptions.find((item) => item.id === id));
    }
    if (init?.method === "PATCH") {
      const body = JSON.parse(String(init.body)); subscriptions = subscriptions.map((item) => item.id === id ? { ...item, enabled: body.enabled } : item); calls.push(body.enabled ? "enable" : "disable");
      return Response.json(subscriptions.find((item) => item.id === id));
    }
    if (init?.method === "POST" && action === "refresh") {
      subscriptions = subscriptions.map((item) => item.id === id ? { ...item, refreshStatus: "success", lastSuccessAt: "2026-08-30T11:00:00Z" } : item); calls.push("refresh");
      return Response.json(subscriptions.find((item) => item.id === id));
    }
    if (init?.method === "DELETE") { subscriptions = subscriptions.filter((item) => item.id !== id); calls.push("delete"); return new Response(null, { status: 204 }); }
    throw new Error(`unexpected mutation: ${url}`);
  });

  render(<App />); await userEvent.click(await screen.findByRole("link", { name: "订阅" }));
  expect(screen.getByRole("button", { name: "订阅 URL" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "上传文件" })).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "粘贴 YAML" }));
  await userEvent.type(screen.getByLabelText("名称"), "新增来源");
  await userEvent.type(screen.getByLabelText("YAML 内容"), "proxies:\n  - name: pasted\n    type: ss");
  await userEvent.click(screen.getByRole("button", { name: "添加订阅" }));
  expect(await screen.findByRole("heading", { name: "新增来源" })).toBeInTheDocument();
  const card = () => screen.getByRole("heading", { name: "新增来源" }).closest("article") as HTMLElement;
  await userEvent.click(within(card()).getByRole("button", { name: "编辑" }));
  const nameInput = screen.getByLabelText("名称"); await userEvent.clear(nameInput); await userEvent.type(nameInput, "已编辑来源");
  await userEvent.click(screen.getByRole("button", { name: "保存更改" }));
  const editedCard = () => screen.getByRole("heading", { name: "已编辑来源" }).closest("article") as HTMLElement;
  await userEvent.click(within(editedCard()).getByRole("button", { name: "手动更新" }));
  await userEvent.click(within(editedCard()).getByRole("button", { name: "禁用" }));
  expect(await within(editedCard()).findByRole("button", { name: "启用" })).toBeInTheDocument();
  await userEvent.click(within(editedCard()).getByRole("button", { name: "启用" }));
  await userEvent.click(within(editedCard()).getByRole("button", { name: "删除" }));
  expect(screen.queryByRole("heading", { name: "已编辑来源" })).not.toBeInTheDocument();
  expect(calls).toEqual(["create", "edit", "refresh", "disable", "enable", "delete"]);
});

test("Nodes has one global evaluation control and compact grouped cards", async () => {
  mockAPI(); render(<App />); await userEvent.click(await screen.findByRole("link", { name: "节点" }));
  expect(await screen.findByRole("heading", { name: "节点" })).toBeInTheDocument();
  expect(screen.getAllByRole("button", { name: "开始评估" })).toHaveLength(1);
  expect(screen.getByText("vless")).toBeInTheDocument();
  expect(screen.getByText("120 ms")).toBeInTheDocument();
  expect(screen.getByText("88")).toBeInTheDocument();
  expect(screen.getByText(/WS · UDP/)).toBeInTheDocument();
  expect(screen.getAllByRole("button", { name: /评估|evaluate/i })).toHaveLength(1);
  const summary = screen.getByText("家庭来源").closest("summary");
  if (!summary) throw new Error("subscription summary not found");
  await userEvent.click(summary);
  expect(summary.parentElement).not.toHaveAttribute("open");
});

test("Nodes starts one global run and disables the control while it is running", async () => {
  let run = { ...currentRun, status: "completed" };
  let starts = 0;
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    if (url.endsWith("/api/settings")) return Response.json(settings);
    if (url.endsWith("/api/upstream-subscriptions")) return Response.json([source]);
    if (url.endsWith("/api/upstream-subscriptions/source-1/nodes")) return Response.json([]);
    if (url.endsWith("/api/evaluation-runs/current")) return Response.json(run);
    if (url.endsWith("/api/evaluation-runs") && init?.method === "POST") { starts++; run = { ...run, status: "running" }; return Response.json(run, { status: 202 }); }
    throw new Error(`unexpected request: ${url}`);
  });
  render(<App />); await userEvent.click(await screen.findByRole("link", { name: "节点" }));
  const startButtons = await screen.findAllByRole("button", { name: "开始评估" });
  expect(startButtons).toHaveLength(1);
  const startButton = startButtons[0];
  await userEvent.click(startButton);
  expect(starts).toBe(1);
  expect(await screen.findByRole("button", { name: "运行中" })).toBeDisabled();
});

test("Nodes renders failed evaluation and scoring states without presenting a speed score", async () => {
  const failedRun = { ...currentRun, status: "failed", passed: 0, failed: 1, results: [{ ...currentRun.results[0], state: "failed", ipScore: undefined, reason: "provider_unavailable: fixture provider outage", stages: { availability: { status: "passed" }, ipScore: { status: "unavailable", reason: "fixture provider outage" } } }] };
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = String(input);
    if (url.endsWith("/api/settings")) return Response.json(settings);
    if (url.endsWith("/api/upstream-subscriptions")) return Response.json([source]);
    if (url.endsWith("/api/upstream-subscriptions/source-1/nodes")) return Response.json([]);
    if (url.endsWith("/api/evaluation-runs/current")) return Response.json(failedRun);
    throw new Error(`unexpected request: ${url}`);
  });
  render(<App />); await userEvent.click(await screen.findByRole("link", { name: "节点" }));
  expect(await screen.findByText("provider_unavailable: fixture provider outage")).toBeInTheDocument();
  expect(screen.getByText("IP Score").parentElement?.parentElement).toHaveTextContent("—");
  expect(screen.queryByText("速度评分")).not.toBeInTheDocument();
});

test("Publish shows only the atomic publication snapshot using the same grouping", async () => {
  mockAPI(); const writeText = vi.fn().mockResolvedValue(undefined); Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } }); render(<App />); await userEvent.click(await screen.findByRole("link", { name: "发布" }));
  expect(await screen.findByRole("heading", { name: "发布" })).toBeInTheDocument();
  expect(screen.getByText("家庭来源 · Tokyo")).toBeInTheDocument();
  expect(screen.getByText("已发布")).toBeInTheDocument();
  expect(screen.getByText(/2026/)).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "复制稳定订阅链接" }));
  expect(writeText).toHaveBeenCalledWith("http://127.0.0.1:9876/sub/clash.yaml");
  const summary = screen.getByText("家庭来源").closest("summary");
  if (!summary) throw new Error("publication summary not found");
  await userEvent.click(summary);
  expect(summary.parentElement).not.toHaveAttribute("open");
  expect(screen.queryByRole("button", { name: "开始评估" })).not.toBeInTheDocument();
});

test("Publish renders only nodes present in the current snapshot", async () => {
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = String(input);
    if (url.endsWith("/api/settings")) return Response.json(settings);
    if (url.endsWith("/api/publication")) return Response.json({ status: "retained", publishedAt: "2026-08-30T10:01:00Z", groups: [{ subscriptionId: "source-1", subscriptionName: "家庭来源", nodes: [{ nodeId: "published-1", name: "家庭来源 · Published", config: { type: "ss" }, medianLatencyMs: 95, ipScore: 82 }] }] });
    throw new Error(`unexpected request: ${url}`);
  });
  render(<App />); await userEvent.click(await screen.findByRole("link", { name: "发布" }));
  expect(await screen.findByText("家庭来源 · Published")).toBeInTheDocument();
  expect(screen.queryByText("家庭来源 · Candidate")).not.toBeInTheDocument();
  expect(screen.getByText("保留上一版")).toBeInTheDocument();
});

test("Settings and Logs are separate destinations and History is not exposed", async () => {
  mockAPI(); render(<App />); await userEvent.click(await screen.findByRole("link", { name: "设置" }));
  expect(await screen.findByRole("heading", { name: "设置" })).toBeInTheDocument();
  expect(screen.getByLabelText("IPSuper 合格阈值")).toHaveValue(70);
  expect(screen.queryByText("评估历史")).not.toBeInTheDocument();
  await userEvent.click(await screen.findByRole("link", { name: "日志" }));
  expect(await screen.findByRole("heading", { name: "日志" })).toBeInTheDocument();
  expect(await screen.findByText("Evaluation Run completed")).toBeInTheDocument();
});
