---
status: accepted
---

# Bundle an isolated Mihomo core

NodeHarbor will bundle and supervise its own pinned Mihomo binary instead of depending on or controlling an existing Clash/Mihomo installation such as Surfing. This keeps evaluation behavior consistent across Windows and KernelSU and prevents user proxy configuration from becoming application state, at the cost of an additional process and strict requirements for process, port, data-directory, and routing isolation.

## Considered Options

- Reuse a user's existing Mihomo controller: rejected because availability, version, configuration, selected node, and lifecycle are outside NodeHarbor's control.
- Temporarily replace Surfing's configuration: rejected because testing could interrupt device networking and corrupt or overwrite user-managed state.
- Bundle an isolated core: accepted because it gives NodeHarbor a deterministic test channel while preserving a hard ownership boundary around Surfing and other proxy software.

## Consequences

Releases must include platform-specific Mihomo binaries, checksums, and license notices. NodeHarbor must address coexistence explicitly and must fail closed whenever it cannot prove that a test request used the intended proxy node.
