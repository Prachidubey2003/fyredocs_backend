/**
 * `@fyredocs/embed` — drop-in `<fyredocs-editor>` Web Component.
 *
 * Importing this module registers the custom element on the
 * global `customElements` registry, after which
 * `<fyredocs-editor token="..." doc-id="...">` is valid HTML
 * anywhere on the page.
 *
 * Partner integration:
 *
 * ```html
 * <script type="module" src="https://cdn.fyredocs.com/embed.js"></script>
 *
 * <fyredocs-editor
 *   doc-id="doc_01HV..."
 *   token="fyr_live_..."
 *   theme="dark"
 *   style="height: 700px"
 * ></fyredocs-editor>
 *
 * <script>
 *   document.querySelector('fyredocs-editor').addEventListener(
 *     'fyredocs:edit',
 *     (e) => console.log('user edited:', e.detail.revId),
 *   );
 * </script>
 * ```
 *
 * Direct ESM import:
 *
 * ```ts
 * import '@fyredocs/embed';
 * // <fyredocs-editor> is now a registered custom element
 * ```
 */

import { FyredocsEditor } from './element.js';

// Self-register on import. Guarded so a double-import doesn't
// throw — the spec disallows re-defining an element name.
if (typeof customElements !== 'undefined' && !customElements.get('fyredocs-editor')) {
  customElements.define('fyredocs-editor', FyredocsEditor);
}

export { FyredocsEditor };
export type {
  EmbedAttribute,
  EmbedEvent,
  EmbedEventDetail,
  IncomingMessage,
} from './types.js';
