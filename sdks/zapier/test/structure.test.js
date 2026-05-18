// Structural smoke tests for the Zapier app definition.
//
// These run via plain `node --test` so they don't pull
// `zapier-platform-core` (the dep is real but the
// platform's test harness needs a Zapier account + auth).
// What we CAN check without the harness: the exported app
// shape, the resource keys + display strings + the
// authentication contract.
//
// `zapier validate` is the deeper structural check run by
// the maintainer pre-push; this file catches the basic
// "did I rename a key and break Zapier discovery" class of
// bug in CI.

const test = require('node:test');
const assert = require('node:assert/strict');

const App = require('../index');
const auth = require('../authentication');

test('exports the required top-level shape', () => {
  assert.equal(typeof App.version, 'string', 'version must be a string');
  assert.match(App.version, /^\d+\.\d+\.\d+$/, 'version must be semver');
  assert.equal(typeof App.platformVersion, 'string');
  assert.ok(App.authentication, 'authentication is required');
  assert.ok(Array.isArray(App.beforeRequest), 'beforeRequest must be an array');
  assert.ok(App.triggers && typeof App.triggers === 'object');
  assert.ok(App.creates && typeof App.creates === 'object');
});

test('authentication is custom + test calls /auth/me', () => {
  assert.equal(App.authentication.type, 'custom');
  assert.equal(App.authentication.test.url, 'https://api.fyredocs.com/auth/me');
  assert.equal(App.authentication.test.method, 'GET');
  // The single required field is `apiKey` (password-typed
  // so Zapier masks it in the dashboard).
  const fields = App.authentication.fields;
  assert.equal(fields.length, 1);
  assert.equal(fields[0].key, 'apiKey');
  assert.equal(fields[0].type, 'password');
  assert.equal(fields[0].required, true);
});

test('connectionLabel renders a friendly string from the /auth/me payload', () => {
  // bundle.cleanedRequest is what Zapier passes after the
  // test request returns — emulate it here.
  const label = App.authentication.connectionLabel(null, {
    cleanedRequest: { data: { user: { fullName: 'Alice', email: 'alice@acme.example' } } },
  });
  assert.equal(label, 'Alice (alice@acme.example)');

  // Falls back to email-only when the name is absent.
  const labelEmail = App.authentication.connectionLabel(null, {
    cleanedRequest: { data: { user: { email: 'bob@x.example' } } },
  });
  assert.equal(labelEmail, 'bob@x.example');

  // Falls back to a generic string when both are missing —
  // never returns undefined / breaks the Zapier dashboard.
  const labelGeneric = App.authentication.connectionLabel(null, { cleanedRequest: {} });
  assert.equal(labelGeneric, 'Fyredocs account');
});

test('includeBearerToken middleware injects Authorization header', () => {
  const req = { headers: {} };
  const out = auth.includeBearerToken(req, null, { authData: { apiKey: 'fyr_live_abc' } });
  assert.equal(out.headers.Authorization, 'Bearer fyr_live_abc');
});

test('includeBearerToken is a no-op when authData is absent', () => {
  // Edge case: the test request runs BEFORE authData is
  // populated for some flows; the middleware must not
  // throw / mangle the request.
  const req = { headers: { 'X-Existing': 'keep' } };
  const out = auth.includeBearerToken(req, null, {});
  assert.equal(out.headers['X-Existing'], 'keep');
  assert.equal(out.headers.Authorization, undefined);
});

test('newEvent trigger is wired as a REST hook with the supported event-type choices', () => {
  const trigger = App.triggers.newEvent;
  assert.ok(trigger, 'newEvent trigger must be exported');
  assert.equal(trigger.key, 'newEvent');
  assert.equal(trigger.operation.type, 'hook');
  assert.equal(typeof trigger.operation.performSubscribe, 'function');
  assert.equal(typeof trigger.operation.performUnsubscribe, 'function');
  assert.equal(typeof trigger.operation.perform, 'function');
  assert.equal(typeof trigger.operation.performList, 'function');

  const eventTypeField = trigger.operation.inputFields.find((f) => f.key === 'eventType');
  assert.ok(eventTypeField, 'eventType field must be present');
  assert.equal(eventTypeField.required, true);
  // Pin the exact set of allowed events — drift here
  // would cause Zapier subscriptions to fail at create
  // time against notify-service's allowlist.
  assert.deepEqual(Object.keys(eventTypeField.choices).sort(), [
    'document.converted',
    'document.signed',
    'subscription.canceled',
    'subscription.changed',
    'subscription.created',
  ]);
});

test('newEvent trigger sample shape pins the wire contract', () => {
  const sample = App.triggers.newEvent.operation.sample;
  for (const key of ['id', 'eventType', 'occurredAt', 'data']) {
    assert.ok(key in sample, `sample must include ${key}`);
  }
  assert.equal(typeof sample.data, 'object');
});

test('newEvent.perform maps Fyredocs payload to Zapier-shaped output', () => {
  // perform receives the inbound webhook body. Our shape
  // mirrors the DomainEvent envelope: {eventId, eventType,
  // occurredAt, data}.
  const out = App.triggers.newEvent.operation.perform(null, {
    cleanedRequest: {
      eventId: 'evt_test_001',
      eventType: 'document.converted',
      occurredAt: '2026-05-18T00:01:00Z',
      data: { jobId: 'j1', tool: 'word-to-pdf' },
    },
  });
  assert.equal(out.length, 1);
  assert.equal(out[0].id, 'evt_test_001');
  assert.equal(out[0].eventType, 'document.converted');
  assert.equal(out[0].data.jobId, 'j1');
});

test('newEvent.perform falls back to a synthesised id when eventId is absent', () => {
  // Legacy payloads + the synthetic webhook.test event
  // sometimes lack eventId. The trigger must NOT crash on
  // Zapier's dedup pass — generate something stable-ish.
  const out = App.triggers.newEvent.operation.perform(null, {
    cleanedRequest: { eventType: 'document.signed', data: {} },
  });
  assert.equal(out.length, 1);
  assert.match(out[0].id, /^evt_\d+$/);
});

test('newEvent.perform returns [] for malformed body (defensive)', () => {
  // Zapier's harness should never invoke perform with a
  // non-object body, but a misconfigured upstream proxy
  // could. Returning an empty array (rather than throwing)
  // means Zapier shows "no new events" instead of an error.
  for (const bad of [null, undefined, 'string', 42]) {
    const out = App.triggers.newEvent.operation.perform(null, { cleanedRequest: bad });
    assert.deepEqual(out, []);
  }
});

test('convertToPdf create exposes required source-file input + sample shape', () => {
  const create = App.creates.convertToPdf;
  assert.ok(create);
  assert.equal(create.key, 'convertToPdf');
  const sourceField = create.operation.inputFields.find((f) => f.key === 'sourceUrl');
  assert.ok(sourceField);
  assert.equal(sourceField.type, 'file');
  assert.equal(sourceField.required, true);
  const sample = create.operation.sample;
  for (const key of ['jobId', 'status', 'tool', 'createdAt']) {
    assert.ok(key in sample, `convertToPdf sample missing ${key}`);
  }
});

test('signPdf create exposes the position dropdown with the seven pdfcpu anchors', () => {
  const create = App.creates.signPdf;
  assert.ok(create);
  const positionField = create.operation.inputFields.find((f) => f.key === 'position');
  assert.ok(positionField);
  // The seven anchors map directly to pdfcpu's signature
  // watermark positions used by the organize-pdf worker.
  assert.deepEqual(Object.keys(positionField.choices).sort(), [
    'bc',
    'bl',
    'br',
    'c',
    'tc',
    'tl',
    'tr',
  ]);
  // Default must be bottom-right — matches the
  // server-side default and the most common visual
  // signature placement.
  assert.equal(positionField.default, 'br');
});

test('every trigger + create supplies a display label + description', () => {
  for (const [key, t] of Object.entries(App.triggers)) {
    assert.ok(t.display?.label, `trigger ${key} missing display.label`);
    assert.ok(t.display?.description, `trigger ${key} missing display.description`);
  }
  for (const [key, c] of Object.entries(App.creates)) {
    assert.ok(c.display?.label, `create ${key} missing display.label`);
    assert.ok(c.display?.description, `create ${key} missing display.description`);
  }
});
