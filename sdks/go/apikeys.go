package fyredocs

import (
	"context"
	"net/http"
	"net/url"
)

// APIKeysAPI wraps the `/auth/api-keys/*` endpoints owned by
// auth-service.
type APIKeysAPI struct {
	c *Client
}

// ListAPIKeysOptions tunes a List call. Zero-value defaults are
// "active keys only".
type ListAPIKeysOptions struct {
	// Revoked, when true, switches to the audit archive of
	// revoked keys instead of active keys.
	Revoked bool
}

// List returns the calling user's API keys.
func (a *APIKeysAPI) List(ctx context.Context, opts *ListAPIKeysOptions) ([]APIKey, error) {
	q := url.Values{}
	if opts != nil && opts.Revoked {
		q.Set("revoked", "true")
	}
	var out []APIKey
	if err := a.c.Request(ctx, "/auth/api-keys", RequestOptions{
		Method: http.MethodGet,
		Query:  q,
		Out:    &out,
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// Issue mints a new API key. The plaintext is in the returned
// response — display + persist it immediately, the server can't
// recover it.
func (a *APIKeysAPI) Issue(ctx context.Context, req IssueAPIKeyRequest) (*IssueAPIKeyResponse, error) {
	var out IssueAPIKeyResponse
	if err := a.c.Request(ctx, "/auth/api-keys", RequestOptions{
		Method: http.MethodPost,
		Body:   req,
		Out:    &out,
	}); err != nil {
		return nil, err
	}
	return &out, nil
}

// Revoke marks the key with `id` as revoked. Idempotent — calling
// revoke on an already-revoked key is a no-op at the server.
func (a *APIKeysAPI) Revoke(ctx context.Context, id string) error {
	return a.c.Request(ctx, "/auth/api-keys/"+url.PathEscape(id)+"/revoke", RequestOptions{
		Method: http.MethodPost,
	})
}
