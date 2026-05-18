package commands

import (
	"net/url"
)

// Whoami prints the caller's plan + subscription status.
// Useful smoke test for "are my credentials wired correctly?".
//
//	fyredocs whoami
type whoamiResp struct {
	Plan struct {
		Code string `json:"code"`
		Name string `json:"name"`
	} `json:"plan"`
	Subscription *struct {
		Status             string `json:"status"`
		Seats              int    `json:"seats"`
		CurrentPeriodEnd   string `json:"currentPeriodEnd"`
	} `json:"subscription,omitempty"`
}

func Whoami(ctx Ctx, _ []string) int {
	c, err := ctx.NewClient()
	if err != nil {
		if IsNotLoggedIn(err) {
			errorf(ctx, "whoami: not logged in. Run `fyredocs login --api-key fyr_…` first.")
			return 1
		}
		errorf(ctx, "whoami: %v", err)
		return 1
	}
	var resp whoamiResp
	if err := c.Do("GET", "/api/billing/v1/billing/me", url.Values{}, nil, &resp); err != nil {
		errorf(ctx, "whoami: %v", err)
		return 1
	}
	infof(ctx, "Plan:   %s (%s)", resp.Plan.Name, resp.Plan.Code)
	if resp.Subscription == nil {
		infof(ctx, "Status: free tier (no subscription)")
		return 0
	}
	infof(ctx, "Status: %s", resp.Subscription.Status)
	if resp.Subscription.Seats > 1 {
		infof(ctx, "Seats:  %d", resp.Subscription.Seats)
	}
	if resp.Subscription.CurrentPeriodEnd != "" {
		infof(ctx, "Renews: %s", resp.Subscription.CurrentPeriodEnd)
	}
	return 0
}
