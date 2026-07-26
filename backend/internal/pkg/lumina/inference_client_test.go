package lumina

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsAuthenticationErrorTrustsOnlyStructuredSignals(t *testing.T) {
	require.True(t, IsAuthenticationError(&APIError{Status: http.StatusUnauthorized}))
	require.True(t, IsAuthenticationError(&APIError{Status: http.StatusUnauthorized, Code: "100013", Message: "not logged in"}))

	// Cloudflare risk-control blocks are not session expiry and must not drive re-login.
	require.False(t, IsAuthenticationError(&APIError{Status: http.StatusForbidden}))
	require.False(t, IsAuthenticationError(&APIError{Status: http.StatusForbidden, Message: "unauthorized"}))
	// Message/code text is not a classification signal.
	require.False(t, IsAuthenticationError(&APIError{Status: http.StatusOK, Code: "auth_expired", Message: "not logged in"}))
	require.False(t, IsAuthenticationError(&APIError{Status: http.StatusOK, Message: "login required"}))
	require.False(t, IsAuthenticationError(errors.New("unauthorized")))
	require.False(t, IsAuthenticationError(nil))
}
