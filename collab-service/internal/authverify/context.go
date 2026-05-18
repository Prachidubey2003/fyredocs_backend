package authverify

import (
	"context"
)

// AuthContext is the per-request identity payload, derived from a
// verified JWT or from gateway-supplied headers when
// AUTH_TRUST_GATEWAY_HEADERS is true.
//
// Unlike the editor-service variant there is no IsGuest field —
// guest sessions can't open collab rooms (multiplayer requires an
// owned identity), so any code path that needed to special-case
// guests in editor-service is just a 401 here.
type AuthContext struct {
	UserID string
	Role   string
	Scope  []string
}

type authContextKey struct{}

var authKey = authContextKey{}

func WithAuthContext(ctx context.Context, authCtx AuthContext) context.Context {
	return context.WithValue(ctx, authKey, authCtx)
}

func FromContext(ctx context.Context) (AuthContext, bool) {
	if ctx == nil {
		return AuthContext{}, false
	}
	value := ctx.Value(authKey)
	authCtx, ok := value.(AuthContext)
	return authCtx, ok
}
