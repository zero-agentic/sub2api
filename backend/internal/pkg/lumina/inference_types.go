package lumina

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type InferenceInput struct {
	Name         string         `json:"name"`
	InternalName string         `json:"internal_name"`
	// Type/Props are retained for decoding upstream task snapshots, but create
	// payloads must omit them (console does). omitempty keeps an empty Type off
	// the wire when builders leave it unset.
	Type         string         `json:"type,omitempty"`
	Value        any            `json:"value"`
	Label        string         `json:"label,omitempty"`
	Alias        string         `json:"alias,omitempty"`
	Format       string         `json:"format,omitempty"`
	Props        map[string]any `json:"props,omitempty"`
	Transformer  string         `json:"transformer,omitempty"`
}

type ImageService struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	NameEN            string          `json:"name_en"`
	ReqKey            string          `json:"req_key"`
	Schema            json.RawMessage `json:"schema"`
	InferenceTypes    []string        `json:"inference_types"`
	InferencePipeline string          `json:"inference_pipeline"`
	SubTaskCount      FlexInt         `json:"sub_task_count"`
	ReqContent        struct {
		Contents []struct {
			Type      string   `json:"type"`
			Multiple  FlexBool `json:"multiple"`
			MaxLength FlexInt  `json:"max_length"`
		} `json:"contents"`
	} `json:"req_content"`
}

type ImageServiceSchema struct {
	Inputs  []SchemaField `json:"inputs"`
	Outputs any           `json:"outputs"`
}

// textToImageInferenceTypes are the Lumina inference types that can be driven by
// a prompt alone. "t2i" is the classic tag; "x2i" is the unified any-to-image
// interface newer services use, which accepts prompt-only requests too.
var textToImageInferenceTypes = []string{"t2i", "x2i"}

// SupportsTextToImage reports whether this service can be driven by a prompt
// alone — the only image capability the ModelArk-compatible gateway exposes.
//
// The declared inference types are authoritative when present, but some services
// ship `inference_types: null` (GPT Image 2 does), so capability then falls back
// to the request contract and finally to the schema shape. Dropping a service
// just because it declares nothing would hide it from model sync entirely.
func (s ImageService) SupportsTextToImage() bool {
	if len(s.InferenceTypes) > 0 {
		for _, declared := range s.InferenceTypes {
			for _, supported := range textToImageInferenceTypes {
				if strings.EqualFold(strings.TrimSpace(declared), supported) {
					return true
				}
			}
		}
		return false
	}
	if len(s.ReqContent.Contents) > 0 {
		for _, content := range s.ReqContent.Contents {
			if strings.EqualFold(strings.TrimSpace(content.Type), "text") {
				return true
			}
		}
		return false
	}
	schema, err := s.ParsedSchema()
	if err != nil {
		return false
	}
	return schemaFieldsContainPrompt(schema.Inputs)
}

func (s ImageService) ParsedSchema() (*ImageServiceSchema, error) {
	if len(s.Schema) == 0 || string(s.Schema) == "null" {
		return nil, fmt.Errorf("lumina image service %s has no schema", s.ID)
	}
	payload := s.Schema
	var encoded string
	if err := json.Unmarshal(s.Schema, &encoded); err == nil {
		payload = []byte(encoded)
	}
	var schema ImageServiceSchema
	if err := json.Unmarshal(payload, &schema); err != nil {
		return nil, fmt.Errorf("decode lumina image service schema: %w", err)
	}
	return &schema, nil
}

type SchemaField struct {
	Name         string         `json:"name"`
	Label        string         `json:"label"`
	Tips         string         `json:"tips"`
	InternalName string         `json:"internal_name"`
	Format       string         `json:"format"`
	Type         string         `json:"type"`
	DataType     string         `json:"data_type"`
	Visible      FlexBool       `json:"visible"`
	DefaultValue any            `json:"default_value"`
	Props        map[string]any `json:"props"`
	Transformer  string         `json:"transformer"`
}

type VideoSchema struct {
	ID                      string            `json:"id"`
	ReqKey                  string            `json:"req_key"`
	VersionID               string            `json:"version_id"`
	Type                    string            `json:"type"`
	TaskType                string            `json:"task_type"`
	Name                    string            `json:"name"`
	InferenceType           string            `json:"inference_type"`
	MaxImageCount           FlexInt           `json:"max_image_count"`
	MaxPromptLength         FlexInt           `json:"max_prompt_length"`
	NeedReturnDataConfig    FlexBool          `json:"need_return_data_config"`
	RequiredLumiResourceURI FlexBool          `json:"required_lumi_resource_uri"`
	Schema                  VideoSchemaFields `json:"schema"`
}

type VideoSchemaFields struct {
	ConfigSchemas        []SchemaField `json:"config_schemas"`
	AdvanceConfigSchemas []SchemaField `json:"advance_config_schemas"`
	InputSchemas         []SchemaField `json:"input_schemas"`
}

// UnmarshalJSON accepts both an inline object and a JSON-encoded string, because
// BytePlus double-encodes nested schema payloads on some endpoints (see
// ImageService.ParsedSchema) and one shape difference would otherwise fail the
// whole video catalog.
func (f *VideoSchemaFields) UnmarshalJSON(data []byte) error {
	payload := bytes.TrimSpace(data)
	if len(payload) == 0 || bytes.Equal(payload, []byte("null")) {
		return nil
	}
	var encoded string
	if err := json.Unmarshal(payload, &encoded); err == nil {
		if strings.TrimSpace(encoded) == "" {
			return nil
		}
		payload = []byte(encoded)
	}
	type plainFields VideoSchemaFields
	var fields plainFields
	if err := json.Unmarshal(payload, &fields); err != nil {
		return fmt.Errorf("decode lumina video schema fields: %w", err)
	}
	*f = VideoSchemaFields(fields)
	return nil
}

// mediaInputInternalNames are the schema inputs that require an uploaded image,
// an existing video, or a multimodal reference. A schema carrying any of them
// cannot be driven by a prompt alone.
var mediaInputInternalNames = map[string]struct{}{
	"prompt_pic":         {},
	"first_prompt_pic":   {},
	"last_prompt_pic":    {},
	"content":            {},
	"driving_video_info": {},
}

// mediaInputNames covers the same inputs by display name, for entries that omit
// internal_name.
var mediaInputNames = map[string]struct{}{"img": {}, "mm": {}}

// IsTextToVideo reports whether this schema can be driven by a prompt alone.
//
// The capability is derived from the schema rather than from task_type or the
// bucket, because both are unreliable: Seedance 2.0 marks its text-to-video
// variant task_type=t2v, while the 1.x family leaves task_type empty and only
// appears under the "t2i2v" bucket. The schema, by contrast, states exactly what
// the request must carry — which is also the precondition for our request
// builder, since it can only fill a prompt.
func (s VideoSchema) IsTextToVideo() bool {
	fields := s.AllSchemaFields()
	for _, field := range fields {
		if _, isMedia := mediaInputInternalNames[strings.TrimSpace(field.InternalName)]; isMedia {
			return false
		}
		if _, isMedia := mediaInputNames[strings.TrimSpace(field.Name)]; isMedia {
			return false
		}
	}
	return schemaFieldsContainPrompt(fields)
}

func schemaFieldsContainPrompt(fields []SchemaField) bool {
	for _, field := range fields {
		if strings.EqualFold(strings.TrimSpace(field.InternalName), "prompt") ||
			strings.EqualFold(strings.TrimSpace(field.Name), "prompt") {
			return true
		}
	}
	return false
}

func (s VideoSchema) AllSchemaFields() []SchemaField {
	fields := make([]SchemaField, 0, len(s.Schema.ConfigSchemas)+len(s.Schema.AdvanceConfigSchemas)+len(s.Schema.InputSchemas))
	fields = append(fields, s.Schema.ConfigSchemas...)
	fields = append(fields, s.Schema.AdvanceConfigSchemas...)
	fields = append(fields, s.Schema.InputSchemas...)
	return fields
}

func (s VideoSchema) SupportsValue(fieldName, value string) bool {
	value = strings.TrimSpace(value)
	for _, field := range s.AllSchemaFields() {
		if field.Name != fieldName && field.InternalName != fieldName {
			continue
		}
		enums, _ := field.Props["enums"].([]any)
		if len(enums) == 0 {
			return true
		}
		for _, item := range enums {
			entry, _ := item.(map[string]any)
			if fmt.Sprint(entry["value"]) == value {
				return true
			}
		}
		return false
	}
	return false
}

type VideoSchemaBucket struct {
	Type  string        `json:"type"`
	Items []VideoSchema `json:"items"`
}

type ImageCreateTaskRequest struct {
	Inputs          []InferenceInput     `json:"inputs"`
	InferenceConfig ImageInferenceConfig `json:"inference_config"`
	InferenceType   string               `json:"inference_type"`
	RequestSource   int                  `json:"request_source"`
	Count           int                  `json:"count"`
}

type ImageInferenceConfig struct {
	InferencePipeline string `json:"inference_pipeline"`
	InferenceID       string `json:"inference_id"`
	InferenceVerID    string `json:"inference_ver_id"`
	Name              string `json:"name"`
	ReqKey            string `json:"req_key"`
}

type VideoCreateTaskRequest struct {
	BAVersion int              `json:"ba_version"`
	Type      string           `json:"type"`
	ModelID   string           `json:"model_id"`
	Inputs    []InferenceInput `json:"inputs"`
}

type CreateTaskResponse struct {
	ID           string `json:"id"`
	ParentTaskID string `json:"parent_task_id"`
}

func (r CreateTaskResponse) TaskID() string {
	if strings.TrimSpace(r.ParentTaskID) != "" {
		return strings.TrimSpace(r.ParentTaskID)
	}
	return strings.TrimSpace(r.ID)
}

type TaskPage struct {
	PageNum    int `json:"page_num"`
	PageSize   int `json:"page_size"`
	TotalPage  int `json:"total_page"`
	TotalCount int `json:"total_count"`
}

type TaskListRequest struct {
	PageNum   int      `json:"page_num"`
	PageSize  int      `json:"page_size"`
	TaskTypes []string `json:"task_types,omitempty"`
	TaskType  string   `json:"task_type,omitempty"`
	Source    []string `json:"source,omitempty"`
}

type TaskListResponse struct {
	Page  TaskPage `json:"page"`
	Tasks []Task   `json:"tasks"`
}

type Task struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Status          string          `json:"status"`
	CreatedAt       int64           `json:"created_at"`
	UpdatedAt       int64           `json:"updated_at"`
	InferenceInfo   InferenceInfo   `json:"inference_info"`
	InferenceConfig json.RawMessage `json:"inference_config"`
	Children        []SubTask       `json:"children"`
}

type InferenceInfo struct {
	InferenceType      string `json:"inference_type"`
	InferenceID        string `json:"inference_id"`
	InferenceVersionID string `json:"inference_version_id"`
	InferencePipeline  string `json:"inference_pipeline"`
}

type SubTask struct {
	ID             string           `json:"id"`
	Status         string           `json:"status"`
	Inputs         []InferenceInput `json:"inputs"`
	Output         *TaskOutput      `json:"output"`
	MultiOutputs   []TaskOutput     `json:"multi_outputs"`
	FailReason     string           `json:"fail_reason"`
	ModelID        string           `json:"model_id"`
	ModelVersionID string           `json:"model_version_id"`
	Queue          any              `json:"queue"`
	Extra          map[string]any   `json:"extra"`
	CreatedAt      int64            `json:"created_at"`
	UpdatedAt      int64            `json:"updated_at"`
}

type TaskOutput struct {
	Name      string         `json:"name"`
	Value     string         `json:"value"`
	VideoURL  string         `json:"video_url"`
	Format    string         `json:"format"`
	Type      string         `json:"type"`
	Cover     string         `json:"cover"`
	MetaInfo  map[string]any `json:"meta_info"`
	ArtworkID string         `json:"artwork_id"`
}
