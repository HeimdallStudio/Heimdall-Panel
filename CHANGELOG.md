# Changelog

Notable Heimdall changes are documented here.

## Unreleased

### Changed

- Removed the vendored Windows OpenSSL installer from source and release archives; Windows users obtain third-party OpenSSL separately when needed.
- Reorganized packaging and localized documentation.
- Established Heimdall-owned documentation, CI, release, and container links.

## 1.5.1

### Changed

- Negotiated runtime-profile capabilities with remote nodes so mixed-version deployments select compatible synchronization behavior.
- Applied public plaintext VLESS outbounds through a full restart of the audited custom Xray core instead of the panel's embedded hot-apply validator.
- Updated the audited linux-amd64 release recipe to reproduce the live CGO-enabled, unstripped panel binary and package the exact custom Xray runtime.

### Fixed

- Preserved multi-profile state correctly for tunnel and WireGuard inbounds while preventing unsupported profile payloads from leaking into their wire configuration.
- Made node inbound import opt-in and durable across legacy updates, preventing selection policy from being overwritten by omitted fields.
- Prevented reconciliation from deleting newly selected remote inbounds before their first safe import and adoption pass.
- Detached local mirrors that fall outside a reduced node selection without deleting the corresponding remote inbounds.
- Removed stale client, traffic, host, fallback, and ownership dependencies when local mirrors are detached, and invalidated all affected frontend caches.
- Retained plaintext VLESS entries received through outbound subscriptions while continuing to reject malformed outbounds and public plaintext Trojan outbounds.

## 1.5.0

### Added

- Runtime-backed multi-profile inbounds with automatic per-profile listeners and subscription generation for TCP/RAW, mKCP, WebSocket, HTTPUpgrade, gRPC, and XHTTP transports.
- Independent per-profile transport, TLS/REALITY, header, Sockopt, and runtime-binding controls.
- Runtime listener collision validation before inbound persistence, including safe use of the same numeric port across TCP and UDP.
- Start-after-first-use client expiration synchronization.

### Changed

- Reworked the subscription-profile editor, transport layouts, field ownership, and security controls across supported transports.
- Canonicalized the linux-amd64 release process to reproduce the validated live panel and package the audited Heimdall custom Xray runtime.
- Refined CLI menu borders, spacing, usage rows, and terminal alignment.

### Fixed

- Corrected profile header-map editing, TCP HTTP obfuscation, TLS and REALITY SNI synchronization, mKCP/finalmask ownership, and runtime shared-port routing.
- Prevented duplicate or conflicting runtime listeners from being persisted.
- Improved validation and synchronization between inbound profiles, generated runtime listeners, and subscription output.

