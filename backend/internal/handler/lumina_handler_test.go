package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/lumina"
	"github.com/Wei-Shaw/sub2api/internal/pkg/modelark"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestDecodeStrictModelArkJSONRejectsUnknownAndTrailingValues(t *testing.T) {
	var request modelark.VideoGenerationRequest
	require.Error(t, decodeStrictModelArkJSON([]byte(`{"model":"seedance","content":[],"unknown":true}`), &request))
	require.Error(t, decodeStrictModelArkJSON([]byte(`{"model":"seedance","content":[]} {}`), &request))
}

func TestShouldTryAnotherLuminaAccountClassifiesAccountLocalFailures(t *testing.T) {
	require.True(t, shouldTryAnotherLuminaAccount(&lumina.AuthCredentialError{Code: "InvalidPassword"}))
	require.True(t, shouldTryAnotherLuminaAccount(&lumina.AuthChallengeError{Operation: "WebAuthn"}))
	require.True(t, shouldTryAnotherLuminaAccount(service.ErrLuminaSessionUnavailable))
	require.True(t, shouldTryAnotherLuminaAccount(service.ErrLuminaAccountIdentityChanged))
	require.True(t, shouldTryAnotherLuminaAccount(modelark.InvalidParameter("model", "not available")))
	require.False(t, shouldTryAnotherLuminaAccount(modelark.InvalidParameter("resolution", "not supported")))
	require.False(t, shouldTryAnotherLuminaAccount(errors.New("database unavailable")))
}

func TestCompactQueryValuesPreservesModelArkTaskIDOrder(t *testing.T) {
	require.Equal(t, []string{"task-a", "task-b"}, compactQueryValues([]string{" task-a ", "", "task-b"}))
}

func TestModelArkWriteErrorIncludesOfficialEnvelopeFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)

	modelArkWriteError(context, modelark.UnsupportedParameter("stream", "streaming is unavailable"))

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.JSONEq(t, `{
		"error": {
			"code": "InvalidParameter",
			"message": "streaming is unavailable",
			"param": "stream",
			"type": "BadRequest"
		}
	}`, recorder.Body.String())
}

func TestModelArkWriteErrorMapsOversizedBodyToOfficialBadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)

	modelArkWriteError(context, &http.MaxBytesError{Limit: 1024})

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.JSONEq(t, `{
		"error": {
			"code": "InvalidParameter",
			"message": "Request body too large, limit is 1024B",
			"param": "body",
			"type": "BadRequest"
		}
	}`, recorder.Body.String())
}

func TestModelArkListStatusMatchesStoredFilterValues(t *testing.T) {
	require.True(t, validModelArkTaskStatus(service.LuminaTaskStatusSucceeded))
	// The service persists expired tasks (upstream timeout, reconciler stale
	// sweep), so users must be able to filter by that status.
	require.True(t, validModelArkTaskStatus(service.LuminaTaskStatusExpired))
	require.False(t, validModelArkTaskStatus("deleted"))
}

func TestModelArkListAcceptsOfficialMaximumPageSize(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/v3/contents/generations/tasks?page_size=500", nil)

	pageSize, err := parseBoundedQueryInt(context, "page_size", 1, 500, 20)
	require.NoError(t, err)
	require.Equal(t, 500, pageSize)
}
