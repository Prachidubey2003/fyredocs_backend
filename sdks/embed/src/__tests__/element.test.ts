import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// Importing the index registers the custom element. happy-dom
// supports customElements + Shadow DOM out of the box.
import { FyredocsEditor } from '../index.js';

describe('<fyredocs-editor>', () => {
  let host: FyredocsEditor;

  beforeEach(() => {
    host = document.createElement('fyredocs-editor') as FyredocsEditor;
    document.body.appendChild(host);
  });

  afterEach(() => {
    host.remove();
  });

  it('registers the element on the global registry', () => {
    expect(customElements.get('fyredocs-editor')).toBe(FyredocsEditor);
  });

  it('mounts an iframe inside its shadow root', () => {
    const iframe = host.shadowRoot?.querySelector('iframe');
    expect(iframe).toBeTruthy();
    expect(iframe?.src).toContain('/editor?');
    expect(iframe?.src).toContain('embed=1');
  });

  it('encodes attributes into the iframe src', () => {
    host.setAttribute('doc-id', 'doc_01HV');
    host.setAttribute('token', 'fyr_live_abc');
    host.setAttribute('theme', 'dark');
    host.setAttribute('base-url', 'https://api.staging.example.com');

    const iframe = host.shadowRoot!.querySelector('iframe')!;
    expect(iframe.src).toContain('doc=doc_01HV');
    expect(iframe.src).toContain('token=fyr_live_abc');
    expect(iframe.src).toContain('theme=dark');
    expect(iframe.src.startsWith('https://api.staging.example.com/editor?')).toBe(true);
  });

  it('trims trailing slashes from base-url', () => {
    host.setAttribute('base-url', 'https://api.example.com//');
    const iframe = host.shadowRoot!.querySelector('iframe')!;
    expect(iframe.src.startsWith('https://api.example.com/editor?')).toBe(true);
  });

  it('forwards typed postMessage events as fyredocs:* CustomEvents', () => {
    const handler = vi.fn();
    host.addEventListener('fyredocs:edit', handler as EventListener);

    // happy-dom's MessageEvent doesn't let us set `source` directly,
    // so we simulate by dispatching a MessageEvent with the iframe's
    // contentWindow as the source. Workaround: define the event,
    // then assign source via Object.defineProperty.
    const iframe = host.shadowRoot!.querySelector('iframe')!;
    const event = new MessageEvent('message', {
      data: { type: 'edit', payload: { revId: 'rev_abc' } },
    });
    Object.defineProperty(event, 'source', {
      value: iframe.contentWindow,
    });
    window.dispatchEvent(event);

    expect(handler).toHaveBeenCalledTimes(1);
    const customEvent = handler.mock.calls[0][0] as CustomEvent;
    expect(customEvent.detail).toEqual({ revId: 'rev_abc' });
    expect(customEvent.bubbles).toBe(true);
  });

  it('ignores messages from other iframes / sources', () => {
    const handler = vi.fn();
    host.addEventListener('fyredocs:edit', handler as EventListener);

    // Source is NOT the element's iframe — must be dropped.
    const otherIframe = document.createElement('iframe');
    document.body.appendChild(otherIframe);

    const event = new MessageEvent('message', {
      data: { type: 'edit', payload: { revId: 'rev_imposter' } },
    });
    Object.defineProperty(event, 'source', {
      value: otherIframe.contentWindow,
    });
    window.dispatchEvent(event);

    expect(handler).not.toHaveBeenCalled();
    otherIframe.remove();
  });

  it('ignores postMessage payloads with unknown event types', () => {
    const handler = vi.fn();
    host.addEventListener('fyredocs:ad-tracker' as 'fyredocs:edit', handler as EventListener);

    // Random ad-tracker iframe or third-party widget posts something
    // that looks like our envelope but has a different type.
    const iframe = host.shadowRoot!.querySelector('iframe')!;
    const event = new MessageEvent('message', {
      data: { type: 'ad-tracker', payload: { something: true } },
    });
    Object.defineProperty(event, 'source', {
      value: iframe.contentWindow,
    });
    window.dispatchEvent(event);

    expect(handler).not.toHaveBeenCalled();
  });

  it('ignores malformed postMessage payloads', () => {
    const editHandler = vi.fn();
    host.addEventListener('fyredocs:edit', editHandler as EventListener);
    const iframe = host.shadowRoot!.querySelector('iframe')!;

    for (const bad of [null, undefined, 42, 'string', { noType: true }, { type: 42 }]) {
      const event = new MessageEvent('message', { data: bad });
      Object.defineProperty(event, 'source', {
        value: iframe.contentWindow,
      });
      window.dispatchEvent(event);
    }
    expect(editHandler).not.toHaveBeenCalled();
  });

  it('rewrites iframe src when an observed attribute changes', () => {
    const initial = host.shadowRoot!.querySelector('iframe')!.src;
    host.setAttribute('doc-id', 'doc_changed');
    const after = host.shadowRoot!.querySelector('iframe')!.src;
    expect(after).not.toBe(initial);
    expect(after).toContain('doc=doc_changed');
  });

  it('detaches the message listener on disconnect', () => {
    const handler = vi.fn();
    host.addEventListener('fyredocs:edit', handler as EventListener);
    const iframe = host.shadowRoot!.querySelector('iframe')!;
    const iframeWindow = iframe.contentWindow;

    host.remove();

    // Post a message that WOULD have fired before disconnect.
    // After remove(), the listener is gone and nothing should
    // bubble out.
    const event = new MessageEvent('message', {
      data: { type: 'edit', payload: { revId: 'rev_after_disconnect' } },
    });
    Object.defineProperty(event, 'source', { value: iframeWindow });
    window.dispatchEvent(event);

    expect(handler).not.toHaveBeenCalled();
  });

  it('emits all four event kinds (ready / edit / save / error)', () => {
    const events: Array<{ type: string; detail: unknown }> = [];
    const record = (e: Event) => {
      events.push({ type: e.type, detail: (e as CustomEvent).detail });
    };
    for (const kind of ['ready', 'edit', 'save', 'error']) {
      host.addEventListener(`fyredocs:${kind}`, record);
    }
    const iframe = host.shadowRoot!.querySelector('iframe')!;
    const post = (type: string, payload: unknown) => {
      const event = new MessageEvent('message', { data: { type, payload } });
      Object.defineProperty(event, 'source', { value: iframe.contentWindow });
      window.dispatchEvent(event);
    };
    post('ready', null);
    post('edit', { revId: 'r1' });
    post('save', { revId: 'r1' });
    post('error', { message: 'boom' });

    expect(events.map((e) => e.type)).toEqual([
      'fyredocs:ready',
      'fyredocs:edit',
      'fyredocs:save',
      'fyredocs:error',
    ]);
    expect(events[3].detail).toEqual({ message: 'boom' });
  });
});

describe('double-import safety', () => {
  it('does not throw when index.js is imported twice', async () => {
    // First import already ran at module load. Second import via
    // dynamic import must be a no-op — the guard in index.ts
    // checks customElements.get() before defining.
    await expect(import('../index.js')).resolves.toBeDefined();
  });
});
