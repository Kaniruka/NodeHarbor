# NodeHarbor

当前发布版本：`0.3.0`

NodeHarbor is a self-hosted Windows desktop tool for aggregating Clash/Mihomo subscriptions, checking node availability, evaluating exit-IP quality, and publishing a curated subscription.

The first runnable slice provides a persistent Go/SQLite backend, an embedded React WebUI, system health endpoints, and an initial valid Published Subscription.

The WebUI can manage up to ten Upstream Subscriptions imported from a URL, uploaded YAML file, or pasted YAML. URL Upstream Subscriptions preserve the complete URL and optional User-Agent. Successful raw documents are retained verbatim; when an Upstream Subscription becomes stale, the previous successful content is kept and the error appears in the Upstream Subscription list.

## 使用方法

1. 从 Windows 发布包中解压文件，运行 `nodeharbor.exe`。程序会启动本地管理页面并尝试自动打开浏览器；如果浏览器没有打开，请访问 <http://127.0.0.1:9876>。
2. 打开“订阅”，添加订阅源。可以粘贴订阅 URL、上传 YAML 文件，或直接粘贴 YAML 内容。URL 订阅可选填写 User-Agent。
3. 打开“节点”，点击“开始评估”。程序会依次检查节点可用性、测速、获取出口 IP，并使用 IPSuper 评分。正在处理的节点会显示状态标签。
4. 在“设置”中调整合格阈值、探测次数、超时、并发数和评分缓存期限。只有通过可用性检查且 IP Score 达到合格阈值的节点才会进入发布结果。
5. 评估完成后，在“发布”或“概览”复制稳定订阅链接，并将它添加到 Clash/Mihomo 客户端：

   ```text
   http://127.0.0.1:9876/sub/clash.yaml
   ```

6. 使用“仅显示达标节点”可以快速查看当前可发布节点；“日志”页面用于查看订阅刷新、评估、评分和发布诊断信息。

程序退出后重新运行即可继续使用原有数据。订阅源、评分结果、发布快照和设置默认保存在程序目录下的 `data/`，迁移或备份时请一并保存该目录。

## Run on Windows

Requirements: Go 1.24 or newer and Node.js 24 or newer.

```powershell
.\scripts\dev.ps1
```

The script downloads the pinned Mihomo v1.19.30 Windows build on first run, verifies both archive and executable SHA-256 digests on every startup, builds NodeHarbor beside its owned core, and opens <http://127.0.0.1:9876> in the default browser. The Windows package also contains the pinned Chromium browser runtime used by IPSuper scoring. The initial subscription is available at <http://127.0.0.1:9876/sub/clash.yaml>, including when there are no Proxy Nodes. Runtime state is stored under `data/` by default.

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

The package contains `browser-runtime\driver`, `browser-runtime\browsers`, the pinned Mihomo core, and the NodeHarbor executable. If the browser runtime is missing or cannot start, scoring fails closed with `runtime_unavailable` and the previous Publication Snapshot is retained. `--browser-headed=true` and `--browser-path` are diagnostic overrides; normal IPSuper scoring is headless and uses an ephemeral browser context per Proxy Node. Run `.\nodeharbor.exe --version` to print the packaged version.

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
