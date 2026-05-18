// REST-hook trigger: "New Fyredocs event".
//
// The user picks an event type from a dropdown and Zapier
// auto-fires whenever Fyredocs emits that event for the
// account. Implemented via the notify-service webhook
// subscription registry — Zapier's `targetUrl` is the
// subscription's POST target.
//
// Why REST hooks (not polling): Fyredocs already ships
// HMAC-signed event POSTs at sub-second latency via the
// fanout pipeline. Polling would add 1–5 minute Zap-trigger
// lag + N×poll-rate cost-per-account vs. the zero-overhead
// hook fan-out. The user's webhook secret is generated on
// our side and stored once on the Zapier side; the
// signature header is informational here because Zapier
// only forwards the body — verification is the user's
// downstream concern when they bridge Zapier to a custom
// receiver.

const WEBHOOK_BASE = 'https://api.fyredocs.com/api/notify/v1/notify/webhooks';

const subscribeHook = async (z, bundle) => {
  const response = await z.request({
    url: WEBHOOK_BASE,
    method: 'POST',
    body: {
      eventType: bundle.inputData.eventType,
      targetUrl: bundle.targetUrl,
    },
  });
  // notify-service returns the subscription row flat (not
  // wrapped in {success, data, ...}) — same convention as
  // the Stripe checkout endpoint. response.data is the row.
  return response.data;
};

const unsubscribeHook = async (z, bundle) => {
  const id = bundle.subscribeData?.id;
  if (!id) {
    // No id stored — Zapier already cleaned up or never
    // subscribed. Return silently so the unsub call is
    // idempotent.
    return {};
  }
  await z.request({
    url: `${WEBHOOK_BASE}/${encodeURIComponent(id)}`,
    method: 'DELETE',
  });
  return {};
};

// performList drives the Zapier "Test Trigger" button — it
// fetches the most recent delivery rows so the user can pick
// a sample shape in the Zap editor without waiting for a
// real event. The user filters by their chosen eventType.
const performList = async (z, bundle) => {
  const response = await z.request({
    url: 'https://api.fyredocs.com/api/notify/v1/notify/deliveries',
    method: 'GET',
    params: { limit: 10 },
  });
  const items = response.data?.data?.items ?? [];
  // Project each delivery row to the same shape the live
  // webhook would deliver — `eventType` + `data` come from
  // the persisted payload column.
  return items
    .filter((row) => row.status === 'delivered')
    .map((row) => {
      const payload = row.payload && typeof row.payload === 'object' ? row.payload : {};
      return {
        id: row.id,
        eventType: payload.eventType ?? bundle.inputData.eventType,
        occurredAt: row.createdAt,
        data: payload.data ?? payload,
      };
    });
};

// The inbound webhook handler. Zapier hands us the raw POST
// body Fyredocs delivered + the request signature header.
// We pass the payload through as-is so the Zap user can map
// `data.*` fields to downstream actions in the Zap editor.
const performHook = (z, bundle) => {
  const payload = bundle.cleanedRequest;
  if (!payload || typeof payload !== 'object') {
    return [];
  }
  return [
    {
      id: payload.eventId ?? `evt_${Date.now()}`,
      eventType: payload.eventType,
      occurredAt: payload.occurredAt,
      data: payload.data ?? {},
    },
  ];
};

module.exports = {
  key: 'newEvent',
  noun: 'Event',
  display: {
    label: 'New Fyredocs event',
    description:
      'Fires when the selected Fyredocs event occurs (document converted, signed, subscription changed, etc.).',
  },
  operation: {
    type: 'hook',
    inputFields: [
      {
        key: 'eventType',
        label: 'Event type',
        type: 'string',
        required: true,
        choices: {
          'document.converted': 'Document converted',
          'document.signed': 'Document signed',
          'subscription.created': 'Subscription created',
          'subscription.changed': 'Subscription changed',
          'subscription.canceled': 'Subscription canceled',
        },
        helpText:
          'Pick one. Each Zap subscribes to exactly one event. Add another Zap for a different event.',
      },
    ],
    performSubscribe: subscribeHook,
    performUnsubscribe: unsubscribeHook,
    performList,
    perform: performHook,
    sample: {
      id: 'evt_01HW...',
      eventType: 'document.converted',
      occurredAt: '2026-05-18T00:01:00Z',
      data: {
        jobId: 'job_01HV...',
        tool: 'word-to-pdf',
        outputPath: '/files/users/u1/jobs/job_01HV.../output.pdf',
        fileSize: 184273,
      },
    },
    outputFields: [
      { key: 'id', label: 'Event ID' },
      { key: 'eventType', label: 'Event type' },
      { key: 'occurredAt', label: 'Occurred at', type: 'datetime' },
      { key: 'data', label: 'Event payload (object)' },
    ],
  },
};
