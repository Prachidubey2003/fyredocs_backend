package residency

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

// ---- Region helpers ----------------------------------------------------

func TestIsKnownRegion_AcceptsAllRegionsAndDefault(t *testing.T) {
	for _, r := range AllRegions {
		if !IsKnownRegion(r) {
			t.Errorf("IsKnownRegion(%q) = false, want true", r)
		}
	}
	if !IsKnownRegion(RegionDefault) {
		t.Error("IsKnownRegion(RegionDefault) should be true")
	}
}

func TestIsKnownRegion_RejectsUnknown(t *testing.T) {
	for _, r := range []Region{
		"us-west",   // not in v0
		"eu-east",   // not in v0
		"",          // empty
		"DEFAULT",   // case-sensitive
		"US-East",   // case-sensitive
	} {
		if IsKnownRegion(r) {
			t.Errorf("IsKnownRegion(%q) = true, want false", r)
		}
	}
}

// ---- NewRouting --------------------------------------------------------

func TestNewRouting_BuildsWithEmptyAssignments(t *testing.T) {
	r, err := NewRouting(RegionUSEast, nil)
	if err != nil {
		t.Fatalf("NewRouting: %v", err)
	}
	if r.DefaultRegion() != RegionUSEast {
		t.Errorf("DefaultRegion = %q", r.DefaultRegion())
	}
	if len(r.Assignments()) != 0 {
		t.Errorf("Assignments = %v, want empty", r.Assignments())
	}
}

func TestNewRouting_RejectsUnknownDefault(t *testing.T) {
	if _, err := NewRouting("not-a-region", nil); !errors.Is(err, ErrUnknownRegion) {
		t.Errorf("err = %v, want ErrUnknownRegion", err)
	}
}

func TestNewRouting_RejectsRegionDefaultAsDefault(t *testing.T) {
	// "default" is a sentinel for "use whatever the cluster's
	// default is" — you can't set it AS the default, or the
	// self-reference would loop.
	if _, err := NewRouting(RegionDefault, nil); !errors.Is(err, ErrUnknownRegion) {
		t.Errorf("err = %v, want ErrUnknownRegion when default = sentinel", err)
	}
}

func TestNewRouting_RejectsUnknownAssignedRegion(t *testing.T) {
	bad := map[string]Region{
		"org_a": "us-west",
	}
	_, err := NewRouting(RegionUSEast, bad)
	if !errors.Is(err, ErrUnknownRegion) {
		t.Errorf("err = %v, want ErrUnknownRegion", err)
	}
	if !strings.Contains(err.Error(), "org_a") {
		t.Errorf("err = %v, should name the offending org", err)
	}
}

func TestNewRouting_RejectsRegionDefaultInAssignment(t *testing.T) {
	// "default" makes no sense as a per-org assignment —
	// that's the meaning of "no assignment". Refuse the
	// ambiguity loudly.
	_, err := NewRouting(RegionUSEast, map[string]Region{"org_x": RegionDefault})
	if !errors.Is(err, ErrUnknownRegion) {
		t.Errorf("err = %v, want ErrUnknownRegion for default-in-assignment", err)
	}
}

func TestNewRouting_RejectsEmptyOrgID(t *testing.T) {
	_, err := NewRouting(RegionUSEast, map[string]Region{"": RegionEUWest})
	if err == nil || !strings.Contains(err.Error(), "empty org-id") {
		t.Errorf("err = %v, want empty-org-id rejection", err)
	}
}

func TestNewRouting_DefensiveCopy(t *testing.T) {
	// Caller-supplied map is copied internally so post-call
	// mutation can't retroactively change the policy.
	scratch := map[string]Region{"org_a": RegionUSEast}
	r, _ := NewRouting(RegionUSEast, scratch)
	scratch["org_a"] = RegionEUWest // mutate after construction
	if got := r.Resolve("org_a"); got != RegionUSEast {
		t.Errorf("Resolve(org_a) = %q, want us-east (post-construction mutation should be ignored)", got)
	}
}

// ---- Resolve -----------------------------------------------------------

func TestResolve_AssignedOrgReturnsItsRegion(t *testing.T) {
	r, _ := NewRouting(RegionUSEast, map[string]Region{
		"org_eu":    RegionEUWest,
		"org_apac":  RegionAPSoutheast,
	})
	if got := r.Resolve("org_eu"); got != RegionEUWest {
		t.Errorf("Resolve(org_eu) = %q", got)
	}
	if got := r.Resolve("org_apac"); got != RegionAPSoutheast {
		t.Errorf("Resolve(org_apac) = %q", got)
	}
}

func TestResolve_UnassignedOrgFallsBackToDefault(t *testing.T) {
	r, _ := NewRouting(RegionUSEast, map[string]Region{"org_eu": RegionEUWest})
	if got := r.Resolve("free_tier_org"); got != RegionUSEast {
		t.Errorf("Resolve(unassigned) = %q, want default us-east", got)
	}
}

func TestResolve_EmptyOrgIDFallsBackToDefault(t *testing.T) {
	// Caller didn't supply an org-id (anonymous /
	// pre-auth request). Same fallback as unknown org.
	r, _ := NewRouting(RegionEUWest, nil)
	if got := r.Resolve(""); got != RegionEUWest {
		t.Errorf("Resolve(\"\") = %q, want default eu-west", got)
	}
}

// ---- Validate ----------------------------------------------------------

func TestValidate_MatchingRegionPasses(t *testing.T) {
	r, _ := NewRouting(RegionUSEast, map[string]Region{"org_eu": RegionEUWest})
	if err := r.Validate(RegionEUWest, "org_eu"); err != nil {
		t.Errorf("Validate(eu-west, org_eu) = %v, want nil", err)
	}
	// Unassigned org → default; serving on default should
	// also pass.
	if err := r.Validate(RegionUSEast, "free_tier_org"); err != nil {
		t.Errorf("Validate(us-east, free) = %v, want nil (default)", err)
	}
}

func TestValidate_MismatchReturnsErrRegionMismatch(t *testing.T) {
	r, _ := NewRouting(RegionUSEast, map[string]Region{"org_eu": RegionEUWest})
	err := r.Validate(RegionUSEast, "org_eu")
	if !errors.Is(err, ErrRegionMismatch) {
		t.Fatalf("err = %v, want ErrRegionMismatch", err)
	}
	// Error message should identify both sides so the
	// gateway can produce a useful 4xx body.
	for _, want := range []string{"us-east", "org_eu", "eu-west"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, should contain %q", err, want)
		}
	}
}

func TestValidate_UnknownServingRegionReturnsUnknown(t *testing.T) {
	r, _ := NewRouting(RegionUSEast, nil)
	err := r.Validate("not-a-region", "any_org")
	if !errors.Is(err, ErrUnknownRegion) {
		t.Errorf("err = %v, want ErrUnknownRegion", err)
	}
}

func TestValidate_RegionDefaultAsServingIsRejected(t *testing.T) {
	// "default" must be resolved to a concrete region BEFORE
	// the request hits Validate. A serving region of
	// "default" is a programming bug.
	r, _ := NewRouting(RegionUSEast, nil)
	if err := r.Validate(RegionDefault, "any_org"); !errors.Is(err, ErrUnknownRegion) {
		t.Errorf("err = %v, want ErrUnknownRegion for default-as-serving", err)
	}
}

// ---- Assignments accessor ---------------------------------------------

func TestAssignments_ReturnsCopy(t *testing.T) {
	r, _ := NewRouting(RegionUSEast, map[string]Region{"org_a": RegionEUWest})
	got := r.Assignments()
	got["org_a"] = "tampered"        // mutate the copy
	got["new_org"] = RegionAUSydney  // and add
	// Routing should still report the original.
	if r.Resolve("org_a") != RegionEUWest {
		t.Errorf("Routing mutated via Assignments() copy")
	}
	if _, present := r.Assignments()["new_org"]; present {
		t.Errorf("Routing acquired new_org through Assignments() copy")
	}
}

// ---- Concurrency -------------------------------------------------------

func TestRouting_ConcurrentResolveSafe(t *testing.T) {
	// The package documents goroutine-safe reads. Hammer it
	// concurrently so the race detector catches any
	// regression (run with -race).
	r, _ := NewRouting(RegionUSEast, map[string]Region{
		"a": RegionEUWest,
		"b": RegionAPSoutheast,
		"c": RegionAUSydney,
	})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				_ = r.Resolve("a")
				_ = r.Resolve("missing")
				_ = r.Validate(RegionEUWest, "a")
				_ = r.Assignments()
			}
		}(i)
	}
	wg.Wait()
}
