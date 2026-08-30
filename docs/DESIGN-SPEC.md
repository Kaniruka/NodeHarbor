# NodeHarbor Design Specification

- Status: Confirmed for implementation
- Version: 0.3
- Frozen on: 2026-08-30
- Implementation: In progress

This document records the product and engineering decisions agreed during the design interview. The domain vocabulary in [`CONTEXT.md`](../CONTEXT.md) is normative.

## 1. Product boundary

NodeHarbor is a single-user, self-hosted, open-source tool that aggregates Clash/Mihomo subscriptions, rejects unusable proxy nodes, obtains external IP scores, and publishes a new Clash/Mihomo subscription.

The first release targets personal use rather than a hosted multi-user service. It does not implement accounts, tenant isolation, billing, cloud publication, automatic upload, or remote administration.

## 2. Supported platforms and deliverables

- Windows 10/11 x64 as a self-contained ZIP archive.
- A pinned Chromium-based Managed Browser Runtime included in the Windows package.
- Project license: MIT.
- Product ID: `NodeHarbor` / `nodeharbor`.
- User interface languages: Simplified Chinese and English, selected from the browser locale.

The Windows build is a persistent executable that starts the local server and opens the management UI in the user's default browser. It does not install a Windows service and does not configure automatic startup. The Managed Browser Runtime is started and supervised only when scoring requires it.

## 3. Technology baseline

- Backend: Go.
- Frontend: React, TypeScript, and Vite.
- Persistence: SQLite.
- Proxy engine: a pinned, bundled Windows Mihomo binary.
- Scoring runtime: a pinned, bundled Chromium and Playwright Go integration.
- Frontend output: static assets embedded by the Windows web server.

Every release pins the Mihomo and Chromium/Playwright versions, verifies distributable digests where available, and includes their licenses in `THIRD_PARTY_NOTICES`.

## 4. Capacity and input

- At most 10 upstream subscriptions.
- Design capacity of approximately 500 parsed proxy nodes.
- Accepted document format: Clash/Mihomo YAML only.
- Input methods: subscription URL, uploaded YAML file, or pasted YAML text.
- URL sources use a common Mihomo-compatible User-Agent by default and may override the User-Agent per source.
- Arbitrary custom HTTP headers are outside the first-release scope.

All proxy-node YAML fields are preserved. NodeHarbor does not reconstruct protocol-specific fields or maintain its own proxy-protocol whitelist. Compatibility is determined by loading the node through the bundled Mihomo version.

Published display names use `[upstream short name] original node name`. A short suffix is added only if that name still collides. Internal identity uses a stable fingerprint of the normalized proxy configuration.

## 5. Evaluation pipeline

An evaluation run is globally serialized. If a manual or scheduled request arrives while a run is active, NodeHarbor coalesces it into at most one subsequent run.

The pipeline is:

1. Refresh upstream subscriptions.
2. Parse and preserve candidate proxy nodes.
3. Validate candidates with the bundled Mihomo engine.
4. Perform the availability check.
5. Determine the actual exit identity through the test channel.
6. Reuse a valid score cache entry or open a Browser Scoring Session for the selected scoring provider.
7. Select qualified nodes.
8. Generate and validate a complete publication snapshot.
9. Atomically replace the previous published subscription.

Node-level checks use at most three concurrent workers and add a small randomized delay between scoring-provider requests. Browser scoring defaults to one concurrent Browser Scoring Session and may be raised to three through advanced configuration.

## 6. Availability check

Availability is tested before any IP-scoring request. It is not a bulk-download throughput benchmark.

Default rules:

- Test each proxy node up to three times.
- Require at least two successful requests.
- Reject a single attempt after five seconds.
- Require successful-request median latency of at most 1500 milliseconds.
- Try `https://www.gstatic.com/generate_204` and `https://cp.cloudflare.com/generate_204`.
- Treat success from either test URL as a successful attempt.

Timeout, success requirement, latency threshold, and test URLs are configurable.

## 7. Exit identity and IP scoring

IPSuper is the sole scoring provider. It has its own enabled state, threshold, browser adapter, cache, and failure status; its aggregate security score is never combined with another provider score.

IPSuper starts with a threshold of 70, configurable from 0 through 100. A proxy node qualifies only when it passes the availability check and its IPSuper score meets that threshold.

The first release prefers an IPv4 exit identity and falls back to IPv6 when IPv4 is unavailable. The UI records the address family that was scored.

Score-cache keys are `(scoring provider, exit IP address)`. The default time-to-live is 24 hours. Cache expiry or an explicit ignore-cache run requires a new request.

If a new exit identity cannot be scored, its nodes are excluded. If a previous successful score exists, it may be reused for at most 24 hours. Multiple nodes sharing one exit identity reuse the same score, but all qualified nodes remain in the published subscription by default.

### 7.1 Website-adapter constraint

IPSuper is queried through its public page in a bounded Test Channel browser session; it is not treated as a documented, stable bulk-scoring API. The adapter extracts only the rendered aggregate security score and treats challenge pages, HTTP failures, missing scores, and page-shape changes as unavailable.

NodeHarbor therefore uses a replaceable, best-effort IPSuper adapter backed by the Managed Browser Runtime. Each Browser Scoring Session creates a fresh Browser Context for one Proxy Node, verifies the Exit Identity through the same Browser Proxy Endpoint, renders the IPSuper page, and extracts the visible aggregate security score from the DOM. The default navigation/rendering deadline is 15 seconds. Transport or browser-process failure may be retried once; HTTP 403, CAPTCHA, challenge, rate limit, or missing score is not retried. Adapter failures and website updates are reported as `score unavailable`; they must not corrupt or empty the previous publication snapshot.

The browser runs headless by default and can be switched to headed diagnostic mode. Its debug interface binds only to loopback on a random port. Browser Context state is ephemeral: cookies, LocalStorage, cache, and credentials are not reused between sessions.

## 8. Stale and failed inputs

When an upstream refresh fails, NodeHarbor retains the last successfully parsed document for up to seven days and marks it stale. After seven days without a successful refresh, its nodes leave the candidate set.

If no upstream succeeds during a run, or every score is unavailable because the provider failed, NodeHarbor retains the previous publication snapshot.

## 9. Published subscription

The generated YAML contains:

- all qualified proxy nodes;
- an `AUTO` latency-selection group;
- a `FALLBACK` failover group;
- a `SELECT` manual-selection group whose default is `AUTO`;
- only the minimum additional fields required for a valid Clash/Mihomo subscription.

It does not inherit upstream rules, DNS settings, TUN settings, scripts, or unrelated proxy groups. It does not implement manual node exclusion, exclusion regular expressions, or force-inclusion overrides.

Before publication, the complete generated YAML is loaded and validated by the bundled Mihomo. Publication is an atomic replacement; clients never receive a partially written document.

Default URLs:

```text
http://127.0.0.1:9876/sub/clash.yaml
http://<LAN address>:9876/sub/clash.yaml
```

The port is configurable. The local root path `/` serves the management UI. LAN clients can access only `/sub/clash.yaml`; management UI and management APIs remain loopback-only.

## 10. Scheduling and retention

- Manual evaluation is always available.
- Scheduled evaluation defaults to every six hours.
- Detection history retention is configurable from three through seven days and defaults to seven days.
- Current subscriptions, settings, latest node states, evaluation runs, score cache, and retained history are stored in SQLite.
- Configuration can be exported as JSON; users do not edit the database directly.

## 11. Local security model

NodeHarbor assumes a trusted single-user host and trusted LAN. It does not add an authentication token to the published subscription URL. Subscription URLs, proxy credentials, and logs are not masked by the application.

The management surface remains loopback-only. Enabling LAN management is outside the first-release scope. Platform file permissions should restrict local state to the current Windows user.

## 12. WebUI scope

The responsive, mobile-first WebUI contains:

1. Dashboard: run status, counts, last publication, and current health.
2. Upstream subscriptions: add, edit, refresh, remove, and view errors.
3. Nodes: availability, median latency, exit IP, address family, score, provider, and qualification result.
4. History: evaluation results retained for the configured three-to-seven-day window.
5. Settings and logs: thresholds, schedule, retention, port, provider, test URLs, cache controls, browser runtime health, diagnostic mode, and diagnostics.

Accounts, complex charts, a theme marketplace, drag-and-drop workflow editing, and a general YAML editor are excluded.

## 13. Mihomo isolation

NodeHarbor never enables TUN, changes the operating-system proxy, or installs transparent-proxy rules. It uses Mihomo only through isolated local test listeners.

The application owns and supervises only its bundled core. It uses a distinct executable/process name, data directory, control socket or port, PID file, and log files. It never uses broad commands such as `killall mihomo` and never edits another proxy application's configuration.

## 14. Windows process and browser isolation

NodeHarbor owns and supervises only its bundled Mihomo core and Managed Browser Runtime. It uses distinct process names, data directories, control endpoints, PID ownership checks, and log files. It never changes the Windows system proxy, starts a TUN, exposes a browser debug interface to LAN clients, or controls a foreign browser or proxy process.

For each Proxy Node, the Test Channel provides a temporary loopback Browser Proxy Endpoint. The Browser Context must use that endpoint explicitly; inability to create or verify it fails closed. A missing or unrecoverable Managed Browser Runtime marks the run `runtime_unavailable`, preserves the previous Publication Snapshot, and does not turn every node into a false low-score result.

Failure evidence is limited to the latest 20 provider failures and expires after 24 hours. It may include status summaries, page titles, HTTP status, error reasons, and screenshots, but never cookies, LocalStorage, credentials, or complete network captures.

## 15. Failure guarantees

- No scoring-provider outage may erase a previously valid publication.
- No partial YAML may be served.
- A failed evaluation records a diagnosable status and leaves the last good publication available.

## 16. Initial acceptance criteria

- The Windows x64 package starts the local WebUI and bundled Mihomo core.
- The package contains the pinned Managed Browser Runtime or reports an actionable runtime-unavailable error.
- The system accepts up to 10 Clash/Mihomo YAML inputs and safely evaluates approximately 500 nodes.
- Availability checks precede scoring and enforce the configured success, timeout, and latency rules.
- The browser-backed IPSuper adapter can obtain and cache aggregate security scores when the website permits it, and degrades safely when it does not.
- Browser scoring uses an isolated Context and the verified Browser Proxy Endpoint, with deterministic local fixture coverage and an explicit non-CI live smoke test.
- The output contains only qualified nodes and the three generated groups, and the bundled Mihomo accepts it.
- LAN clients can fetch the published subscription but cannot reach management APIs.
- Publication remains unchanged after an upstream-wide failure, provider-wide failure, invalid generated YAML, or process interruption.

## 17. Explicitly excluded from the first release

- Multi-user hosting and authentication.
- Public/cloud subscription publication.
- Formats other than Clash/Mihomo YAML.
- Full-bandwidth download benchmarking.
- System proxy or TUN management by NodeHarbor.
- Manual node exclusion and force inclusion.
- Windows service or automatic startup.
- Automatic Mihomo or browser-runtime updates.
- Automatic CAPTCHA solving or user-assisted challenge completion.
