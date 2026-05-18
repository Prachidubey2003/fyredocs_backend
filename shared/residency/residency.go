// Package residency is the data-residency policy primitive for
// the Fyredocs platform. Every per-org request that mutates
// customer data passes through Resolve() to learn which
// region's storage / compute is allowed to serve it; the
// edge / api-gateway then routes the request accordingly.
//
// Per plan §2.5, §4.4.5, and §4.9, Fyredocs supports
// per-region data isolation as an enterprise-tier feature.
// This package is the in-code policy contract — it defines
// the regions, the assignment shape, and the routing
// invariants. The actual multi-region deploy (separate
// Postgres replicas, separate `/files/` mounts, separate
// service fleets) is infrastructure work tracked elsewhere.
//
// What the library does:
//   - Enumerates supported regions (US-East, EU-West,
//     AP-Southeast, AU-Sydney, plus a Default sentinel).
//   - Maps org-id → Region via an in-memory assignment table
//     (the per-org row lives in auth-service; this library
//     consumes a snapshot supplied by the caller).
//   - Validates that a request currently on `serving` region
//     is allowed to handle data for `dataOwner` org. A
//     mismatch is a hard-stop — the request is rejected with
//     ErrRegionMismatch so a misrouted call can never leak
//     cross-region.
//
// What the library does NOT do:
//   - Hold a connection pool. The policy is a function; the
//     calling service holds its own region-scoped clients.
//   - Persist anything. The Routing struct is a snapshot; if
//     the assignment table changes, the caller builds a fresh
//     Routing.
//   - Replicate data. Region transitions for an existing org
//     are a separate migration tracked in the data-residency
//     runbook.
package residency

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Region identifies one of the supported deployment regions.
// Add new entries here and in IsKnownRegion in sync; otherwise
// the api-gateway will reject the unknown value at admission.
type Region string

const (
	// RegionUSEast is the default US deployment. Most existing
	// customers map here.
	RegionUSEast Region = "us-east"
	// RegionEUWest serves GDPR-required tenants. Strict no-
	// egress posture: no replication outside EU.
	RegionEUWest Region = "eu-west"
	// RegionAPSoutheast covers SG / HK / SEA tenants. Useful
	// for latency-sensitive APAC integrations.
	RegionAPSoutheast Region = "ap-southeast"
	// RegionAUSydney is the Australia/NZ deployment. Has
	// Australian Privacy Act constraints similar to GDPR.
	RegionAUSydney Region = "au-sydney"

	// RegionDefault routes to whatever the cluster's
	// configured default region is — used by the free tier
	// where the customer hasn't expressed a residency
	// preference. The api-gateway resolves Default to a
	// concrete region at admission time.
	RegionDefault Region = "default"
)

// AllRegions is the canonical iteration order. Useful for the
// admin UI / docs / tests that need to enumerate the set.
var AllRegions = []Region{
	RegionUSEast,
	RegionEUWest,
	RegionAPSoutheast,
	RegionAUSydney,
}

// IsKnownRegion reports whether `r` is in AllRegions (or is
// RegionDefault). Caller uses this at admission to reject
// unrecognised values.
func IsKnownRegion(r Region) bool {
	if r == RegionDefault {
		return true
	}
	for _, k := range AllRegions {
		if r == k {
			return true
		}
	}
	return false
}

// Routing is the in-memory policy snapshot. Construct via
// NewRouting; the caller refreshes the snapshot when the
// underlying assignment table changes.
//
// Goroutine-safe for concurrent Resolve / Validate calls; the
// internal map is taken by snapshot at construction and not
// mutated after. To update the policy, build a new Routing and
// atomically swap the pointer in the caller.
type Routing struct {
	// defaultRegion is what Resolve returns for orgs without
	// an explicit assignment. Caller picks this at startup —
	// in practice it's the cluster's nearest region.
	defaultRegion Region

	// orgAssignments maps org-id → Region. Empty map is valid;
	// every Resolve falls back to defaultRegion.
	orgAssignments map[string]Region

	// mu guards reads of orgAssignments only insofar as we
	// want the no-mutation invariant to be explicit. The map
	// is set once at construction and not mutated, so reads
	// don't need locking — kept for the future case where
	// somebody adds hot-reload.
	mu sync.RWMutex
}

// ErrUnknownRegion is returned by NewRouting / Validate when
// a region value isn't recognised. Signals a config bug at
// the caller — fix the input rather than papering over it.
var ErrUnknownRegion = errors.New("residency: unknown region")

// ErrRegionMismatch is returned by Validate when a request
// currently on region A asks to operate on data assigned to
// region B. The caller MUST treat this as a 4xx (a deliberate
// "wrong region" response is the policy boundary).
var ErrRegionMismatch = errors.New("residency: request and data are in different regions")

// NewRouting builds a Routing from a default region + an
// org-id → Region assignment map. Validates every region
// value; returns ErrUnknownRegion on a typo.
//
// The map is copied internally — callers can reuse / mutate
// theirs after the call. Pass nil for an empty assignment
// (every org → default).
func NewRouting(defaultRegion Region, orgAssignments map[string]Region) (*Routing, error) {
	if !IsKnownRegion(defaultRegion) {
		return nil, fmt.Errorf("%w: default region %q", ErrUnknownRegion, defaultRegion)
	}
	if defaultRegion == RegionDefault {
		// "Default" is a sentinel for "use whatever the
		// cluster says is default" — you can't ALSO set it
		// as the default. The caller must pick a concrete
		// region.
		return nil, fmt.Errorf("%w: defaultRegion cannot be %q (use a concrete region)", ErrUnknownRegion, RegionDefault)
	}
	copied := make(map[string]Region, len(orgAssignments))
	for orgID, region := range orgAssignments {
		if strings.TrimSpace(orgID) == "" {
			return nil, errors.New("residency: orgAssignments contains an empty org-id key")
		}
		if !IsKnownRegion(region) || region == RegionDefault {
			return nil, fmt.Errorf("%w: org %q assigned %q", ErrUnknownRegion, orgID, region)
		}
		copied[orgID] = region
	}
	return &Routing{
		defaultRegion:  defaultRegion,
		orgAssignments: copied,
	}, nil
}

// Resolve returns the region this org's data is stored in.
// Unknown / unassigned orgs fall back to the default region —
// in particular, the free tier passes its org-id and gets the
// cluster default.
func (r *Routing) Resolve(orgID string) Region {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if region, ok := r.orgAssignments[orgID]; ok {
		return region
	}
	return r.defaultRegion
}

// DefaultRegion returns the routing's configured default.
// Exposed for admin tools / observability dashboards.
func (r *Routing) DefaultRegion() Region {
	return r.defaultRegion
}

// Validate enforces the cross-region invariant: the serving
// region must equal the data-owner org's assigned region.
//
//   - Returns nil when the serving region matches.
//   - Returns ErrRegionMismatch when they differ — caller
//     should respond 4xx (gateway redirect to the right
//     region, or a hard refusal if cross-region calls aren't
//     supported in the topology).
//   - Returns ErrUnknownRegion if `servingRegion` isn't a
//     recognised value — a sign of a programming bug at the
//     caller; fail loud.
func (r *Routing) Validate(servingRegion Region, dataOwnerOrgID string) error {
	if !IsKnownRegion(servingRegion) || servingRegion == RegionDefault {
		return fmt.Errorf("%w: serving region %q", ErrUnknownRegion, servingRegion)
	}
	want := r.Resolve(dataOwnerOrgID)
	if servingRegion != want {
		return fmt.Errorf("%w: serving=%q, org %q is assigned %q",
			ErrRegionMismatch, servingRegion, dataOwnerOrgID, want)
	}
	return nil
}

// Assignments returns a defensive copy of the routing's
// org-id → region map. Useful for admin endpoints that list
// the current policy.
func (r *Routing) Assignments() map[string]Region {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]Region, len(r.orgAssignments))
	for k, v := range r.orgAssignments {
		out[k] = v
	}
	return out
}
