package fyredocs

import (
	"context"
	"net/http"
	"net/url"
)

// UsageAPI wraps the `/v1/usage/*` endpoints owned by
// analytics-service.
type UsageAPI struct {
	c *Client
}

// UsageMeOptions tunes a Me call.
type UsageMeOptions struct {
	// Period selects a specific YYYY-MM rollup. Empty means
	// "current UTC month" (server-side default).
	Period string
}

// Me returns the calling user's usage rollup for the supplied
// period (or current month when Period is empty).
func (u *UsageAPI) Me(ctx context.Context, opts *UsageMeOptions) (*UsageMeResponse, error) {
	q := url.Values{}
	if opts != nil && opts.Period != "" {
		q.Set("period", opts.Period)
	}
	var out UsageMeResponse
	if err := u.c.Request(ctx, "/v1/usage/me", RequestOptions{
		Method: http.MethodGet,
		Query:  q,
		Out:    &out,
	}); err != nil {
		return nil, err
	}
	return &out, nil
}
