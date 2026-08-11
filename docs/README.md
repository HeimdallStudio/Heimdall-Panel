<h1 align="center">Heimdall Documentation</h1>

<p align="center">
  Documentation and browser-side tools for
  <a href="https://github.com/HeimdallStudio/Heimdall-Panel"><b>Heimdall</b></a>,
  an advanced Xray-core management panel based on 3x-ui.
</p>

<p align="center">
  <a href="https://github.com/HeimdallStudio/Heimdall-Panel/actions/workflows/docs-ci.yml"><img src="https://github.com/HeimdallStudio/Heimdall-Panel/actions/workflows/docs-ci.yml/badge.svg" alt="Docs CI" /></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-GPL--3.0-blue?style=flat-square" alt="License: GPL-3.0" /></a>
  <img src="https://img.shields.io/badge/Next.js-16-black?style=flat-square&logo=next.js" alt="Next.js 16" />
  <img src="https://img.shields.io/badge/Fumadocs-16-0ea5e9?style=flat-square" alt="Fumadocs 16" />
</p>

## Overview

The `docs/` directory contains Heimdall's documentation source, reference material,
and browser-side configuration tools. It is maintained alongside the panel source
so documentation can evolve with the project.

## What's inside

The documentation walks you through Heimdall from first install to day-to-day operation:

- **Getting Started** — installation, first login, and updating or uninstalling the panel.
- **Configuration** — the panel, inbounds, REALITY, transports, clients, subscriptions, and share links.
- **Operations** — reverse proxy, multi-node setups, outbounds & routing, backup/restore, the Telegram bot, and security.
- **Reference** — environment variables, the database, ports & firewall, and the HTTP API.
- **Help** — troubleshooting, FAQ, migration, and how to contribute.

## Interactive tools

The site ships with in-browser helpers that generate configuration for you — **no data
ever leaves your browser**:

| Tool                         | What it does                                            |
| ---------------------------- | ------------------------------------------------------- |
| **REALITY Config Generator** | Build a valid REALITY inbound configuration.            |
| **Share Link Inspector**     | Decode and inspect `vless://` / `vmess://` share links. |
| **Install Command Builder**  | Assemble the right install command for your setup.      |
| **Reverse Proxy Generator**  | Generate reverse-proxy configs (Nginx / Caddy).         |
| **Protocol Wizard**          | Pick and configure the right protocol for your needs.   |
| **Firewall Rules Generator** | Produce firewall rules for your ports.                  |

## Tech stack

| Layer      | Technology                                                  |
| ---------- | ---------------------------------------------------------- |
| Framework  | [Next.js 16](https://nextjs.org) (App Router) · React 19   |
| Docs       | [Fumadocs](https://fumadocs.dev) (`-ui` / `-core` / `-mdx`) |
| Styling    | [Tailwind CSS v4](https://tailwindcss.com)                 |
| Search     | [Orama](https://orama.com) static index                    |
| Language   | TypeScript (strict)                                         |
| Tests      | [Vitest](https://vitest.dev) for the pure `lib/xray` logic  |
| Tooling    | pnpm · ESLint 9 · Prettier                                  |

## Quick start

This project uses **[pnpm](https://pnpm.io)** (npm lockfiles are gitignored). Run everything
from the `docs/` directory:

```bash
cd docs
pnpm install
pnpm dev        # http://localhost:3000
```

Useful scripts:

| Script           | Description                                  |
| ---------------- | -------------------------------------------- |
| `pnpm dev`       | Start the dev server                         |
| `pnpm build`     | Production build (also typechecks)           |
| `pnpm typecheck` | Generate MDX/route types and `tsc --noEmit`  |
| `pnpm lint`      | Run ESLint                                    |
| `pnpm test`      | Run unit tests (Vitest)                       |

See [`CONTRIBUTING.md`](./CONTRIBUTING.md) for the full list and project conventions.

## Project structure

```
app/             # Next.js App Router — layouts, home, docs, OG images, search, llms.txt
components/      # React components — interactive tools, home sections, MDX bindings
content/docs/    # MDX documentation, one folder per locale (en · fa · ru · zh)
lib/             # source config, i18n, GitHub stats, and the unit-tested lib/xray logic
public/          # static assets — logos, favicon, openapi.json, CNAME
scripts/         # build-time scripts (API reference generation)
source.config.ts # Fumadocs MDX schema & collection config
next.config.mjs  # Next.js config (static-export gating)
proxy.ts         # i18n middleware
```

## Internationalization

Documentation is authored in **English**. Persian (`fa`, RTL), Russian (`ru`), and
Chinese (`zh`) locales are wired up; untranslated pages fall back to English so they
never 404. English URLs are unprefixed; other locales live under `/fa`, `/ru`, `/zh`.

## Deployment

The site builds for two targets:

- **Vercel / Node** — `pnpm build` (static search index + prerendered OG images).
- **GitHub Pages (static export)** — `DEPLOY_TARGET=static pnpm build` → `out/`.

## Contributing

Contributions are welcome! Setup, scripts, and project conventions live in
[`CONTRIBUTING.md`](./CONTRIBUTING.md).

## License

Licensed under [GPL-3.0](./LICENSE).
