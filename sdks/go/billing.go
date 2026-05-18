package fyredocs

import (
	"context"
	"net/http"
)

// BillingAPI wraps the `/api/billing/v1/billing/*` endpoints owned
// by billing-service.
type BillingAPI struct {
	c *Client
}

// Plans returns the list of self-serve + sales-led plans the user
// is eligible to subscribe to.
func (b *BillingAPI) Plans(ctx context.Context) ([]Plan, error) {
	var wrapper struct {
		Plans []Plan `json:"plans"`
	}
	if err := b.c.Request(ctx, "/api/billing/v1/billing/plans", RequestOptions{
		Method: http.MethodGet,
		Out:    &wrapper,
	}); err != nil {
		return nil, err
	}
	return wrapper.Plans, nil
}

// Me returns the calling user's plan + subscription + current
// usage rollup.
func (b *BillingAPI) Me(ctx context.Context) (*BillingMeResponse, error) {
	var out BillingMeResponse
	if err := b.c.Request(ctx, "/api/billing/v1/billing/me", RequestOptions{
		Method: http.MethodGet,
		Out:    &out,
	}); err != nil {
		return nil, err
	}
	return &out, nil
}

// Subscribe upgrades / downgrades / starts the calling user's
// subscription. Returns the updated Subscription.
func (b *BillingAPI) Subscribe(ctx context.Context, req SubscribeRequest) (*Subscription, error) {
	var out Subscription
	if err := b.c.Request(ctx, "/api/billing/v1/billing/me/subscribe", RequestOptions{
		Method: http.MethodPost,
		Body:   req,
		Out:    &out,
	}); err != nil {
		return nil, err
	}
	return &out, nil
}
