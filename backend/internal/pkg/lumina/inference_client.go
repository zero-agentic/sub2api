package lumina

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

var videoInferenceTypes = []string{"x2v", "flf", "i2v", "t2i2v", "edit", "motion", "ev", "r2v"}

func (c *Client) ListImageServices(ctx context.Context, pageNum, pageSize int) ([]ImageService, error) {
	if pageNum <= 0 {
		pageNum = 1
	}
	if pageSize <= 0 || pageSize > 500 {
		pageSize = 100
	}
	endpoint := LuminaAPIBase + "/inference/v2/ai_service/list_ai_services?page_num=" + strconv.Itoa(pageNum) + "&page_size=" + strconv.Itoa(pageSize)
	var raw json.RawMessage
	if err := c.DoJSON(ctx, http.MethodPost, endpoint, nil, &raw); err != nil {
		return nil, err
	}
	var result struct {
		List       []ImageService `json:"list"`
		Items      []ImageService `json:"items"`
		AIServices []ImageService `json:"ai_services"`
		Services   []ImageService `json:"services"`
	}
	if err := decodeRawOrEnvelope(raw, &result); err != nil {
		return nil, err
	}
	switch {
	case len(result.List) > 0:
		return result.List, nil
	case len(result.Items) > 0:
		return result.Items, nil
	case len(result.Services) > 0:
		return result.Services, nil
	default:
		return result.AIServices, nil
	}
}

func (c *Client) GetImageService(ctx context.Context, id string) (*ImageService, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("lumina image service ID is required")
	}
	endpoint := LuminaAPIBase + "/inference/v2/ai_service/get_ai_service_by_id?id=" + url.QueryEscape(id)
	var raw json.RawMessage
	if err := c.DoJSON(ctx, http.MethodGet, endpoint, nil, &raw); err != nil {
		return nil, err
	}
	var result ImageService
	if err := decodeRawOrEnvelope(raw, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) ListVideoSchemas(ctx context.Context) ([]VideoSchemaBucket, error) {
	items := make([]map[string]string, 0, len(videoInferenceTypes))
	for _, inferenceType := range videoInferenceTypes {
		items = append(items, map[string]string{"type": inferenceType})
	}
	var raw json.RawMessage
	if err := c.DoJSON(ctx, http.MethodPost, LuminaAPIBase+"/inference/get_schema_list", map[string]any{"items": items}, &raw); err != nil {
		return nil, err
	}
	var result []VideoSchemaBucket
	if err := decodeRawOrEnvelope(raw, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) CreateImageTask(ctx context.Context, request ImageCreateTaskRequest) (*CreateTaskResponse, error) {
	var raw json.RawMessage
	if err := c.DoJSON(ctx, http.MethodPost, LuminaAPIBase+"/inference/v2/create_task", request, &raw); err != nil {
		return nil, err
	}
	var result CreateTaskResponse
	if err := decodeRawOrEnvelope(raw, &result); err != nil {
		return nil, err
	}
	if result.TaskID() == "" {
		return nil, fmt.Errorf("lumina image create response has no task ID")
	}
	return &result, nil
}

func (c *Client) CreateVideoTask(ctx context.Context, request VideoCreateTaskRequest) (*CreateTaskResponse, error) {
	if request.BAVersion == 0 {
		request.BAVersion = 2
	}
	var raw json.RawMessage
	if err := c.DoJSON(ctx, http.MethodPost, LuminaAPIBase+"/inference/create_task", request, &raw); err != nil {
		return nil, err
	}
	var result CreateTaskResponse
	if err := decodeRawOrEnvelope(raw, &result); err != nil {
		return nil, err
	}
	if result.TaskID() == "" {
		return nil, fmt.Errorf("lumina video create response has no task ID")
	}
	return &result, nil
}

func (c *Client) QueryTask(ctx context.Context, taskID string) (*Task, error) {
	var raw json.RawMessage
	if err := c.DoJSON(ctx, http.MethodPost, LuminaAPIBase+"/inference/task/query_task", map[string]string{"task_id": taskID}, &raw); err != nil {
		return nil, err
	}
	var task Task
	if err := decodeRawOrEnvelope(raw, &task); err != nil {
		return nil, err
	}
	return &task, nil
}

func (c *Client) QueryTaskList(ctx context.Context, request TaskListRequest) (*TaskListResponse, error) {
	var raw json.RawMessage
	if err := c.DoJSON(ctx, http.MethodPost, LuminaAPIBase+"/inference/task/query_task_list", request, &raw); err != nil {
		return nil, err
	}
	var result TaskListResponse
	if err := decodeRawOrEnvelope(raw, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) StopTask(ctx context.Context, taskID string) error {
	return c.taskMutation(ctx, "/inference/task/stop_task", taskID)
}

func (c *Client) RemoveTask(ctx context.Context, taskID string) error {
	return c.taskMutation(ctx, "/inference/task/remove_task", taskID)
}

func (c *Client) taskMutation(ctx context.Context, path, taskID string) error {
	var raw json.RawMessage
	if err := c.DoJSON(ctx, http.MethodPost, LuminaAPIBase+path, map[string]string{"task_id": taskID}, &raw); err != nil {
		return err
	}
	var result map[string]any
	return decodeRawOrEnvelope(raw, &result)
}

func decodeRawOrEnvelope(raw json.RawMessage, destination any) error {
	if len(raw) == 0 {
		return nil
	}
	var envelope struct {
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decode lumina response envelope: %w", err)
	}
	if len(envelope.Code) > 0 && string(envelope.Code) != "null" {
		code := strings.Trim(string(envelope.Code), `"`)
		if code != "" && code != "0" {
			return &APIError{Status: http.StatusOK, Code: code, Message: envelope.Message}
		}
		if len(envelope.Data) > 0 && string(envelope.Data) != "null" {
			if err := json.Unmarshal(envelope.Data, destination); err != nil {
				return fmt.Errorf("decode lumina response data: %w", err)
			}
			return nil
		}
	}
	if err := json.Unmarshal(raw, destination); err != nil {
		return fmt.Errorf("decode lumina response: %w", err)
	}
	return nil
}

// IsAuthenticationError reports whether err carries the structured signal of an
// expired BytePlus console session. Only the HTTP 401 status is trusted: the
// lumi-api envelope codes are undocumented, and HTTP 403 is emitted by the
// Cloudflare WAF for risk-control blocks that say nothing about session
// validity, so neither message text nor 403 may drive re-login or failover.
func IsAuthenticationError(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr == nil {
		return false
	}
	return apiErr.Status == http.StatusUnauthorized
}
