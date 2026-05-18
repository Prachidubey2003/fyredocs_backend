// Fyredocs Zapier app — entry point. Exported shape is what
// zapier-platform-core consumes; the Zapier CLI's `zapier
// validate` / `zapier push` commands read from here.

const { authentication, includeBearerToken } = require('./authentication');
const newEventTrigger = require('./triggers/event_webhook');
const convertToPdfCreate = require('./creates/convert_to_pdf');
const signPdfCreate = require('./creates/sign_pdf');

const App = {
  // Version of THIS Zapier app — bumped per release; the
  // Zapier dashboard surfaces this so users know which
  // version they're on. Independent from package.json
  // version intentionally so a doc-only release doesn't
  // force a Zapier dashboard prompt.
  version: '0.1.0',

  // platformVersion locks us to a specific zapier-platform-core
  // major. Bumps require coordinated app-definition shape
  // review.
  platformVersion: '16.4.0',

  authentication,

  // Inject the Bearer token on every outbound request. Lives
  // here (rather than per-trigger / per-create) so a new
  // resource added later inherits auth without ceremony.
  beforeRequest: [includeBearerToken],

  // afterResponse middleware would land here for shared
  // response-shape unwrapping. For now we leave it empty —
  // each resource handles its own envelope shape because
  // the backend mixes flat-returning endpoints (webhook
  // create, checkout-session) with envelope-wrapped
  // endpoints (everything else). Forcing a uniform unwrap
  // here would mis-shape one side.
  afterResponse: [],

  triggers: {
    [newEventTrigger.key]: newEventTrigger,
  },

  creates: {
    [convertToPdfCreate.key]: convertToPdfCreate,
    [signPdfCreate.key]: signPdfCreate,
  },

  searches: {},

  resources: {},
};

module.exports = App;
