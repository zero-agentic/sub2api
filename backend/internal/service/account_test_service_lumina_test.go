package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/lumina"
	"github.com/Wei-Shaw/sub2api/internal/pkg/modelark"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// fakeLuminaAccountProbe stands in for the gateway so the SSE flow can be
// exercised without a BytePlus console session.
type fakeLuminaAccountProbe struct {
	images  []lumina.ImageService
	buckets []lumina.VideoSchemaBucket

	probe  LuminaMediaProbe
	probes int
	result *LuminaMediaProbeResult
	err    error
	// repeatedPolls emits that many unchanged progress reports, mirroring a task
	// that stays in one status across several polls.
	repeatedPolls int
}

func (f *fakeLuminaAccountProbe) CatalogForAccount(context.Context, *Account, bool) ([]lumina.ImageService, []lumina.VideoSchemaBucket, error) {
	return f.images, f.buckets, f.err
}

func (f *fakeLuminaAccountProbe) ProbeMediaModel(_ context.Context, _ *Account, probe LuminaMediaProbe) (*LuminaMediaProbeResult, error) {
	f.probes++
	f.probe = probe
	if f.err != nil {
		return nil, f.err
	}
	if probe.OnProgress != nil {
		probe.OnProgress("Resolved " + string(f.result.Kind) + " model \"" + f.result.Model + "\"")
		// The gateway reports every poll, so the same status repeats while the
		// task keeps running.
		for range f.repeatedPolls {
			probe.OnProgress("Video task running")
		}
	}
	return f.result, nil
}

func newLuminaTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/1/test", nil)
	return c, recorder
}

func luminaCookieAccount() *Account {
	return &Account{ID: 11, Name: "lumina", Platform: PlatformLumina, Type: AccountTypeCookie, Credentials: map[string]any{}}
}

// Selecting an image model must run a real generation and stream the result back
// as an inline data URL, exactly like the OpenAI and Gemini image tests.
func TestTestLuminaAccountConnectionStreamsGeneratedImage(t *testing.T) {
	c, recorder := newLuminaTestContext()
	probe := &fakeLuminaAccountProbe{result: &LuminaMediaProbeResult{
		Kind:   LuminaMediaKindImage,
		Model:  "GPT Image 2 （Beta）",
		Images: []LuminaProbeImage{{Base64: "aGVsbG8=", MimeType: "image/png"}},
	}}
	svc := &AccountTestService{luminaGateway: probe}

	require.NoError(t, svc.testLuminaAccountConnection(c, luminaCookieAccount(), "gpt-image-2", "draw a cat"))

	body := recorder.Body.String()
	require.Equal(t, 1, probe.probes)
	require.Equal(t, "draw a cat", probe.probe.Prompt)
	require.Contains(t, body, `"model":"gpt-image-2"`)
	require.Contains(t, body, `GPT Image 2`)
	require.Contains(t, body, `"image_url":"data:image/png;base64,aGVsbG8="`)
	require.Contains(t, body, `"success":true`)
	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
}

// A video model returns a playable URL rather than inline bytes, and its slow
// progress has to reach the operator while the task runs.
func TestTestLuminaAccountConnectionStreamsGeneratedVideo(t *testing.T) {
	c, recorder := newLuminaTestContext()
	probe := &fakeLuminaAccountProbe{result: &LuminaMediaProbeResult{
		Kind:     LuminaMediaKindVideo,
		Model:    "Seedance 2.0",
		TaskID:   "74123",
		VideoURL: "https://example.com/clip.mp4",
	}}
	svc := &AccountTestService{luminaGateway: probe}

	require.NoError(t, svc.testLuminaAccountConnection(c, luminaCookieAccount(), "dreamina-seedance-2-0-260128", "a quiet lake"))

	body := recorder.Body.String()
	require.Contains(t, body, `"type":"status"`)
	require.Contains(t, body, `Resolved video model \"Seedance 2.0\"`)
	require.Contains(t, body, `"video_url":"https://example.com/clip.mp4"`)
	require.Contains(t, body, `"mime_type":"video/mp4"`)
	require.Contains(t, body, `"success":true`)
}

// A video task that outlives the probe window still proves the account works, so
// the test succeeds and reports the task the operator can follow up on.
func TestTestLuminaAccountConnectionReportsPendingVideoTaskAsSuccess(t *testing.T) {
	c, recorder := newLuminaTestContext()
	probe := &fakeLuminaAccountProbe{result: &LuminaMediaProbeResult{
		Kind:    LuminaMediaKindVideo,
		Model:   "Seedance 2.0",
		TaskID:  "74123",
		Pending: true,
	}}
	svc := &AccountTestService{luminaGateway: probe}

	require.NoError(t, svc.testLuminaAccountConnection(c, luminaCookieAccount(), "dreamina-seedance-2-0-260128", ""))

	body := recorder.Body.String()
	require.Equal(t, defaultLuminaMediaTestPrompt, probe.probe.Prompt, "an empty prompt must fall back to the default")
	require.Contains(t, body, "74123")
	require.Contains(t, body, "still generating upstream")
	require.Contains(t, body, `"success":true`)
	require.NotContains(t, body, `"type":"video"`)
}

// Without a selected model the test stays a credential check: no generation, no
// upstream cost. Scheduled background health checks rely on this path.
func TestTestLuminaAccountConnectionWithoutModelOnlyDiscoversCatalog(t *testing.T) {
	c, recorder := newLuminaTestContext()
	probe := &fakeLuminaAccountProbe{
		images:  []lumina.ImageService{{ID: "svc-1"}, {ID: "svc-2"}},
		buckets: []lumina.VideoSchemaBucket{{Type: "x2v", Items: []lumina.VideoSchema{{ID: "schema-1"}}}},
	}
	svc := &AccountTestService{luminaGateway: probe}

	require.NoError(t, svc.testLuminaAccountConnection(c, luminaCookieAccount(), "", ""))

	require.Zero(t, probe.probes, "catalog discovery must not generate media")
	require.Contains(t, recorder.Body.String(), "discovered 2 image services and 1 video schemas")
}

// A model that is neither an image service nor a text-to-video schema must fail
// with the reason, not a generic "test failed".
func TestTestLuminaAccountConnectionSurfacesModelClassificationFailure(t *testing.T) {
	c, recorder := newLuminaTestContext()
	probe := &fakeLuminaAccountProbe{err: modelark.ModelNotFound("the selected model is not a text-to-image or text-to-video model in this account's Lumina catalog")}
	svc := &AccountTestService{luminaGateway: probe}

	require.Error(t, svc.testLuminaAccountConnection(c, luminaCookieAccount(), "claude-sonnet-4-5", "hi"))
	require.Contains(t, recorder.Body.String(), "not a text-to-image or text-to-video model")
}

// A slow video task reports the same status on every poll. Repeats must not spam
// the terminal, but they still have to put bytes on the wire, or a reverse proxy
// closes the response mid-generation.
func TestTestLuminaAccountConnectionKeepsSlowVideoStreamAlive(t *testing.T) {
	c, recorder := newLuminaTestContext()
	probe := &fakeLuminaAccountProbe{
		repeatedPolls: 3,
		result: &LuminaMediaProbeResult{
			Kind:     LuminaMediaKindVideo,
			Model:    "Seedance 2.0",
			TaskID:   "74123",
			VideoURL: "https://example.com/clip.mp4",
		},
	}
	svc := &AccountTestService{luminaGateway: probe}

	require.NoError(t, svc.testLuminaAccountConnection(c, luminaCookieAccount(), "dreamina-seedance-2-0-260128", "a quiet lake"))

	body := recorder.Body.String()
	require.Equal(t, 1, strings.Count(body, `"text":"Video task running"`), "an unchanged status must be reported once")
	require.Equal(t, 2, strings.Count(body, ": keep-alive"), "each further repeat must still write bytes")
}

// Free-form upstream text stays hidden, but the short error code is reported so
// a failure is actionable.
func TestSafeLuminaAccountTestErrorHidesUpstreamTextButKeepsCode(t *testing.T) {
	message := safeLuminaAccountTestError(&lumina.APIError{
		Status:  200,
		Code:    "OutputVideoSensitiveContentDetected",
		Message: "internal console detail",
	})
	require.Contains(t, message, "OutputVideoSensitiveContentDetected")
	require.NotContains(t, message, "internal console detail")

	require.Equal(t, "Lumina connection test failed", safeLuminaAccountTestError(context.DeadlineExceeded))
}
