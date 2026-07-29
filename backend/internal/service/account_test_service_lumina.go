package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/lumina"
	"github.com/Wei-Shaw/sub2api/internal/pkg/modelark"
	"github.com/gin-gonic/gin"
)

// defaultLuminaMediaTestPrompt drives both catalog halves: it reads equally well
// as a still image and as a short shot, so one default covers image and video
// probes without asking the operator to phrase two prompts.
const defaultLuminaMediaTestPrompt = "A cute orange cat astronaut floating in a pastel nebula."

// luminaProbeVideoMIMEType is what Lumina renders every video output as.
const luminaProbeVideoMIMEType = "video/mp4"

// luminaAccountProbe is the slice of the Lumina gateway that account testing
// depends on. Declaring it at the consumer keeps the SSE flow unit-testable
// without a live BytePlus console session.
type luminaAccountProbe interface {
	CatalogForAccount(ctx context.Context, account *Account, allowInactive bool) ([]lumina.ImageService, []lumina.VideoSchemaBucket, error)
	ProbeMediaModel(ctx context.Context, account *Account, probe LuminaMediaProbe) (*LuminaMediaProbeResult, error)
}

// testLuminaAccountConnection routes a Lumina account test.
//
// Without a selected model the test stays a pure credential check — catalog
// discovery, no generation and therefore no upstream cost. That is the path
// scheduled background health checks take. With a model selected the test runs
// the real generation the operator asked for, because for a media platform
// "the session is valid" says nothing about whether the model can be driven.
func (s *AccountTestService) testLuminaAccountConnection(c *gin.Context, account *Account, modelID, prompt string) error {
	if s.luminaGateway == nil {
		return s.sendErrorAndEnd(c, "Lumina gateway is not configured")
	}

	// Video probes stream progress for minutes, so the response must not be
	// buffered by an intermediary.
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()

	if strings.TrimSpace(modelID) == "" {
		return s.testLuminaCatalogDiscovery(c, account)
	}
	return s.testLuminaMediaGeneration(c, account, modelID, prompt)
}

// testLuminaCatalogDiscovery verifies the cookie session by listing the catalog.
func (s *AccountTestService) testLuminaCatalogDiscovery(c *gin.Context, account *Account) error {
	s.sendEvent(c, TestEvent{Type: "test_start", Model: "lumina"})
	images, videoSchemas, err := s.luminaGateway.CatalogForAccount(c.Request.Context(), account, true)
	if err != nil {
		return s.sendErrorAndEnd(c, safeLuminaAccountTestError(err))
	}
	videoCount := 0
	for _, bucket := range videoSchemas {
		videoCount += len(bucket.Items)
	}
	s.sendEvent(c, TestEvent{
		Type: "content",
		Text: fmt.Sprintf("Lumina authenticated; discovered %d image services and %d video schemas", len(images), videoCount),
	})
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}

// testLuminaMediaGeneration runs one real generation on the selected model. The
// gateway decides from the live catalog whether that model is text-to-image or
// text-to-video; this method only translates the outcome into SSE events.
func (s *AccountTestService) testLuminaMediaGeneration(c *gin.Context, account *Account, modelID, prompt string) error {
	testPrompt := strings.TrimSpace(prompt)
	if testPrompt == "" {
		testPrompt = defaultLuminaMediaTestPrompt
	}

	s.sendEvent(c, TestEvent{Type: "test_start", Model: modelID})
	result, err := s.luminaGateway.ProbeMediaModel(c.Request.Context(), account, LuminaMediaProbe{
		Model:      modelID,
		Prompt:     testPrompt,
		OnProgress: s.luminaProgressWriter(c),
	})
	if err != nil {
		return s.sendErrorAndEnd(c, safeLuminaAccountTestError(err))
	}

	// Images travel inline as data URLs, matching the OpenAI and Gemini image
	// tests; videos travel as the upstream URL the public gateway already hands
	// to API clients, because a base64 video would be tens of megabytes of SSE.
	for _, image := range result.Images {
		s.sendEvent(c, TestEvent{
			Type:     "image",
			ImageURL: "data:" + image.MimeType + ";base64," + image.Base64,
			MimeType: image.MimeType,
		})
	}
	if result.VideoURL != "" {
		s.sendEvent(c, TestEvent{Type: "video", VideoURL: result.VideoURL, MimeType: luminaProbeVideoMIMEType})
	}
	if result.Pending {
		s.sendEvent(c, TestEvent{Type: "content", Text: fmt.Sprintf(
			"Video task %s was accepted and is still generating upstream after %s; the account and model work.",
			result.TaskID, luminaProbeVideoTimeout,
		)})
	}
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}

// luminaProgressWriter turns repeated probe progress into a stream that is both
// readable and connection-safe: a new line becomes a status event, an unchanged
// one becomes an SSE comment. Without those comments a video probe would write
// nothing for minutes and any default reverse-proxy read timeout would drop the
// response while the generation was still running.
func (s *AccountTestService) luminaProgressWriter(c *gin.Context) func(string) {
	lastText := ""
	return func(text string) {
		if text == lastText {
			s.sendKeepAlive(c)
			return
		}
		lastText = text
		s.sendEvent(c, TestEvent{Type: "status", Text: text})
	}
}

// fetchLuminaCatalog delegates to the gateway's authenticated catalog flow so
// the re-login retry logic lives in exactly one place.
func (s *AccountTestService) fetchLuminaCatalog(
	ctx context.Context,
	account *Account,
	allowInactive bool,
) ([]lumina.ImageService, []lumina.VideoSchemaBucket, error) {
	if s == nil || s.luminaGateway == nil {
		return nil, nil, ErrLuminaSessionUnavailable
	}
	return s.luminaGateway.CatalogForAccount(ctx, account, allowInactive)
}

// safeLuminaAccountTestError turns a probe failure into operator-facing text.
// Free-form upstream messages stay hidden — they carry console internals — but
// our own parameter errors and the short upstream error codes are surfaced,
// because "test failed" with no reason is not actionable.
func safeLuminaAccountTestError(err error) string {
	if parameterErr, ok := modelark.AsParameterError(err); ok {
		return parameterErr.Message
	}
	switch {
	case errors.Is(err, ErrLuminaAccountIdentityChanged):
		return "Lumina account identity changed; update the account credentials manually"
	case lumina.IsInteractiveAuthError(err):
		return "Lumina requires interactive verification; complete it in the browser and update the cookie jar"
	case lumina.IsCredentialAuthError(err):
		return "Lumina email or password authentication failed"
	}
	var apiErr *lumina.APIError
	if errors.As(err, &apiErr) && apiErr != nil && strings.TrimSpace(apiErr.Code) != "" {
		return "Lumina connection test failed (code " + strings.TrimSpace(apiErr.Code) + ")"
	}
	return "Lumina connection test failed"
}
