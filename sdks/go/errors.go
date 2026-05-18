package fyredocs

import "fmt"

// Error is the single error type the SDK returns from API calls.
// Callers can type-assert via errors.As to access Status and Code
// without parsing the message string.
//
//	var apiErr *fyredocs.Error
//	if errors.As(err, &apiErr) {
//	    if apiErr.Status == 401 { /* re-auth */ }
//	    if apiErr.Code == "RATE_LIMITED" { /* back off */ }
//	}
//
// Status == 0 means the request never reached the server (network
// error, DNS failure, timeout). Code is then either "NETWORK" or
// "READ_FAILED" so callers can distinguish.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("fyredocs: %s (%s %d)", e.Message, e.Code, e.Status)
	}
	return fmt.Sprintf("fyredocs: %s %d", e.Code, e.Status)
}
