package lumina

import (
	"encoding/json"
	"fmt"
	"strings"
)

type InferenceInput struct {
	Name         string         `json:"name"`
	InternalName string         `json:"internal_name"`
	Type         string         `json:"type"`
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
	SubTaskCount      int             `json:"sub_task_count"`
	ReqContent        struct {
		Contents []struct {
			Type      string `json:"type"`
			Multiple  bool   `json:"multiple"`
			MaxLength int    `json:"max_length"`
		} `json:"contents"`
	} `json:"req_content"`
}

type ImageServiceSchema struct {
	Inputs  []SchemaField `json:"inputs"`
	Outputs any           `json:"outputs"`
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
	Visible      bool           `json:"visible"`
	DefaultValue any            `json:"default_value"`
	Props        map[string]any `json:"props"`
	Transformer  string         `json:"transformer"`
}

type VideoSchema struct {
	ID                      string `json:"id"`
	ReqKey                  string `json:"req_key"`
	VersionID               string `json:"version_id"`
	Type                    string `json:"type"`
	TaskType                string `json:"task_type"`
	Name                    string `json:"name"`
	InferenceType           string `json:"inference_type"`
	MaxImageCount           int    `json:"max_image_count"`
	MaxPromptLength         int    `json:"max_prompt_length"`
	NeedReturnDataConfig    bool   `json:"need_return_data_config"`
	RequiredLumiResourceURI bool   `json:"required_lumi_resource_uri"`
	Schema                  struct {
		ConfigSchemas        []SchemaField `json:"config_schemas"`
		AdvanceConfigSchemas []SchemaField `json:"advance_config_schemas"`
		InputSchemas         []SchemaField `json:"input_schemas"`
	} `json:"schema"`
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
