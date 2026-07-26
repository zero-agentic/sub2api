package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAbortWithErrorUsesModelArkErrorEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v3/test", ModelArkErrorResponses(), func(c *gin.Context) {
		AbortWithError(c, http.StatusUnauthorized, "INVALID_API_KEY", "Invalid API key")
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v3/test", nil)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusUnauthorized, recorder.Code)
	require.JSONEq(t, `{
		"error": {
			"code": "AuthenticationError",
			"message": "Invalid API key",
			"param": "",
			"type": "Unauthorized"
		}
	}`, recorder.Body.String())
}

func TestAbortWithErrorMapsModelArkQuotaErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v3/test", ModelArkErrorResponses(), func(c *gin.Context) {
		AbortWithError(c, http.StatusForbidden, "INSUFFICIENT_BALANCE", "Insufficient balance")
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v3/test", nil)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusTooManyRequests, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"code":"QuotaExceeded"`)
	require.Contains(t, recorder.Body.String(), `"type":"TooManyRequests"`)
}

func TestAbortWithErrorKeepsStandardEnvelopeWithoutModelArkMarker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	// Same path shape as the ModelArk group but without the marker middleware:
	// the error style must come from route registration, not the URL prefix.
	router.GET("/api/v3/test", func(c *gin.Context) {
		AbortWithError(c, http.StatusUnauthorized, "INVALID_API_KEY", "Invalid API key")
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v3/test", nil)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusUnauthorized, recorder.Code)
	require.JSONEq(t, `{"code":"INVALID_API_KEY","message":"Invalid API key"}`, recorder.Body.String())
}
