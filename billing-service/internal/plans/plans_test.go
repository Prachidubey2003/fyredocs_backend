package plans

import "testing"

func TestAll_ReturnsEveryRegisteredPlan(t *testing.T) {
	got := All()
	if len(got) < 5 {
		t.Fatalf("All() returned %d plans, want at least 5 (Free, Pro, Teams, Business, Enterprise)", len(got))
	}
	wantCodes := []string{FreeCode, ProCode, TeamsCode, BusinessCode, EnterpriseCode}
	for _, code := range wantCodes {
		if _, ok := Lookup(code); !ok {
			t.Errorf("registry missing plan %q", code)
		}
	}
}

func TestLookup_UnknownReturnsFalse(t *testing.T) {
	if _, ok := Lookup("nonexistent"); ok {
		t.Error("Lookup(\"nonexistent\") returned ok=true, want false")
	}
	if _, ok := Lookup(""); ok {
		t.Error("Lookup(\"\") returned ok=true, want false")
	}
}

func TestDefaultPlan_IsFree(t *testing.T) {
	dp := DefaultPlan()
	if dp.Code != FreeCode {
		t.Errorf("DefaultPlan().Code = %q, want %q", dp.Code, FreeCode)
	}
	if dp.MonthlyPriceCents != 0 {
		t.Errorf("DefaultPlan() price = %d, want 0", dp.MonthlyPriceCents)
	}
}

func TestEnterprise_NotSelfServe(t *testing.T) {
	// Enterprise must be sales-led — handlers gate
	// /v1/billing/me/subscribe on SelfServe=true. If this flag
	// flips by accident, users could self-onboard onto a plan
	// whose pricing isn't even defined.
	p, ok := Lookup(EnterpriseCode)
	if !ok {
		t.Fatal("Enterprise plan missing from registry")
	}
	if p.SelfServe {
		t.Error("Enterprise.SelfServe = true, want false (sales-led only)")
	}
	if p.MonthlyPriceCents != -1 {
		t.Errorf("Enterprise.MonthlyPriceCents = %d, want -1 (contact-sales sentinel)", p.MonthlyPriceCents)
	}
}

func TestPerSeatFlag_TeamsAndBusiness(t *testing.T) {
	cases := []struct {
		code         string
		wantPerSeat  bool
	}{
		{FreeCode, false},
		{ProCode, false},
		{TeamsCode, true},
		{BusinessCode, true},
		{EnterpriseCode, true},
	}
	for _, tc := range cases {
		p, ok := Lookup(tc.code)
		if !ok {
			t.Errorf("plan %q missing", tc.code)
			continue
		}
		if p.PerSeat != tc.wantPerSeat {
			t.Errorf("%q.PerSeat = %v, want %v", tc.code, p.PerSeat, tc.wantPerSeat)
		}
	}
}

func TestFreePlan_HasLimits(t *testing.T) {
	// The Free plan's limits drive the gateway rate-limit
	// configuration. Empty limits would silently disable plan
	// gating — important to guard against.
	free, _ := Lookup(FreeCode)
	if len(free.Limits) == 0 {
		t.Error("Free.Limits is empty; gateway rate-limit would be unrestricted")
	}
	if free.Limits["op.merge"] <= 0 {
		t.Errorf("Free.Limits[\"op.merge\"] = %d, want > 0 (free tier caps daily ops)", free.Limits["op.merge"])
	}
}
