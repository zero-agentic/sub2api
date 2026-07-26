package modelark

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"
)

var dimensionsPattern = regexp.MustCompile(`^[1-9][0-9]{2,4}x[1-9][0-9]{2,4}$`)

type ParameterError struct {
	Status  int
	Code    string
	Param   string
	Message string
}

func (e *ParameterError) Error() string {
	if e == nil {
		return "invalid parameter"
	}
	return e.Message
}

func InvalidParameter(param, message string) error {
	return &ParameterError{Status: http.StatusBadRequest, Code: "InvalidParameter", Param: param, Message: message}
}

func MissingParameter(param, message string) error {
	return &ParameterError{Status: http.StatusBadRequest, Code: "MissingParameter", Param: param, Message: message}
}

func UnsupportedParameter(param, message string) error {
	return InvalidParameter(param, message)
}

func ModelNotFound(message string) error {
	return &ParameterError{
		Status:  http.StatusNotFound,
		Code:    "InvalidEndpointOrModel.NotFound",
		Param:   "model",
		Message: message,
	}
}

func AsParameterError(err error) (*ParameterError, bool) {
	var target *ParameterError
	ok := errors.As(err, &target)
	return target, ok
}

func (r *ImageGenerationRequest) Validate() error {
	if r == nil {
		return InvalidParameter("body", "request body is required")
	}
	r.Model = strings.TrimSpace(r.Model)
	r.Prompt = strings.TrimSpace(r.Prompt)
	r.Size = strings.TrimSpace(r.Size)
	r.SequentialImageGeneration = strings.ToLower(strings.TrimSpace(r.SequentialImageGeneration))
	if r.Model == "" {
		return MissingParameter("model", "model is required")
	}
	if r.Prompt == "" {
		return MissingParameter("prompt", "prompt is required")
	}
	if len(r.Image) > 14 {
		return InvalidParameter("image", "image must contain no more than 14 items")
	}
	for index := range r.Image {
		r.Image[index] = strings.TrimSpace(r.Image[index])
		if r.Image[index] == "" {
			return InvalidParameter("image", "image entries must not be empty")
		}
	}
	if r.Size != "" && !validImageSize(r.Size) {
		return InvalidParameter("size", "size must be 1K, 2K, 3K, 4K, adaptive, or WIDTHxHEIGHT")
	}
	if r.Seed != nil && (*r.Seed < -1 || *r.Seed > 2147483647) {
		return InvalidParameter("seed", "seed must be between -1 and 2147483647")
	}
	switch r.SequentialImageGeneration {
	case "", "disabled", "auto":
	default:
		return InvalidParameter("sequential_image_generation", "sequential_image_generation must be disabled or auto")
	}
	if r.SequentialImageGenerationOptions != nil {
		if r.SequentialImageGeneration != "auto" {
			return InvalidParameter("sequential_image_generation_options", "sequential_image_generation_options requires sequential_image_generation=auto")
		}
		if r.SequentialImageGenerationOptions.MaxImages < 1 || r.SequentialImageGenerationOptions.MaxImages > 15 {
			return InvalidParameter("sequential_image_generation_options.max_images", "max_images must be between 1 and 15")
		}
	}
	if r.GuidanceScale != nil && (*r.GuidanceScale < 1 || *r.GuidanceScale > 10) {
		return InvalidParameter("guidance_scale", "guidance_scale must be between 1 and 10")
	}
	r.OutputFormat = strings.ToLower(strings.TrimSpace(r.OutputFormat))
	switch r.OutputFormat {
	case "", "png", "jpeg":
	default:
		return InvalidParameter("output_format", "output_format must be png or jpeg")
	}
	r.ResponseFormat = strings.ToLower(strings.TrimSpace(r.ResponseFormat))
	switch r.ResponseFormat {
	case "", "url", "b64_json":
	default:
		return InvalidParameter("response_format", "response_format must be url or b64_json")
	}
	if r.OptimizePromptOptions != nil {
		r.OptimizePromptOptions.Mode = strings.ToLower(strings.TrimSpace(r.OptimizePromptOptions.Mode))
		switch r.OptimizePromptOptions.Mode {
		case "", "standard", "fast":
		default:
			return InvalidParameter("optimize_prompt_options.mode", "mode must be standard or fast")
		}
	}
	return nil
}

func (r *VideoGenerationRequest) Validate() error {
	if r == nil {
		return InvalidParameter("body", "request body is required")
	}
	r.Model = strings.TrimSpace(r.Model)
	r.CallbackURL = strings.TrimSpace(r.CallbackURL)
	if r.Model == "" {
		return MissingParameter("model", "model is required")
	}
	if len(r.Content) == 0 {
		return MissingParameter("content", "content is required")
	}
	textCount := 0
	mediaCount := 0
	for index := range r.Content {
		if err := r.Content[index].validate(index); err != nil {
			return err
		}
		switch r.Content[index].Type {
		case "text":
			textCount++
		default:
			mediaCount++
		}
	}
	if textCount > 1 {
		return InvalidParameter("content", "content must contain at most one text item")
	}
	if textCount == 0 && mediaCount == 0 {
		return InvalidParameter("content", "content must contain text or media")
	}
	r.Resolution = strings.ToLower(strings.TrimSpace(r.Resolution))
	switch r.Resolution {
	case "", "480p", "720p", "1080p", "4k":
	default:
		return InvalidParameter("resolution", "resolution must be 480p, 720p, 1080p, or 4k")
	}
	r.Ratio = strings.ToLower(strings.TrimSpace(r.Ratio))
	switch r.Ratio {
	case "", "adaptive", "16:9", "4:3", "1:1", "3:4", "9:16", "21:9":
	default:
		return InvalidParameter("ratio", "ratio is not supported")
	}
	if r.Duration != nil && (*r.Duration != -1 && (*r.Duration < 2 || *r.Duration > 15)) {
		return InvalidParameter("duration", "duration must be -1 or between 2 and 15 seconds")
	}
	if r.Frames != nil && (*r.Frames < 29 || *r.Frames > 289 || (*r.Frames-25)%4 != 0) {
		return InvalidParameter("frames", "frames must be between 29 and 289 and match 25 + 4n")
	}
	if r.Seed != nil && (*r.Seed < -1 || *r.Seed > 4294967295) {
		return InvalidParameter("seed", "seed must be between -1 and 4294967295")
	}
	r.ServiceTier = strings.ToLower(strings.TrimSpace(r.ServiceTier))
	switch r.ServiceTier {
	case "", "default", "flex":
	default:
		return InvalidParameter("service_tier", "service_tier must be default or flex")
	}
	if r.ExecutionExpiresAfter != nil && (*r.ExecutionExpiresAfter < 3600 || *r.ExecutionExpiresAfter > 259200) {
		return InvalidParameter("execution_expires_after", "execution_expires_after must be between 3600 and 259200")
	}
	r.SafetyIdentifier = strings.TrimSpace(r.SafetyIdentifier)
	if utf8.RuneCountInString(r.SafetyIdentifier) > 64 {
		return InvalidParameter("safety_identifier", "safety_identifier must not exceed 64 characters")
	}
	if r.Priority != nil && (*r.Priority < 0 || *r.Priority > 9) {
		return InvalidParameter("priority", "priority must be between 0 and 9")
	}
	return nil
}

func (c *VideoContent) validate(index int) error {
	param := fmt.Sprintf("content[%d]", index)
	c.Type = strings.TrimSpace(c.Type)
	c.Role = strings.TrimSpace(c.Role)
	switch c.Type {
	case "text":
		c.Text = strings.TrimSpace(c.Text)
		if c.Text == "" {
			return MissingParameter(param+".text", "text is required")
		}
		if c.ImageURL != nil || c.VideoURL != nil || c.AudioURL != nil || c.DraftTask != nil || c.Role != "" {
			return InvalidParameter(param, "text content contains fields for another content type")
		}
	case "image_url":
		if c.ImageURL == nil || strings.TrimSpace(c.ImageURL.URL) == "" {
			return MissingParameter(param+".image_url.url", "image URL is required")
		}
		c.ImageURL.URL = strings.TrimSpace(c.ImageURL.URL)
		if c.Text != "" || c.VideoURL != nil || c.AudioURL != nil || c.DraftTask != nil {
			return InvalidParameter(param, "image content contains fields for another content type")
		}
		switch c.Role {
		case "", "first_frame", "last_frame", "reference_image":
		default:
			return InvalidParameter(param+".role", "image role is not supported")
		}
	case "video_url":
		if c.VideoURL == nil || strings.TrimSpace(c.VideoURL.URL) == "" {
			return MissingParameter(param+".video_url.url", "video URL is required")
		}
		c.VideoURL.URL = strings.TrimSpace(c.VideoURL.URL)
		if c.Text != "" || c.ImageURL != nil || c.AudioURL != nil || c.DraftTask != nil {
			return InvalidParameter(param, "video content contains fields for another content type")
		}
		if c.Role != "" && c.Role != "reference_video" {
			return InvalidParameter(param+".role", "video role must be reference_video")
		}
	case "audio_url":
		if c.AudioURL == nil || strings.TrimSpace(c.AudioURL.URL) == "" {
			return MissingParameter(param+".audio_url.url", "audio URL is required")
		}
		c.AudioURL.URL = strings.TrimSpace(c.AudioURL.URL)
		if c.Text != "" || c.ImageURL != nil || c.VideoURL != nil || c.DraftTask != nil {
			return InvalidParameter(param, "audio content contains fields for another content type")
		}
		if c.Role != "" && c.Role != "reference_audio" {
			return InvalidParameter(param+".role", "audio role must be reference_audio")
		}
	case "draft_task":
		if c.DraftTask == nil || strings.TrimSpace(c.DraftTask.ID) == "" {
			return MissingParameter(param+".draft_task.id", "draft task ID is required")
		}
		c.DraftTask.ID = strings.TrimSpace(c.DraftTask.ID)
		if c.Text != "" || c.ImageURL != nil || c.VideoURL != nil || c.AudioURL != nil || c.Role != "" {
			return InvalidParameter(param, "draft task content contains fields for another content type")
		}
	default:
		return InvalidParameter(param+".type", "content type must be text, image_url, video_url, audio_url, or draft_task")
	}
	return nil
}

func validImageSize(value string) bool {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "1K", "2K", "3K", "4K", "ADAPTIVE":
		return true
	default:
		return dimensionsPattern.MatchString(strings.ToLower(strings.TrimSpace(value)))
	}
}
