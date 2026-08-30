# NodeHarbor

NodeHarbor is a self-hosted Windows desktop tool for aggregating Clash/Mihomo subscriptions, checking node availability, evaluating exit-IP quality, and publishing a curated subscription.

The first runnable slice provides a persistent Go/SQLite backend, an embedded React WebUI, system health endpoints, and an initial valid Published Subscription.

The WebUI can manage up to ten Upstream Subscriptions imported from a URL, uploaded YAML file, or pasted YAML. URL Upstream Subscriptions preserve the complete URL and optional User-Agent. Successful raw documents are retained verbatim; when an Upstream Subscription becomes stale, the previous successful content is kept and the error appears in the Upstream Subscription list.

## Run on Windows

Requirements: Go 1.24 or newer and Node.js 24 or newer.

```powershell
.\scripts\dev.ps1
```

The script downloads the pinned Mihomo v1.19.30 Windows build on first run, verifies both archive and executable SHA-256 digests on every startup, builds NodeHarbor beside its owned core, and opens <http://127.0.0.1:9876> in the default browser. The Windows package also contains the pinned Chromium browser runtime used by IPSuper and IPLark scoring. The initial subscription is available at <http://127.0.0.1:9876/sub/clash.yaml>, including when there are no Proxy Nodes. Runtime state is stored under `data/` by default.

The default listener is `127.0.0.1:9876`. To make only the Published Subscription and health reachable from a trusted LAN, bind the listener to a reachable address such as `0.0.0.0`; management routes still reject non-loopback clients:

```powershell
.\scripts\dev.ps1 -Listen 0.0.0.0:9876
```

To use a different address or state directory:

```powershell
.\scripts\dev.ps1 -Listen 127.0.0.1:9988 -Data C:\NodeHarborData
```

The listener address and port can also be saved in the WebUI. They are persisted in SQLite and take effect after the next restart. The WebUI shows the local subscription URL, the current access URL, and listener diagnostics. A bind conflict is reported at startup and does not replace the last Publication Snapshot.

## Build and verify the Windows package

```powershell
.\scripts\package-windows.ps1 -Version 0.3.0
Expand-Archive .\dist\NodeHarbor-windows-0.3.0.zip -DestinationPath .\dist\nodeharbor-package
.\scripts\smoke-windows.ps1 -PackageDirectory .\dist\nodeharbor-package
```

The package contains `browser-runtime\driver`, `browser-runtime\browsers`, the pinned Mihomo core, and the NodeHarbor executable. If the browser runtime is missing or cannot start, scoring fails closed with `runtime_unavailable` and the previous Publication Snapshot is retained. `--browser-headed=true` and `--browser-path` are diagnostic overrides; normal scoring is headless and uses an ephemeral browser context per Proxy Node and provider.

## Verify

```powershell
cd web
npm ci
npm test
npm run typecheck
npm run build
cd ..
go test ./...
go vet ./...
go build ./cmd/nodeharbor
```

The integration suite starts the real HTTP backend with a temporary SQLite database. Its test-only black-box evaluation endpoint proves that one request can traverse replaceable `Upstream`, `ScoringProvider`, `Kernel`, and `TestChannel` adapters. CI additionally downloads the pinned Mihomo v1.19.30 Windows build, verifies its SHA-256 digest, and uses the production Mihomo adapter to validate the generated empty subscription.

## Design documents

- [Design specification](docs/DESIGN-SPEC.md)
- [Domain language](CONTEXT.md)
- [Architecture decisions](docs/adr/)

## Supported platform

- Windows 10/11 x64 only

## License

[MIT](LICENSE)
