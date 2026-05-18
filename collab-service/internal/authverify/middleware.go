package authverify

import (
	"net/http"
	"strings"

	"fyredocs/shared/response"
)

// MiddlewareOptions configures the net/http auth middleware.
//
// TrustGatewayHeaders bypasses JWT verification when the gateway
// has set X-User-ID. The gateway is the canonical verifier; the
// header path lets the service skip a redundant verification in
// production while keeping the JWT path as a defense-in-depth
// fallback when the gateway is bypassed (direct testing,
// in-cluster traffic, etc.).
//
// AccessTokenCookieName + AccessTokenQueryParam exist because
// browsers can't set custom headers on `new WebSocket(url)`.
// Cookies are the production path (auth-service sets them on
// login); the query parameter is a controlled escape hatch for
// flows where cookies aren't available (CORS-restricted iframes,
// preview links, etc.). Both are checked AFTER the Authorization
// header, which non-browser clients can still set normally.
type MiddlewareOptions struct {
	Verifier              *Verifier
	TrustGatewayHeaders   bool
	AccessTokenCookieName string
	AccessTokenQueryParam string
}

// Middleware returns an http.Handler wrapping `next` with auth.
// Behaviour:
//   - OPTIONS passes through (CORS preflight).
//   - If TrustGatewayHeaders + X-User-ID is set → use it.
//   - Else extract a token from (Authorization Bearer, cookie,
//     query param) in that order.
//   - Missing token → 401.
//   - Invalid token → 401.
//   - `is_guest=true` claim → 401 (collab requires an account).
//   - Valid token → AuthContext attached to ctx, calls next.
func Middleware(opts MiddlewareOptions, next http.Handler) http.Handler {
	cookieName := opts.AccessTokenCookieName
	if cookieName == "" {
		cookieName = "access_token"
	}
	queryParam := opts.AccessTokenQueryParam
	if queryParam == "" {
		queryParam = "access_token"
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		if opts.TrustGatewayHeaders {
			if authCtx, ok := authContextFromGatewayHeaders(r.Header); ok {
				r = r.WithContext(WithAuthContext(r.Context(), authCtx))
				next.ServeHTTP(w, r)
				return
			}
		}

		token, hasToken := extractBearerToken(r.Header.Get("Authorization"))
		if !hasToken {
			if cookie, err := r.Cookie(cookieName); err == nil {
				token = strings.TrimSpace(cookie.Value)
				hasToken = token != ""
			}
		}
		if !hasToken {
			// Query-param fallback for browser WS clients. Cookie
			// remains the recommended path; the query param is
			// here so embed-iframe flows that strip third-party
			// cookies still have a way to authenticate.
			if q := strings.TrimSpace(r.URL.Query().Get(queryParam)); q != "" {
				token = q
				hasToken = true
			}
		}

		if !hasToken {
			writeUnauthorized(w)
			return
		}
		if opts.Verifier == nil {
			writeUnauthorized(w)
			return
		}
		claims, err := opts.Verifier.Verify(r.Context(), token)
		if err != nil {
			writeUnauthorized(w)
			return
		}
		if claims.IsGuest {
			// Guests can run one-off conversion jobs but cannot
			// open multiplayer sessions — there's no owned doc
			// to share.
			writeUnauthorized(w)
			return
		}
		r = r.WithContext(WithAuthContext(r.Context(), claims.ToAuthContext()))
		next.ServeHTTP(w, r)
	})
}

func writeUnauthorized(w http.ResponseWriter) {
	response.WriteErr(w, http.StatusUnauthorized, "AUTH_UNAUTHORIZED",
		"Your session has expired. Please log in again.")
}

func authContextFromGatewayHeaders(header http.Header) (AuthContext, bool) {
	if header == nil {
		return AuthContext{}, false
	}
	userID := strings.TrimSpace(header.Get("X-User-ID"))
	if userID == "" {
		return AuthContext{}, false
	}
	role := strings.TrimSpace(header.Get("X-User-Role"))
	scope := splitScope(header.Get("X-User-Scope"))
	return AuthContext{
		UserID: userID,
		Role:   role,
		Scope:  scope,
	}, true
}

func extractBearerToken(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 {
		return "", false
	}
	if !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}
	token := strings.TrimSpace(parts[1])
	if token == "" {
		return "", false
	}
	return token, true
}
