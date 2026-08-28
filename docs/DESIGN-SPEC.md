# NodeHarbor Design Specification

- Status: Confirmed for implementation
- Version: 0.2
- Frozen on: 2026-08-28
- Implementation: Not started

This document records the product and engineering decisions agreed during the design interview. The domain vocabulary in [`CONTEXT.md`](../CONTEXT.md) is normative.

## 1. Product boundary

NodeHarbor is a single-user, self-hosted, open-source tool that aggregates Clash/Mihomo subscriptions, rejects unusable proxy nodes, obtains external IP scores, and publishes a new Clash/Mihomo subscription.

The first release targets personal use rather than a hosted multi-user service. It does not implement accounts, tenant isolation, billing, cloud publication, automatic upload, or remote administration.

## 2. Supported platforms and deliverables

- Windows 10/11 x64 as a ZIP archive.
- KernelSU on Android arm64-v8 as a module ZIP.
- Chromium-based browsers and Android Chromium WebView.
- Project license: MIT.
- Product and KernelSU module ID: `NodeHarbor` / `nodeharbor`.
- User interface languages: Simplified Chinese and English, selected from the browser locale.

The Windows build is a persistent executable that starts the local server and opens the browser. It does not install a Windows service and does not configure automatic startup.

The KernelSU build starts its daemon from `service.sh`. Scheduled work continues while the WebUI is closed. The WebUI is a control surface, not a process supervisor.

## 3. Technology baseline

- Backend: Go.
- Frontend: React, TypeScript, and Vite.
- Persistence: SQLite.
- Proxy engine: a pinned, bundled Mihomo binary for each target platform.
- Frontend output: static assets shared by the Windows web server and KernelSU `webroot`.

Every release pins the Mihomo version, verifies its SHA-256 digest, and includes its license in `THIRD_PARTY_NOTICES`. Other bundled assets require separate license review.

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
6. Reuse a valid score cache entry or query the selected scoring provider.
7. Select qualified nodes.
8. Generate and validate a complete publication snapshot.
9. Atomically replace the previous published subscription.

Node-level checks use at most three concurrent workers and add a small randomized delay between scoring-provider requests. The implementation may lower concurrency when a provider or platform requires stricter serialization.

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

The default scoring provider is IPLark. IPCheck.ing is an alternative provider. Each provider has its own enabled state, threshold, adapter, cache, and failure status. Scores from different providers are never combined or treated as equivalent.

Both providers start with a threshold of 70, configurable from 0 through 100. A proxy node qualifies only when it passes the availability check and its selected provider score meets that provider's threshold.

The first release prefers an IPv4 exit identity and falls back to IPv6 when IPv4 is unavailable. The UI records the address family that was scored.

Score-cache keys are `(scoring provider, exit IP address)`. The default time-to-live is 24 hours. Switching providers, cache expiry, or an explicit ignore-cache run requires a new request.

If a new exit identity cannot be scored, its nodes are excluded. If a previous successful score exists, it may be reused for at most 24 hours. Multiple nodes sharing one exit identity reuse the same score, but all qualified nodes remain in the published subscription by default.

### 7.1 Website-adapter constraint

IPLark was observed to request `/ipscore`; arbitrary-IP pages request `/ipscore?ip=<address>&token=<page token>`. Direct non-browser requests can receive HTTP 403. Neither IPLark nor IPCheck.ing is treated as a documented, stable bulk-scoring API.

NodeHarbor therefore uses replaceable, best-effort provider adapters. It does not bundle a headless browser. Adapter failures, anti-bot challenges, token changes, rate limits, and website updates are reported as `score unavailable`; they must not corrupt or empty the previous publication snapshot.

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

The management surface remains loopback-only. Enabling LAN management is outside the first-release scope. Platform file permissions should restrict local state to the current Windows user or the KernelSU module environment.

## 12. WebUI scope

The responsive, mobile-first WebUI contains:

1. Dashboard: run status, counts, last publication, and current health.
2. Upstream subscriptions: add, edit, refresh, remove, and view errors.
3. Nodes: availability, median latency, exit IP, address family, score, provider, and qualification result.
4. History: evaluation results retained for the configured three-to-seven-day window.
5. Settings and logs: thresholds, schedule, retention, port, provider, test URLs, cache controls, and diagnostics.

Accounts, complex charts, a theme marketplace, drag-and-drop workflow editing, and a general YAML editor are excluded.

## 13. Mihomo isolation

NodeHarbor never enables TUN, changes the operating-system proxy, or installs transparent-proxy rules. It uses Mihomo only through isolated local test listeners.

The application owns and supervises only its bundled core. It uses a distinct executable/process name, data directory, control socket or port, PID file, and log files. It never uses broad commands such as `killall mihomo` and never edits another proxy application's configuration.

## 14. Surfing coexistence on KernelSU

Surfing may run another Mihomo instance and may install REDIRECT, TPROXY, DNS, or TUN routing. NodeHarbor must coexist without modifying Surfing's module, `/data/adb/box_bll`, firewall chains, route tables, configuration, processes, or ports.

Required isolation:

- Use module ID `nodeharbor` and a NodeHarbor-owned data directory.
- Use a renamed core executable such as `nodeharbor-core` and exact PID ownership checks.
- Avoid Surfing's known default ports, including 7890, 7891, 1536, 1053, and 9090.
- Probe every intended listener before starting and choose internal test ports dynamically.
- Bind core control surfaces to loopback only.
- Detect Surfing and, where safe, launch NodeHarbor networking processes with the UID/GID identity that Surfing already exempts from REDIRECT/TPROXY interception.
- Verify the observed exit identity before accepting a score or publishing a new snapshot.
- Fail closed: uncertainty about routing isolation pauses publication and preserves the previous snapshot.

### 14.1 Accepted behavior: Surfing TUN mode

Surfing's TUN mode may recapture NodeHarbor's underlying proxy-engine connections at the routing layer even when UID/GID bypass works for REDIRECT or TPROXY.

The first release detects active Surfing TUN mode, pauses evaluation, displays an incompatibility warning, and continues serving the previous publication snapshot. It does not modify or stop Surfing. Supporting simultaneous Surfing TUN evaluation would require a separately designed and device-tested network-namespace or physical-interface binding solution.

## 15. Failure guarantees

- No scoring-provider outage may erase a previously valid publication.
- No partial YAML may be served.
- No NodeHarbor stop, update, or uninstall action may stop Surfing or another Mihomo process.
- No Surfing stop, reload, or update may cause NodeHarbor to adopt or kill a foreign process.
- A failed evaluation records a diagnosable status and leaves the last good publication available.

## 16. Initial acceptance criteria

- Windows x64 and KernelSU arm64-v8 packages start their respective local WebUI and background service.
- The system accepts up to 10 Clash/Mihomo YAML inputs and safely evaluates approximately 500 nodes.
- Availability checks precede scoring and enforce the configured success, timeout, and latency rules.
- A provider adapter can obtain and cache an IPLark score when the website permits it, and degrades safely when it does not.
- The output contains only qualified nodes and the three generated groups, and the bundled Mihomo accepts it.
- LAN clients can fetch the published subscription but cannot reach management APIs.
- Publication remains unchanged after an upstream-wide failure, provider-wide failure, invalid generated YAML, or process interruption.
- With Surfing in REDIRECT or TPROXY mode, neither module modifies, stops, or adopts the other's process, configuration, ports, or routing state.
- With Surfing TUN mode active, NodeHarbor pauses evaluation, reports the incompatibility, and continues serving the previous publication snapshot.

## 17. Explicitly excluded from the first release

- Multi-user hosting and authentication.
- Public/cloud subscription publication.
- Formats other than Clash/Mihomo YAML.
- Full-bandwidth download benchmarking.
- System proxy or TUN management by NodeHarbor.
- Manual node exclusion and force inclusion.
- Windows service or automatic startup.
- Automatic Mihomo binary updates.
- Headless-browser automation for scoring websites.
- Simultaneous evaluation while Surfing TUN mode is active.
