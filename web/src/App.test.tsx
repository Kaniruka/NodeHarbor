import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import App from "./App";

afterEach(() => vi.restoreAllMocks());

test("shows health in Simplified Chinese and switches every visible label to English", async () => {
  const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input);
    if (url.endsWith("/api/settings") && init?.method === "PUT") {
      return new Response(null, { status: 204 });
    }
    if (url.endsWith("/api/settings")) {
      return Response.json({ language: "zh-CN", installationId: "installation-1" });
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
