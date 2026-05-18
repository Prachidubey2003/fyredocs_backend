// API-key authentication. The user pastes a token they minted
// at fyredocs.com → Account → API keys. Tokens always start
// with `fyr_live_` or `fyr_test_` and are 43-char base64url
// strings after the prefix.
//
// We deliberately do NOT support session-cookie auth here:
// Zapier runs in a different origin from the user's browser,
// has no access to the auth cookie, and a long-lived API key
// is the canonical pattern for server-to-server integrations.
//
// `connectionLabel` is what users see in the Zapier dashboard
// after connecting. Pulling the user's email (via /auth/me)
// makes "Acme Inc. (alice@acme.example)" the dashboard label
// instead of an opaque token prefix.

const TEST_URL = 'https://api.fyredocs.com/auth/me';

const authentication = {
  type: 'custom',
  test: {
    url: TEST_URL,
    method: 'GET',
  },
  // Token shape: `fyr_<env>_<prefix>_<secret>`. Field is
  // marked `required` + `password` so the value is masked in
  // the Zapier UI and never persisted in plaintext on the
  // user's browser side.
  fields: [
    {
      key: 'apiKey',
      label: 'API key',
      type: 'password',
      required: true,
      helpText:
        'Generate one at https://fyredocs.com/account/api-keys. Keys begin with `fyr_live_` (production) or `fyr_test_` (sandbox).',
    },
  ],
  // After auth succeeds, surface a human-friendly label in the
  // user's Zapier dashboard.
  connectionLabel: (z, bundle) => {
    const user = bundle.inputData?.user ?? bundle.cleanedRequest?.data?.user ?? {};
    const email = user.email ?? '';
    const name = user.fullName ?? user.name ?? '';
    if (name && email) return `${name} (${email})`;
    return email || name || 'Fyredocs account';
  },
};

// includeBearerToken is the request middleware that injects
// the API key on EVERY outbound call. Registered globally in
// index.js so triggers + creates inherit it without having to
// remember to set the header themselves.
function includeBearerToken(request, z, bundle) {
  if (bundle.authData?.apiKey) {
    request.headers = request.headers || {};
    request.headers.Authorization = `Bearer ${bundle.authData.apiKey}`;
  }
  return request;
}

module.exports = {
  authentication,
  includeBearerToken,
  TEST_URL,
};
