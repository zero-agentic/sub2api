package modelark

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageInputAcceptsStringAndArray(t *testing.T) {
	for _, raw := range []string{`"https://example.com/a.png"`, `["https://example.com/a.png","https://example.com/b.png"]`} {
		var input ImageInput
		require.NoError(t, json.Unmarshal([]byte(raw), &input))
		require.NotEmpty(t, input)
	}
}

func TestImageGenerationRequestAcceptsStandardTransportOptions(t *testing.T) {
	req := &ImageGenerationRequest{
		Model:          "seedream-5-0-pro",
		Prompt:         "test",
		Size:           "3K",
		Stream:         true,
		ResponseFormat: "b64_json",
	}
	require.NoError(t, req.Validate())
	require.Equal(t, "b64_json", req.ResponseFormat)
}

func TestImageGenerationRequestRejectsUnknownSizeWithCompleteAllowedValues(t *testing.T) {
	req := &ImageGenerationRequest{Model: "seedream", Prompt: "test", Size: "5K"}
	err := req.Validate()
	parameterErr, ok := AsParameterError(err)
	require.True(t, ok)
	require.Equal(t, "size", parameterErr.Param)
	require.Contains(t, parameterErr.Message, "3K")
}

func TestUnsupportedParameterUsesModelArkInvalidParameterCode(t *testing.T) {
	err := UnsupportedParameter("stream", "streaming is unavailable")
	parameterErr, ok := AsParameterError(err)
	require.True(t, ok)
	require.Equal(t, "InvalidParameter", parameterErr.Code)
	require.Equal(t, "stream", parameterErr.Param)
}

func TestVideoGenerationRequestAccepts4K(t *testing.T) {
	duration := 5
	req := &VideoGenerationRequest{
		Model:      "seedance-2-0",
		Content:    []VideoContent{{Type: "text", Text: "A quiet lake"}},
		Resolution: "4k",
		Duration:   &duration,
	}
	require.NoError(t, req.Validate())
}

func TestStrictDecoderRejectsUnknownVideoField(t *testing.T) {
	decoder := json.NewDecoder(strings.NewReader(`{"model":"seedance-2-0","content":[{"type":"text","text":"test"}],"unknown":true}`))
	decoder.DisallowUnknownFields()
	var req VideoGenerationRequest
	require.Error(t, decoder.Decode(&req))
}

func TestVideoGenerationRequestValidatesModelArkRanges(t *testing.T) {
	invalidFrames := 30
	invalidPriority := 10
	invalidExpiry := 3599
	for _, request := range []*VideoGenerationRequest{
		{Model: "seedance", Content: []VideoContent{{Type: "text", Text: "test"}}, Frames: &invalidFrames},
		{Model: "seedance", Content: []VideoContent{{Type: "text", Text: "test"}}, Priority: &invalidPriority},
		{Model: "seedance", Content: []VideoContent{{Type: "text", Text: "test"}}, ExecutionExpiresAfter: &invalidExpiry},
	} {
		_, ok := AsParameterError(request.Validate())
		require.True(t, ok)
	}

	maxSeed := int64(4294967295)
	validFrames := 29
	request := &VideoGenerationRequest{
		Model:   "seedance",
		Content: []VideoContent{{Type: "text", Text: "test"}},
		Frames:  &validFrames,
		Seed:    &maxSeed,
	}
	require.NoError(t, request.Validate())
}

func TestVideoGenerationRequestCountsSafetyIdentifierCharacters(t *testing.T) {
	req := &VideoGenerationRequest{
		Model:            "seedance",
		Content:          []VideoContent{{Type: "text", Text: "test"}},
		SafetyIdentifier: strings.Repeat("\u7528", 64),
	}
	require.NoError(t, req.Validate())

	req.SafetyIdentifier += "\u6237"
	parameterErr, ok := AsParameterError(req.Validate())
	require.True(t, ok)
	require.Equal(t, "safety_identifier", parameterErr.Param)
}

func TestMissingRequiredFieldUsesModelArkMissingParameterCode(t *testing.T) {
	err := (&ImageGenerationRequest{Prompt: "test"}).Validate()
	parameterErr, ok := AsParameterError(err)
	require.True(t, ok)
	require.Equal(t, "MissingParameter", parameterErr.Code)
	require.Equal(t, "model", parameterErr.Param)
}
