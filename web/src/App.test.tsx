import { cleanup, render, screen } from "@testing-library/react";
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
  mockAPI(); render(<App />);
  expect(await screen.findByRole("heading", { name: "概览" })).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "订阅" })).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "节点" })).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "发布" })).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "设置" })).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "日志" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "开始评估" })).not.toBeInTheDocument();
  expect(screen.getByText("达标节点")).toBeInTheDocument();
});

test("Subscriptions owns source management and shows unknown metadata without node results", async () => {
  mockAPI(); render(<App />); await userEvent.click(await screen.findByRole("link", { name: "订阅" }));
  expect(await screen.findByRole("heading", { name: "订阅" })).toBeInTheDocument();
  expect(screen.getByRole("heading", { name: "家庭来源" })).toBeInTheDocument();
  expect(screen.getAllByText("未知").length).toBeGreaterThanOrEqual(2);
  expect(screen.queryByText("Tokyo")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "开始评估" })).not.toBeInTheDocument();
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

test("Publish shows only the atomic publication snapshot using the same grouping", async () => {
  mockAPI(); render(<App />); await userEvent.click(await screen.findByRole("link", { name: "发布" }));
  expect(await screen.findByRole("heading", { name: "发布" })).toBeInTheDocument();
  expect(screen.getByText("家庭来源 · Tokyo")).toBeInTheDocument();
  expect(screen.getByText("已发布")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "开始评估" })).not.toBeInTheDocument();
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
