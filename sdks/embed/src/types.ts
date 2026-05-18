/**
 * Public types for `<fyredocs-editor>`.
 *
 * Stable surface — partner code references these via the
 * package's `dist/index.d.ts`. Renaming any of the literal
 * strings (`'ready'`, `'edit'`, …) is a major-version break.
 */

/** Attributes the element responds to. */
export type EmbedAttribute = 'doc-id' | 'token' | 'base-url' | 'theme';

/** Event names dispatched on the host element (prefixed `fyredocs:`). */
export type EmbedEvent = 'ready' | 'edit' | 'save' | 'error';

/** Per-event detail shape; `null` for events with no payload. */
export type EmbedEventDetail =
  | { revId: string } // edit, save
  | { message: string } // error
  | null; // ready

/** Wire-format the iframe posts. The element validates incoming
 *  messages match this shape before re-dispatching them as
 *  CustomEvents on the host. */
export interface IncomingMessage {
  type: EmbedEvent;
  payload?: unknown;
}
