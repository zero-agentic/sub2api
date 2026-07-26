package modelark

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeErrorSharesSingleMapping(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       int
		internalCode string
		wantStatus   int
		wantCode     string
	}{
		{"quota code wins over status", http.StatusForbidden, "INSUFFICIENT_BALANCE", http.StatusTooManyRequests, "QuotaExceeded"},
		{"quota status with quota code", http.StatusTooManyRequests, "USER_QUOTA_EXCEEDED", http.StatusTooManyRequests, "QuotaExceeded"},
		{"bad request", http.StatusBadRequest, "INVALID_PARAM", http.StatusBadRequest, "InvalidParameter"},
		{"body too large", http.StatusRequestEntityTooLarge, "", http.StatusRequestEntityTooLarge, "InvalidParameter"},
		{"unauthorized", http.StatusUnauthorized, "INVALID_API_KEY", http.StatusUnauthorized, "AuthenticationError"},
		{"forbidden", http.StatusForbidden, "GROUP_DISABLED", http.StatusForbidden, "InvalidAccountStatus"},
		{"not found", http.StatusNotFound, "", http.StatusNotFound, "NotFound"},
		{"rate limit is not server overload", http.StatusTooManyRequests, "USER_RPM_LIMIT", http.StatusTooManyRequests, "AccountRateLimitExceeded"},
		{"unknown status", http.StatusBadGateway, "", http.StatusInternalServerError, "InternalServiceError"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, code := NormalizeError(tc.status, tc.internalCode)
			require.Equal(t, tc.wantStatus, status)
			require.Equal(t, tc.wantCode, code)
		})
	}
}
