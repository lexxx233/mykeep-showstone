<div align="center">

# 🔮 mykeep · Showstone

### A contained, portable web browser your AI agent can see — and act — through.

**Status: vision / design.** No code yet — this repo holds the design.

[mykeep.ai](https://mykeep.ai) · **Personal · Private · Portable**

</div>

---

Showstone is the **"sees the web"** component of [mykeep](https://mykeep.ai) — a portable suite of
local capabilities any AI agent can plug into, all on a USB stick. A *showstone* is a scrying
glass — a stone you gaze into to see distant, hidden things. This one lets your agent see, and act
on, the live web — in a sandbox you carry, leaving no trace on the host.

Where the **Memory Capsule** makes an agent *know* you, **SecretVault** lets it *act as* you, and
**Foundry** lets it *do more*, **Showstone** lets it *see the web*: navigate, read, click, fill,
screenshot, and extract.

## What it is

A real browser — **Chromium, bundled on the stick** — driven by a pure-Go control layer over the
Chrome DevTools Protocol (CDP, via [chromedp](https://github.com/chromedp/chromedp)). The mykeep
binary stays pure-Go/no-CGo; the engine is a bundled Chromium process it launches and steers.

- **Contained** — an ephemeral browser profile (cookies, storage, history, logged-in sessions)
  **sealed on the stick** with the mykeep AES-256-GCM seal. Nothing is written to the host machine;
  pull the stick and no trace remains.
- **Portable** — your logged-in web travels with you. Plug into any machine, browse, unplug. The
  engine runs from the stick — no host install.
- **Agent-drivable** — the agent navigates, clicks, fills, screenshots, and extracts clean
  text/markdown, all by reference over the local API. Every action is auditable.

## The flywheel — stronger inside mykeep

Showstone does things a standalone browser can't, because the rest of the suite is right there:

- **SecretVault logs it in *by reference*.** The agent fills a login form using a credential it
  never sees — so it browses *as you* without your password ever entering its context.
- **Memory Capsule remembers** what it found, across sessions.
- **Foundry tools** can request a browsing capability and compose web actions.

> Your agent can see and act on the web, logged in as you, in a sandbox you carry — and it never
> holds your password.

## How an agent uses it

The same shape as the rest of mykeep: a **loopback REST API + a pasted guide** — the zero-install
floor that works with any agent that can make an HTTP call.

```
POST /v1/browse/navigate    { "url": "https://…" }       → page state + readable text/markdown
POST /v1/browse/act         { "click": "…", "fill": {…} } → interact (click, type, submit)
GET  /v1/browse/extract     ?selector=…                   → structured content
POST /v1/browse/screenshot                                → an image of the page
```

## Honest caveats

- **Size.** Bundling Chromium is heavy (~150 MB per OS). This is the one component where mykeep's
  "tiny pure-Go binary" ideal genuinely bends — *pure Go* applies to the **control** plane; the
  engine is native. Ship only the platform(s) you use to keep the stick footprint sane.
- **Web content is untrusted, and the agent reads it.** A page can try to prompt-inject the agent
  driving it. As with SecretVault, the honest promise is **bounded + observable**: a contained
  profile, an audit log of every action, and a human-approval gate for sensitive acts (purchases,
  posts, irreversible clicks). Treat page content as hostile input.
- **Acting on the web *as you* is powerful.** Containment + audit + approval are the guardrails —
  not "trust the agent."

## Design principles

- **Portable & contained** — the engine and your sealed profile live on the stick; no host trace.
- **By reference** — logins come from SecretVault; the agent never holds the secret.
- **Bounded + observable** — contained profile, audited actions, approval for the dangerous ones.
- **The agent reasons, mykeep provides** — Showstone drives the browser and records; it does no
  reasoning of its own.

## Where it fits

- **[Memory Capsule](https://github.com/lexxx233/mykeep-memory-capsule)** — *knows* you.
- **[SecretVault](https://github.com/lexxx233/mykeep-secretvault)** — *acts as* you.
- **[Foundry](https://github.com/lexxx233/mykeep-foundry)** — *does* more.
- **Showstone** — *sees* the web.

---

<div align="center">
<sub>A component of <a href="https://mykeep.ai">mykeep</a> · Personal · Private · Portable · © 2026 Domu Inc</sub>
</div>
