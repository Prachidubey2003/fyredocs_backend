/**
 * <fyredocs-editor> — embeddable Fyredocs editor Web Component.
 *
 * Wraps the hosted editor in an iframe and turns iframe →
 * postMessage events into CustomEvents on the host element so
 * partner code can listen with the normal `.addEventListener`
 * pattern. Auth + the target document are passed via element
 * attributes; the iframe URL is rebuilt whenever an attribute
 * changes.
 *
 * Why iframe instead of inline mount: it isolates the editor's
 * JS, CSS, and storage from the host page. A partner can drop
 * the element into a React, Vue, or static site without
 * worrying about CSS bleed or auth-cookie scoping.
 *
 * See README.md for the partner-facing integration guide.
 */

import type {
  EmbedAttribute,
  EmbedEvent,
  EmbedEventDetail,
  IncomingMessage,
} from './types.js';

const ATTRIBUTES: readonly EmbedAttribute[] = Object.freeze([
  'doc-id',
  'token',
  'base-url',
  'theme',
]);

const DEFAULT_BASE_URL = 'https://fyredocs.com';

/**
 * FyredocsEditor is the custom-element class. Most usage doesn't
 * touch it directly — `customElements.define` (called by
 * `index.ts`) makes `<fyredocs-editor>` valid HTML, and the
 * element auto-wires its iframe on `connectedCallback`.
 *
 * Lifecycle:
 *   1. `connectedCallback` — attaches a shadow root + iframe.
 *   2. `attributeChangedCallback` — rebuilds the iframe `src`
 *      when `doc-id`, `token`, `base-url`, or `theme` change.
 *   3. `disconnectedCallback` — detaches the postMessage
 *      listener so disposing the element doesn't leak listeners.
 *
 * Events dispatched on the host (bubble: true):
 *   - `fyredocs:ready` — editor mounted, document loaded.
 *   - `fyredocs:edit` — user made an edit (detail = `{revId}`).
 *   - `fyredocs:save` — auto-save settled.
 *   - `fyredocs:error` — something failed (detail = `{message}`).
 */
export class FyredocsEditor extends HTMLElement {
  static get observedAttributes(): readonly string[] {
    return ATTRIBUTES;
  }

  private iframe: HTMLIFrameElement | null = null;
  private boundOnMessage: ((event: MessageEvent) => void) | null = null;

  connectedCallback(): void {
    if (this.shadowRoot) {
      // Already connected — moving the element in the DOM
      // re-fires connectedCallback. Don't double-mount.
      return;
    }
    const shadow = this.attachShadow({ mode: 'open' });

    // Inline a minimal stylesheet so the iframe fills the host
    // by default. Consumers can override via `style="height:
    // 600px"` on the host element.
    const style = document.createElement('style');
    style.textContent = `
      :host {
        display: block;
        width: 100%;
        height: 600px;
      }
      iframe {
        width: 100%;
        height: 100%;
        border: 0;
        display: block;
      }
    `;
    shadow.appendChild(style);

    const iframe = document.createElement('iframe');
    iframe.title = 'Fyredocs editor';
    // Clipboard for copy/paste, downloads for export. We
    // deliberately don't grant camera/microphone — the editor
    // doesn't need them and minimising the allow-list shrinks
    // the partner page's attack surface.
    iframe.setAttribute(
      'allow',
      'clipboard-read; clipboard-write; downloads',
    );
    iframe.src = this.buildSrc();
    this.iframe = iframe;
    shadow.appendChild(iframe);

    this.boundOnMessage = (event) => this.onMessage(event);
    window.addEventListener('message', this.boundOnMessage);
  }

  disconnectedCallback(): void {
    if (this.boundOnMessage) {
      window.removeEventListener('message', this.boundOnMessage);
      this.boundOnMessage = null;
    }
    this.iframe = null;
  }

  attributeChangedCallback(
    name: string,
    oldValue: string | null,
    newValue: string | null,
  ): void {
    if (!this.iframe || oldValue === newValue) {
      return;
    }
    if ((ATTRIBUTES as readonly string[]).includes(name)) {
      this.iframe.src = this.buildSrc();
    }
  }

  /**
   * onMessage is the postMessage receiver. We accept messages
   * only from this element's own iframe — postMessage is
   * broadcast on the entire window, so without this check the
   * element would also fire events from OTHER iframes on the
   * same page (e.g., two `<fyredocs-editor>` instances in
   * different tabs of the same SPA).
   */
  private onMessage(event: MessageEvent): void {
    if (!this.iframe || event.source !== this.iframe.contentWindow) return;
    const data = parseIncoming(event.data);
    if (!data) return;
    // Forward as a CustomEvent on the host. Bubbles so partner
    // code can listen at any ancestor (a common pattern for
    // React, Vue, etc. that bind handlers higher in the tree).
    // The payload's runtime shape is the trust-boundary
    // contract of the iframe; downstream listeners get the
    // typed EmbedEventDetail union. The `unknown` payload is
    // cast through `as` because TS can't statically map
    // `data.type` → the right detail shape without a discriminated
    // union — partner listeners narrow the detail themselves
    // (`if (e.type === 'fyredocs:edit') ...`).
    this.dispatchEvent(
      new CustomEvent<EmbedEventDetail>(`fyredocs:${data.type}`, {
        detail: (data.payload ?? null) as EmbedEventDetail,
        bubbles: true,
        composed: true,
      }),
    );
  }

  /**
   * buildSrc assembles the iframe URL from the host attributes.
   * Always passes `embed=1` so the hosted editor knows to
   * suppress the global header/footer and broadcast its events
   * via postMessage rather than navigating directly.
   *
   * The embed-mode flag in the editor frontend is a tracked
   * follow-up — for v0 the parameter is plumbed end-to-end so a
   * subsequent change in the frontend can hide chrome without
   * an embed-side release.
   */
  private buildSrc(): string {
    const base = (this.getAttribute('base-url') ?? DEFAULT_BASE_URL).replace(
      /\/+$/,
      '',
    );
    const params = new URLSearchParams();
    params.set('embed', '1');
    const docId = this.getAttribute('doc-id');
    if (docId) params.set('doc', docId);
    const token = this.getAttribute('token');
    if (token) params.set('token', token);
    const theme = this.getAttribute('theme');
    if (theme) params.set('theme', theme);
    return `${base}/editor?${params.toString()}`;
  }
}

/**
 * parseIncoming validates the iframe's postMessage payload.
 * We require a typed envelope `{type, payload?}` where `type`
 * is one of the EmbedEvent strings — random `message` events
 * from advertising scripts or other widgets on the partner
 * page must not bubble out of our element.
 */
function parseIncoming(data: unknown): IncomingMessage | null {
  if (!data || typeof data !== 'object') return null;
  const maybe = data as Record<string, unknown>;
  if (typeof maybe.type !== 'string') return null;
  if (!isKnownEvent(maybe.type)) return null;
  return { type: maybe.type, payload: maybe.payload };
}

function isKnownEvent(t: string): t is EmbedEvent {
  return (
    t === 'ready' || t === 'edit' || t === 'save' || t === 'error'
  );
}
