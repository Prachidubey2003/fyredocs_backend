package commands

import (
	"flag"
	"fmt"
	"net/url"
	"strings"
)

// Usage prints the caller's current-period rollup as a simple
// aligned table. v0 doesn't try to match Plan limits up against
// the totals — that's a follow-up once the rollup endpoint
// surfaces the user's plan info inline.
//
//	fyredocs usage [--period 2026-05]
type usageResp struct {
	UserID string `json:"userId"`
	Period string `json:"period"`
	Items  []struct {
		EventType     string `json:"eventType"`
		Unit          string `json:"unit"`
		TotalQuantity int64  `json:"totalQuantity"`
		EventCount    int64  `json:"eventCount"`
	} `json:"items"`
}

func Usage(ctx Ctx, args []string) int {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	fs.SetOutput(ctx.Stderr)
	period := fs.String("period", "", "Billing period as YYYY-MM (defaults to the current UTC month)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c, err := ctx.NewClient()
	if err != nil {
		if IsNotLoggedIn(err) {
			errorf(ctx, "usage: not logged in. Run `fyredocs login --api-key fyr_…` first.")
			return 1
		}
		errorf(ctx, "usage: %v", err)
		return 1
	}
	q := url.Values{}
	if p := strings.TrimSpace(*period); p != "" {
		q.Set("period", p)
	}
	var resp usageResp
	if err := c.Do("GET", "/v1/usage/me", q, nil, &resp); err != nil {
		errorf(ctx, "usage: %v", err)
		return 1
	}
	if len(resp.Items) == 0 {
		infof(ctx, "No metered usage for %s yet.", resp.Period)
		return 0
	}
	infof(ctx, "Period: %s", resp.Period)
	infof(ctx, "")

	// Compute column widths so the table aligns regardless of the
	// longest event-type / unit string. Two-pass: measure, then
	// emit. Fine for a CLI that prints ≤ 20 rows.
	const (
		colEvent = "Event"
		colUnit  = "Unit"
		colQty   = "Quantity"
		colCount = "Events"
	)
	wEvent, wUnit, wQty, wCount := len(colEvent), len(colUnit), len(colQty), len(colCount)
	for _, r := range resp.Items {
		if l := len(r.EventType); l > wEvent {
			wEvent = l
		}
		if l := len(r.Unit); l > wUnit {
			wUnit = l
		}
		if l := len(fmt.Sprintf("%d", r.TotalQuantity)); l > wQty {
			wQty = l
		}
		if l := len(fmt.Sprintf("%d", r.EventCount)); l > wCount {
			wCount = l
		}
	}
	headerFmt := fmt.Sprintf("%%-%ds  %%-%ds  %%%ds  %%%ds", wEvent, wUnit, wQty, wCount)
	infof(ctx, headerFmt, colEvent, colUnit, colQty, colCount)
	infof(ctx, headerFmt,
		strings.Repeat("-", wEvent),
		strings.Repeat("-", wUnit),
		strings.Repeat("-", wQty),
		strings.Repeat("-", wCount),
	)
	for _, r := range resp.Items {
		infof(ctx, headerFmt, r.EventType, r.Unit,
			fmt.Sprintf("%d", r.TotalQuantity),
			fmt.Sprintf("%d", r.EventCount))
	}
	return 0
}
