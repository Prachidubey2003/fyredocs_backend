# @fyredocs/zapier

Official Fyredocs integration for the [Zapier Developer Platform](https://platform.zapier.com/). REST-hook trigger + conversion / signing actions, authenticated via long-lived API keys.

This directory IS the Zapier app: `index.js` exports the app definition that `zapier-platform-core` consumes. The Zapier CLI (`zapier validate` / `zapier push`) reads from here.

## What it ships

| Resource | Key | Purpose |
|---|---|---|
| Trigger | `newEvent` | REST hook — subscribes to one of Fyredocs's domain events (`document.converted`, `document.signed`, `subscription.*`) and fires whenever Fyredocs POSTs the matching event. |
| Action | `convertToPdf` | Submits a `convert-to-pdf` job for a file. Returns the job id; pair with the `newEvent` trigger watching for `document.converted` to chain follow-up steps. |
| Action | `signPdf` | Applies a visible-stamp signature to an existing document. Returns the job id. |

## Auth

Custom API-key. Users paste a `fyr_live_…` (or `fyr_test_…`) token minted at [fyredocs.com/account/api-keys](https://fyredocs.com/account/api-keys). The token is masked in the Zapier dashboard (`type: password`), threaded onto every request as `Authorization: Bearer …` via the `beforeRequest` middleware (`authentication.js`), and validated by a `/auth/me` GET on the test-auth flow.

`connectionLabel` pulls the user's name + email from the test response so the dashboard reads `"Alice (alice@acme.example)"` instead of an opaque token prefix.

## Local development

```bash
npm install
npm test            # structural smoke tests via node --test
npm run validate    # zapier validate — deeper schema check
npm run test:zapier # zapier test — exercises the actual auth + perform flows against fyredocs.com
npm run push        # zapier push — deploys this version
```

`npm test` runs without the Zapier CLI installed (uses only Node's built-in test runner + the local app definition). It catches:

- `index.js` exports drift (renamed trigger/create keys, missing display labels).
- Authentication shape regressions (test URL, required fields, `connectionLabel` fallback chain).
- Bearer-token middleware behaviour (injects when present, no-op when absent).
- Trigger choice-list drift — pins the exact five event types `notify-service` accepts so a Zap that subscribes to a renamed event would fail at `npm test` rather than at the user's runtime.
- `perform` defensiveness for malformed inbound bodies (returns `[]` instead of throwing).

## How REST hooks work here

1. User adds a Zap with the **New Fyredocs event** trigger and selects an event type (e.g., `document.converted`).
2. Zapier hits our `performSubscribe`, which `POST /api/notify/v1/notify/webhooks` with `{eventType, targetUrl}`. `targetUrl` is the per-Zap callback URL Zapier supplies.
3. Fyredocs's [`notify-service`](../../notify-service/) persists the subscription, generates an HMAC signing secret, and starts delivering matching events to Zapier's `targetUrl`.
4. On each delivery the trigger's `perform` runs against `bundle.cleanedRequest`, maps the DomainEvent envelope to a flat output Zapier can wire into downstream steps, and returns it.
5. When the user removes the Zap, Zapier calls `performUnsubscribe`, which `DELETE /api/notify/v1/notify/webhooks/{id}`.

The HMAC signature on the inbound webhook isn't verified in this Zapier app — Zapier itself doesn't expose a per-step verification hook, and the body arrives over Zapier's authenticated channel anyway. Users who bridge Zapier to a custom receiver should verify the signature on their side using the secret they captured at subscription time.

## Why not polling triggers

Fyredocs's fanout pipeline already delivers signed event POSTs at sub-second latency. A polling trigger would add 1–5 minutes of Zap-trigger lag plus N×poll-rate cost-per-account, with no upside. We ship only REST hooks here.

## Versioning

`App.version` in `index.js` is the Zapier-dashboard-surfaced version. Bump it on every push that changes resource keys / wire shapes (NOT on README-only changes — the Zapier "new version available" prompt is non-trivial UX friction for users).

`App.platformVersion` is locked to `zapier-platform-core@16.4.0`. Bumping requires re-running `zapier validate` against the new core release and reviewing breaking-change notes — coordinate via the @fyredocs/zapier maintainer.

## Deployment

```bash
# One-time:
zapier login         # browser flow against zapier.com
zapier register      # only on the very first push

# Per release:
npm run validate     # catch schema mistakes
npm run push         # uploads the current version
zapier promote 0.1.0 # promote a private version to public (when ready)
```
