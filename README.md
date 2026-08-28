# NodeHarbor

NodeHarbor is a self-hosted tool for aggregating Clash/Mihomo subscriptions, checking node availability, evaluating exit-IP quality, and publishing a curated subscription for Windows and KernelSU.

The first runnable slice provides a persistent Go/SQLite backend, an embedded React WebUI, system health endpoints, and an initial valid Published Subscription.

## Run on Windows

Requirements: Go 1.24 or newer and Node.js 24 or newer.

```powershell
.\scripts\dev.ps1
```

Then open <http://127.0.0.1:9876>. The initial subscription is available at <http://127.0.0.1:9876/sub/clash.yaml>, including when there are no Proxy Nodes. Runtime state is stored under `data/` by default.

To use a different address or state directory:

```powershell
.\scripts\dev.ps1 -Listen 127.0.0.1:9988 -Data C:\NodeHarborData
```

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

The integration suite starts the real HTTP backend with a temporary SQLite database. Its `Upstream`, `ScoringProvider`, `Kernel`, and `TestChannel` adapters can be replaced from the application assembly seam. CI additionally downloads the pinned Mihomo v1.19.30 Windows build, verifies its SHA-256 digest, and uses it to validate the generated empty subscription.

## Design documents

- [Design specification](docs/DESIGN-SPEC.md)
- [Domain language](CONTEXT.md)
- [Architecture decisions](docs/adr/)

## Planned platforms

- Windows 10/11 x64
- KernelSU on Android arm64-v8

## License

[MIT](LICENSE)
