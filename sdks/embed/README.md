# @fyredocs/embed

Drop a Fyredocs editor into any web page with one HTML tag.

```html
<script type="module" src="https://cdn.fyredocs.com/embed.js"></script>

<fyredocs-editor
  doc-id="doc_01HV..."
  token="fyr_live_..."
  theme="dark"
  style="height: 700px"
></fyredocs-editor>
```

Under the hood: a Custom Element that wraps the hosted editor in a sandboxed iframe and bridges its events to standard `CustomEvent`s on the host element. No framework lock-in, no CSS leaks, no auth-cookie scoping surprises — works in plain HTML, React, Vue, Svelte, or any framework that lets you render a tag.

## Install

### CDN

```html
<script type="module" src="https://cdn.fyredocs.com/embed.js"></script>
```

### npm

```bash
npm install @fyredocs/embed
```

```ts
import '@fyredocs/embed';
// <fyredocs-editor> is now a registered custom element
```

The package self-registers on import; subsequent imports are no-ops.

## Attributes

| Attribute | Required | Description |
|---|---|---|
| `doc-id` | yes (for document mode) | Fyredocs document ID. |
| `token` | yes (for authenticated documents) | `fyr_…` API key or a JWT minted via your own server. **Never embed a long-lived live key in a public page** — use a short-lived JWT signed by your backend. |
| `base-url` | no — defaults to `https://fyredocs.com` | Self-hosted / staging origin. |
| `theme` | no | `light`, `dark`, or `auto`. Defaults to the editor's user preference. |

Attribute changes after mount rebuild the iframe URL automatically — pointing at a new document is `element.setAttribute('doc-id', 'doc_xyz')`.

## Events

The element dispatches `CustomEvent`s named `fyredocs:<kind>`. Bubble so listeners can be bound at any ancestor:

| Event | When | `detail` shape |
|---|---|---|
| `fyredocs:ready` | Editor mounted, document loaded. | `null` |
| `fyredocs:edit` | User made an edit; a new revision landed. | `{ revId: string }` |
| `fyredocs:save` | A burst of edits settled — fires once ~1.5s after the last edit, with the latest revision. Use this (rather than `edit`) when you want a debounced "Saved" UI signal without one event per click. | `{ revId: string }` |
| `fyredocs:error` | Something failed. | `{ message: string }` |

```js
document.querySelector('fyredocs-editor').addEventListener('fyredocs:edit', (e) => {
  console.log('new revision:', e.detail.revId);
});
```

Only messages from this element's own iframe — with a recognised `type` — bubble out. Stray `postMessage` calls from advertising scripts or other widgets on the same page are silently dropped.

## Security model

- **iframe sandbox**: the editor runs in its own browsing context. CSS, JS, and storage are isolated from the host page.
- **`allow` allow-list**: only `clipboard-read; clipboard-write; downloads` are granted. Camera/microphone/geolocation are NOT.
- **Two-layer postMessage trust**:
  - *Receive side* — incoming events whose `source` isn't this element's `iframe.contentWindow` are dropped, so multiple `<fyredocs-editor>` instances on the same page don't cross-talk.
  - *Sender side* — the editor addresses messages with a tight `targetOrigin` derived from `document.referrer` (the partner page that loaded the iframe), never with `'*'` when the partner's origin is known. The combination prevents a malicious sibling-frame from sniffing messages even when the iframe runs in a permissive sandbox. Partners that ship `Referrer-Policy: no-referrer` strip this signal and the editor falls back to `'*'` — set at least `Referrer-Policy: origin` if you want the tight binding.
- **Token rotation**: when the partner backend rotates a JWT, set the new value via `element.setAttribute('token', '...')`; the iframe re-loads with the new credential.

## Framework usage

### React (Next, CRA, Vite)

```tsx
import '@fyredocs/embed';
import { useEffect, useRef } from 'react';

declare global {
  namespace JSX {
    interface IntrinsicElements {
      'fyredocs-editor': React.DetailedHTMLProps<
        React.HTMLAttributes<HTMLElement> & {
          'doc-id'?: string;
          token?: string;
          theme?: 'light' | 'dark' | 'auto';
        },
        HTMLElement
      >;
    }
  }
}

export function Editor({ docId, token }: { docId: string; token: string }) {
  const ref = useRef<HTMLElement>(null);
  useEffect(() => {
    const node = ref.current;
    const onEdit = (e: Event) => console.log('edit', (e as CustomEvent).detail);
    node?.addEventListener('fyredocs:edit', onEdit);
    return () => node?.removeEventListener('fyredocs:edit', onEdit);
  }, []);
  return <fyredocs-editor ref={ref} doc-id={docId} token={token} style={{ height: 700 }} />;
}
```

### Vue 3

```vue
<script setup lang="ts">
import '@fyredocs/embed';
</script>

<template>
  <fyredocs-editor
    :doc-id="docId"
    :token="token"
    style="height: 700px"
    @fyredocs:edit="onEdit"
  />
</template>
```

(Vue auto-handles custom-element events via the `@event-name` directive.)

## Status

End-to-end wired:

- This package (`@fyredocs/embed`) — Custom Element + iframe + postMessage receiver.
- Frontend embed mode (`?embed=1`) — Layout skips global chrome (header/footer/skip-link); EditorPage broadcasts:
  - `ready` on first successful load,
  - `edit` on every subsequent `currentRevId` change,
  - `save` once ~1.5s after the last edit (debounced via [`useDebouncedEmbedSave`](../../../fyredocs_frontend/src/hooks/useEmbedMode.ts) — a burst of edits coalesces into one `save` carrying the latest `revId`),
  - `error` on load failures
  via `window.parent.postMessage`.

Embed contract is fully wired — both sides of the postMessage trust boundary are in place (receive-side source filter, sender-side targetOrigin from `document.referrer`). Future work tracked separately in the main roadmap.

## License

Apache-2.0
