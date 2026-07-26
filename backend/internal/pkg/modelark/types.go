package modelark

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type ImageInput []string

func (i *ImageInput) UnmarshalJSON(data []byte) error {
	if i == nil {
		return fmt.Errorf("image input is nil")
	}
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) || len(data) == 0 {
		*i = nil
		return nil
	}
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*i = []string{single}
		return nil
	}
	var multiple []string
	if err := json.Unmarshal(data, &multiple); err != nil {
		return fmt.Errorf("image must be a string or an array of strings")
	}
	*i = multiple
	return nil
}

type SequentialImageGenerationOptions struct {
	MaxImages int `json:"max_images"`
}

type OptimizePromptOptions struct {
	Mode string `json:"mode"`
}

type ImageGenerationRequest struct {
	Model                            string                            `json:"model"`
	Prompt                           string                            `json:"prompt"`
	Image                            ImageInput                        `json:"image,omitempty"`
	Size                             string                            `json:"size,omitempty"`
	Seed                             *int64                            `json:"seed,omitempty"`
	SequentialImageGeneration        string                            `json:"sequential_image_generation,omitempty"`
	SequentialImageGenerationOptions *SequentialImageGenerationOptions `json:"sequential_image_generation_options,omitempty"`
	Stream                           bool                              `json:"stream,omitempty"`
	GuidanceScale                    *float64                          `json:"guidance_scale,omitempty"`
	OutputFormat                     string                            `json:"output_format,omitempty"`
	ResponseFormat                   string                            `json:"response_format,omitempty"`
	Watermark                        *bool                             `json:"watermark,omitempty"`
	OptimizePromptOptions            *OptimizePromptOptions            `json:"optimize_prompt_options,omitempty"`
}

type ImageGenerationData struct {
	URL          string                `json:"url,omitempty"`
	B64JSON      string                `json:"b64_json,omitempty"`
	OutputFormat string                `json:"output_format,omitempty"`
	Size         string                `json:"size,omitempty"`
	Error        *ImageGenerationError `json:"error,omitempty"`
}

type ImageGenerationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ImageGenerationUsage struct {
	GeneratedImages int `json:"generated_images"`
	InputImages     int `json:"input_images,omitempty"`
	OutputTokens    int `json:"output_tokens,omitempty"`
	TotalTokens     int `json:"total_tokens,omitempty"`
}

type ImageGenerationResponse struct {
	Model   string                `json:"model"`
	Created int64                 `json:"created"`
	Data    []ImageGenerationData `json:"data"`
	Usage   ImageGenerationUsage  `json:"usage"`
	Error   *ImageGenerationError `json:"error,omitempty"`
}

type URLContent struct {
	URL string `json:"url"`
}

type DraftTaskContent struct {
	ID string `json:"id"`
}

type VideoContent struct {
	Type      string            `json:"type"`
	Text      string            `json:"text,omitempty"`
	ImageURL  *URLContent       `json:"image_url,omitempty"`
	VideoURL  *URLContent       `json:"video_url,omitempty"`
	AudioURL  *URLContent       `json:"audio_url,omitempty"`
	DraftTask *DraftTaskContent `json:"draft_task,omitempty"`
	Role      string            `json:"role,omitempty"`
}

type VideoGenerationRequest struct {
	Model                 string         `json:"model"`
	Content               []VideoContent `json:"content"`
	CallbackURL           string         `json:"callback_url,omitempty"`
	ReturnLastFrame       *bool          `json:"return_last_frame,omitempty"`
	ServiceTier           string         `json:"service_tier,omitempty"`
	ExecutionExpiresAfter *int           `json:"execution_expires_after,omitempty"`
	GenerateAudio         *bool          `json:"generate_audio,omitempty"`
	Draft                 *bool          `json:"draft,omitempty"`
	SafetyIdentifier      string         `json:"safety_identifier,omitempty"`
	Priority              *int           `json:"priority,omitempty"`
	Resolution            string         `json:"resolution,omitempty"`
	Ratio                 string         `json:"ratio,omitempty"`
	Duration              *int           `json:"duration,omitempty"`
	Frames                *int           `json:"frames,omitempty"`
	Seed                  *int64         `json:"seed,omitempty"`
	CameraFixed           *bool          `json:"camera_fixed,omitempty"`
	Watermark             *bool          `json:"watermark,omitempty"`
}

type VideoTaskError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type VideoTaskContent struct {
	VideoURL     string `json:"video_url,omitempty"`
	LastFrameURL string `json:"last_frame_url,omitempty"`
}

type VideoTask struct {
	ID                    string            `json:"id"`
	Model                 string            `json:"model"`
	Status                string            `json:"status"`
	Error                 *VideoTaskError   `json:"error"`
	CreatedAt             int64             `json:"created_at"`
	UpdatedAt             int64             `json:"updated_at"`
	Content               *VideoTaskContent `json:"content,omitempty"`
	Seed                  *int64            `json:"seed,omitempty"`
	Resolution            string            `json:"resolution,omitempty"`
	Ratio                 string            `json:"ratio,omitempty"`
	Duration              *int              `json:"duration,omitempty"`
	Frames                *int              `json:"frames,omitempty"`
	FramesPerSecond       *int              `json:"framespersecond,omitempty"`
	GenerateAudio         *bool             `json:"generate_audio,omitempty"`
	SafetyIdentifier      string            `json:"safety_identifier,omitempty"`
	Priority              *int              `json:"priority,omitempty"`
	Draft                 *bool             `json:"draft,omitempty"`
	DraftTaskID           string            `json:"draft_task_id,omitempty"`
	ServiceTier           string            `json:"service_tier,omitempty"`
	ExecutionExpiresAfter *int              `json:"execution_expires_after,omitempty"`
}

type CreateVideoTaskResponse struct {
	ID string `json:"id"`
}

type ListVideoTasksResponse struct {
	Items []*VideoTask `json:"items"`
	Total int          `json:"total"`
}

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Param   string `json:"param"`
	Type    string `json:"type"`
}

type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}
