---
status: accepted
---

# Bundle an isolated Mihomo core

NodeHarbor bundles and supervises its own pinned Mihomo binary instead of depending on or controlling an existing Clash/Mihomo installation. The supported product boundary is Windows 10/11 x64 only; Android and KernelSU delivery are no longer part of the system. The isolated core keeps evaluation behavior deterministic and prevents user proxy configuration from becoming application state, at the cost of an additional process and strict requirements for process, port, data-directory, and routing isolation.

## Considered Options

- Reuse a user's existing Mihomo controller: rejected because availability, version, configuration, selected node, and lifecycle are outside NodeHarbor's control.
- Temporarily replace another proxy application's configuration: rejected because testing could interrupt device networking and corrupt or overwrite user-managed state.
- Bundle an isolated Windows core: accepted because it gives NodeHarbor a deterministic Test Channel while preserving a hard ownership boundary around other proxy software.
- Retain an Android or KernelSU build path: rejected because the product is intentionally scoped to the Windows Desktop Runtime and the second platform would continue to expand the ownership, packaging, and routing surface.

## Consequences

The Windows release includes a pinned Mihomo binary, checksums, and license notices. NodeHarbor must address coexistence explicitly and must fail closed whenever it cannot prove that a test request used the intended proxy node. The former Android/KernelSU packaging, lifecycle, and Surfing-isolation paths are deliberately removed rather than retained as dormant compatibility code.
