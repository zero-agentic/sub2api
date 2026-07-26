package service

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/lumina"
	"github.com/Wei-Shaw/sub2api/internal/pkg/modelark"
	"github.com/stretchr/testify/require"
)

func TestBuildLuminaVideoInputsAccepts4KOnlyWhenSchemaAdvertisesIt(t *testing.T) {
	duration := 5
	request := &modelark.VideoGenerationRequest{
		Model:      "seedance-2.0-pro",
		Content:    []modelark.VideoContent{{Type: "text", Text: "A quiet lake"}},
		Resolution: "4k",
		Ratio:      "16:9",
		Duration:   &duration,
	}
	pro := luminaVideoSchemaForResolutionTest("480p", "720p", "1080p", "4k")
	inputs, normalized, err := buildLuminaVideoInputs(request, &pro)
	require.NoError(t, err)
	require.NotEmpty(t, inputs)
	require.Equal(t, "4k", normalized.Resolution)

	mini := luminaVideoSchemaForResolutionTest("480p", "720p")
	_, _, err = buildLuminaVideoInputs(request, &mini)
	parameterErr, ok := modelark.AsParameterError(err)
	require.True(t, ok)
	require.Equal(t, "resolution", parameterErr.Param)
}

func TestResolveLuminaModelsUsesExplicitAccountMapping(t *testing.T) {
	account := &Account{Credentials: map[string]any{
		"model_mapping": map[string]any{
			"seedance-pro": "Doubao-Seedance-2.0-pro",
			"seedream-pro": "ByteDance-Seedream-5.0-pro",
		},
	}}
	imageServices := []lumina.ImageService{{ID: "image-id", ReqKey: "ByteDance-Seedream-5.0-pro"}}
	image, err := resolveLuminaImageService(account, "seedream-pro", imageServices)
	require.NoError(t, err)
	require.Equal(t, "image-id", image.ID)

	videoSchemas := []lumina.VideoSchema{{ID: "video-id", ReqKey: "Doubao-Seedance-2.0-pro", InferenceType: "x2v", TaskType: "t2v"}}
	video, err := resolveLuminaVideoSchema(account, &modelark.VideoGenerationRequest{Model: "seedance-pro"}, videoSchemas)
	require.NoError(t, err)
	require.Equal(t, "video-id", video.ID)
}

func TestResolveLuminaModelsAcceptsOfficialModelArkIDs(t *testing.T) {
	account := &Account{Credentials: map[string]any{}}
	imageServices := []lumina.ImageService{{ID: "image-id", ReqKey: "ByteDance-Seedream-5.0-pro"}}
	image, err := resolveLuminaImageService(account, "dola-seedream-5-0-pro-260628", imageServices)
	require.NoError(t, err)
	require.Equal(t, "image-id", image.ID)
	image, err = resolveLuminaImageService(account, "seedream-5-0-260128", imageServices)
	require.NoError(t, err)
	require.Equal(t, "image-id", image.ID)

	videoSchemas := []lumina.VideoSchema{{ID: "video-id", ReqKey: "Doubao-Seedance-2.0-pro", InferenceType: "x2v", TaskType: "t2v"}}
	video, err := resolveLuminaVideoSchema(account, &modelark.VideoGenerationRequest{Model: "dreamina-seedance-2-0-260128"}, videoSchemas)
	require.NoError(t, err)
	require.Equal(t, "video-id", video.ID)
}

func TestEncodeLuminaImageResponseProducesModelArkBase64Data(t *testing.T) {
	response := &modelark.ImageGenerationResponse{Data: []modelark.ImageGenerationData{{
		URL:  "https://example.com/image.png",
		Size: "2048x2048",
	}}}
	downloader := stubLuminaImageDownloader{downloaded: &lumina.DownloadedMedia{
		Data:        []byte("generated-image"),
		ContentType: "image/png",
	}}

	require.NoError(t, encodeLuminaImageResponse(context.Background(), downloader, response))
	require.Empty(t, response.Data[0].URL)
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte("generated-image")), response.Data[0].B64JSON)
	require.Equal(t, "png", response.Data[0].OutputFormat)
}

func TestLuminaImageProviderAcceptsBase64ResponseFormatAndRejectsStreaming(t *testing.T) {
	service := &LuminaGatewayService{}
	_, err := service.GenerateImage(context.Background(), nil, &modelark.ImageGenerationRequest{
		Model:          "seedream",
		Prompt:         "test",
		ResponseFormat: "b64_json",
	})
	_, isParameterError := modelark.AsParameterError(err)
	require.False(t, isParameterError, "b64_json must pass provider capability validation")

	_, err = service.GenerateImage(context.Background(), nil, &modelark.ImageGenerationRequest{
		Model:  "seedream",
		Prompt: "test",
		Stream: true,
	})
	parameterError, ok := modelark.AsParameterError(err)
	require.True(t, ok)
	require.Equal(t, "InvalidParameter", parameterError.Code)
	require.Equal(t, "stream", parameterError.Param)
}

func TestMapLuminaImageResponseCompletesStandardMetadata(t *testing.T) {
	now := time.Date(2026, time.July, 21, 1, 0, 0, 0, time.UTC)
	response, err := mapLuminaImageResponse("seedream", &lumina.Task{
		CreatedAt: 1_750_000_000_000,
		Children: []lumina.SubTask{{Output: &lumina.TaskOutput{
			Value:    "https://example.com/image.jpeg",
			Format:   "jpg",
			MetaInfo: map[string]any{"width": 2048, "height": 1024},
		}}},
	}, now)
	require.NoError(t, err)
	require.Equal(t, "jpeg", response.Data[0].OutputFormat)
	require.Equal(t, "2048x1024", response.Data[0].Size)
	require.Equal(t, 8192, response.Usage.OutputTokens)
	require.Equal(t, response.Usage.OutputTokens, response.Usage.TotalTokens)

	fallback, err := mapLuminaImageResponse("seedream", &lumina.Task{
		CreatedAt: 0,
		Children: []lumina.SubTask{{Output: &lumina.TaskOutput{Value: "https://example.com/image.png"}}},
	}, now)
	require.NoError(t, err)
	require.Equal(t, now.Unix(), fallback.Created, "missing upstream timestamp must use the injected clock")
}

func TestListVideoTasksUsesOfficialSevenDayWindow(t *testing.T) {
	now := time.Date(2026, time.July, 21, 1, 0, 0, 0, time.UTC)
	repository := &capturingLuminaTaskRepository{}
	service := &LuminaGatewayService{taskRepo: repository, now: func() time.Time { return now }}

	result, err := service.ListVideoTasks(context.Background(), LuminaTaskOwnership{UserID: 1, APIKeyID: 2}, LuminaTaskFilter{Limit: 500})
	require.NoError(t, err)
	require.Empty(t, result.Items)
	require.Equal(t, now.Add(-7*24*time.Hour), repository.filter.CreatedAfter)
	require.Equal(t, now, repository.filter.CreatedBefore)
	require.Equal(t, 500, repository.filter.Limit)
}

func TestModelArkVideoTaskFromStoredMapsTerminalOutput(t *testing.T) {
	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	task := &LuminaTask{
		TaskID:    "task-public",
		Model:     "seedance-pro",
		Status:    LuminaTaskStatusSucceeded,
		CreatedAt: now,
		UpdatedAt: now.Add(time.Minute),
		RequestPayload: map[string]any{
			"resolution": "4k",
			"duration":   5,
		},
		ResponsePayload: map[string]any{
			"children": []any{map[string]any{
				"output": map[string]any{"video_url": "https://example.com/video.mp4"},
			}},
		},
	}

	result := modelArkVideoTaskFromStored(task)
	require.Equal(t, "task-public", result.ID)
	require.Equal(t, "4k", result.Resolution)
	require.NotNil(t, result.Duration)
	require.Equal(t, 5, *result.Duration)
	require.NotNil(t, result.Content)
	require.Equal(t, "https://example.com/video.mp4", result.Content.VideoURL)
	status, recognized := mapLuminaTaskStatus("partial_success")
	require.True(t, recognized)
	require.Equal(t, LuminaTaskStatusSucceeded, status)
	status, recognized = mapLuminaTaskStatus("timeout")
	require.True(t, recognized)
	require.Equal(t, LuminaTaskStatusExpired, status)
}

func TestMapLuminaTaskStatusNeverMigratesUnrecognizedValues(t *testing.T) {
	for _, value := range []string{"", "uploading", "reviewing", "some_new_upstream_state"} {
		_, recognized := mapLuminaTaskStatus(value)
		require.False(t, recognized, "status %q must not produce a state migration", value)
	}
	for value, expected := range map[string]string{
		"queued":  LuminaTaskStatusQueued,
		"running": LuminaTaskStatusRunning,
		"success": LuminaTaskStatusSucceeded,
		"failed":  LuminaTaskStatusFailed,
		"error":   LuminaTaskStatusFailed,
	} {
		status, recognized := mapLuminaTaskStatus(value)
		require.True(t, recognized, "status %q", value)
		require.Equal(t, expected, status)
	}
}

func TestPlanLuminaTaskRefreshKeepsStoredStateForUnrecognizedStatus(t *testing.T) {
	now := time.Date(2026, time.July, 21, 1, 0, 0, 0, time.UTC)
	stored := &LuminaTask{TaskID: "task", Status: LuminaTaskStatusRunning}
	plan := planLuminaTaskRefresh(stored, &lumina.Task{Status: "brand_new_state"}, now)
	require.False(t, plan.recognized)
	require.False(t, plan.terminal)
	require.Nil(t, plan.update.CompletedAt, "unrecognized status must never set CompletedAt")
}

func TestPlanLuminaTaskRefreshSkipsWriteWhenNothingChanged(t *testing.T) {
	now := time.Date(2026, time.July, 21, 1, 0, 0, 0, time.UTC)
	stored := &LuminaTask{TaskID: "task", Status: LuminaTaskStatusQueued}
	plan := planLuminaTaskRefresh(stored, &lumina.Task{Status: "queued"}, now)
	require.True(t, plan.recognized)
	require.True(t, plan.unchanged, "unchanged snapshots must skip persistence so UpdatedAt tracks real progress")

	plan = planLuminaTaskRefresh(stored, &lumina.Task{Status: "running"}, now)
	require.False(t, plan.unchanged)
	require.False(t, plan.terminal)
	require.Equal(t, LuminaTaskStatusRunning, plan.update.Status)
	require.Nil(t, plan.update.CompletedAt)
}

func TestPlanLuminaTaskRefreshMarksTerminalWithCompletionTime(t *testing.T) {
	now := time.Date(2026, time.July, 21, 1, 0, 0, 0, time.UTC)
	stored := &LuminaTask{TaskID: "task", Status: LuminaTaskStatusRunning}
	plan := planLuminaTaskRefresh(stored, &lumina.Task{Status: "success"}, now)
	require.True(t, plan.terminal)
	require.Equal(t, LuminaTaskStatusSucceeded, plan.update.Status)
	require.NotNil(t, plan.update.CompletedAt)
	require.Equal(t, now, *plan.update.CompletedAt)

	plan = planLuminaTaskRefresh(stored, &lumina.Task{Status: "failed", Children: []lumina.SubTask{{FailReason: "output sensitive content"}}}, now)
	require.True(t, plan.terminal)
	require.Equal(t, LuminaTaskStatusFailed, plan.update.Status)
	require.NotNil(t, plan.update.ErrorCode)
	require.Equal(t, "OutputVideoSensitiveContentDetected", *plan.update.ErrorCode)
}

func TestCompleteStoredVideoTaskBillsExactlyOnceOnSucceededTransition(t *testing.T) {
	now := time.Date(2026, time.July, 21, 1, 0, 0, 0, time.UTC)
	stored := &LuminaTask{TaskID: "task", Status: LuminaTaskStatusRunning}

	// The caller that applied the guarded transition bills.
	repository := &stubLuminaTransitionRepository{task: &LuminaTask{TaskID: "task", Status: LuminaTaskStatusSucceeded}, applied: true}
	biller := &spyLuminaVideoTaskBiller{}
	service := &LuminaGatewayService{taskRepo: repository, videoTaskBiller: biller}
	_, err := service.completeStoredVideoTask(context.Background(), stored, UpdateLuminaTaskParams{Status: LuminaTaskStatusSucceeded, CompletedAt: &now})
	require.NoError(t, err)
	require.Equal(t, 1, biller.calls)

	// A concurrent refresh that lost the guarded transition must not bill again.
	repository.applied = false
	_, err = service.completeStoredVideoTask(context.Background(), stored, UpdateLuminaTaskParams{Status: LuminaTaskStatusSucceeded, CompletedAt: &now})
	require.NoError(t, err)
	require.Equal(t, 1, biller.calls)

	// Terminal states other than succeeded never bill.
	repository.applied = true
	_, err = service.completeStoredVideoTask(context.Background(), stored, UpdateLuminaTaskParams{Status: LuminaTaskStatusFailed, CompletedAt: &now})
	require.NoError(t, err)
	require.Equal(t, 1, biller.calls)
}

func TestReconcileVideoTasksExpiresStalledTasksWithoutBilling(t *testing.T) {
	now := time.Date(2026, time.July, 21, 1, 0, 0, 0, time.UTC)
	stalled := &LuminaTask{TaskID: "stalled", Status: LuminaTaskStatusQueued, UpdatedAt: now.Add(-luminaVideoTaskStaleTimeout - time.Hour)}
	repository := &stubLuminaTransitionRepository{task: stalled, applied: true}
	biller := &spyLuminaVideoTaskBiller{}
	service := &LuminaGatewayService{taskRepo: repository, videoTaskBiller: biller, now: func() time.Time { return now }}

	service.ReconcileVideoTasks(context.Background())
	require.Len(t, repository.transitions, 1)
	require.Equal(t, LuminaTaskStatusExpired, repository.transitions[0].Status)
	require.NotNil(t, repository.transitions[0].CompletedAt)
	require.Equal(t, 0, biller.calls, "expired tasks must not be billed")
}

func TestNormalizeLuminaVideoRequestIsTheSingleDefaultSource(t *testing.T) {
	normalized := NormalizeLuminaVideoRequest(&modelark.VideoGenerationRequest{Model: "seedance"})
	require.Equal(t, VideoBillingResolution720P, normalized.Resolution)
	require.Equal(t, "adaptive", normalized.Ratio)
	require.NotNil(t, normalized.Duration)
	require.Equal(t, 5, *normalized.Duration)
	require.NotNil(t, normalized.GenerateAudio)
	require.True(t, *normalized.GenerateAudio)

	normalized = NormalizeLuminaVideoRequest(&modelark.VideoGenerationRequest{Model: "seedance", Resolution: " 4K ", Ratio: "16:9"})
	require.Equal(t, "4k", normalized.Resolution, "resolution is lowercased once at the source")
	require.Equal(t, "16:9", normalized.Ratio)
}

func TestBuildLuminaVideoInputsOnlyRejectsExplicitUnsupportedParameters(t *testing.T) {
	// The schema declares no generate_audio field: the provider default is
	// dropped silently instead of failing with a misleading error.
	schema := luminaVideoSchemaForResolutionTest("720p")
	trimmed := schema.Schema.ConfigSchemas[:len(schema.Schema.ConfigSchemas)-1]
	schema.Schema.ConfigSchemas = trimmed
	request := &modelark.VideoGenerationRequest{
		Model:   "seedance-2.0-pro",
		Content: []modelark.VideoContent{{Type: "text", Text: "A quiet lake"}},
		Ratio:   "16:9",
	}
	_, _, err := buildLuminaVideoInputs(request, &schema)
	require.NoError(t, err)

	// ...but an explicitly requested unsupported parameter still errors.
	generateAudio := false
	request.GenerateAudio = &generateAudio
	_, _, err = buildLuminaVideoInputs(request, &schema)
	require.Error(t, err)
	parameterErr, ok := modelark.AsParameterError(err)
	require.True(t, ok)
	require.Equal(t, "generate_audio", parameterErr.Param)
}

func luminaVideoSchemaForResolutionTest(resolutions ...string) lumina.VideoSchema {
	schema := lumina.VideoSchema{ID: "schema", ReqKey: "Doubao-Seedance-2.0-pro", InferenceType: "x2v", TaskType: "t2v"}
	resolutionEnums := make([]any, 0, len(resolutions))
	for _, resolution := range resolutions {
		resolutionEnums = append(resolutionEnums, map[string]any{"value": resolution})
	}
	schema.Schema.ConfigSchemas = []lumina.SchemaField{
		{InternalName: "task_type"},
		{InternalName: "prompt"},
		{InternalName: "resolution", Props: map[string]any{"enums": resolutionEnums}},
		{InternalName: "frames"},
		{InternalName: "aspect_ratio", Props: map[string]any{"enums": []any{map[string]any{"value": "16:9"}}}},
		{InternalName: "seed"},
		{InternalName: "generate_audio"},
	}
	return schema
}

type stubLuminaImageDownloader struct {
	downloaded *lumina.DownloadedMedia
	err        error
}

func (s stubLuminaImageDownloader) DownloadImage(context.Context, string, int64) (*lumina.DownloadedMedia, error) {
	return s.downloaded, s.err
}

type capturingLuminaTaskRepository struct {
	filter LuminaTaskFilter
}

func (r *capturingLuminaTaskRepository) CreateLuminaTask(context.Context, CreateLuminaTaskParams) (*LuminaTask, error) {
	return nil, nil
}

func (r *capturingLuminaTaskRepository) GetLuminaTaskForOwner(context.Context, int64, int64, string) (*LuminaTask, error) {
	return nil, ErrLuminaTaskNotFound
}

func (r *capturingLuminaTaskRepository) ListLuminaTasksForOwner(_ context.Context, _, _ int64, filter LuminaTaskFilter) ([]*LuminaTask, int, error) {
	r.filter = filter
	return nil, 0, nil
}

func (r *capturingLuminaTaskRepository) ListActiveLuminaTasks(context.Context, int) ([]*LuminaTask, error) {
	return nil, nil
}

func (r *capturingLuminaTaskRepository) UpdateLuminaTask(context.Context, string, UpdateLuminaTaskParams) (*LuminaTask, error) {
	return nil, ErrLuminaTaskNotFound
}

func (r *capturingLuminaTaskRepository) TransitionLuminaTaskToTerminal(context.Context, string, UpdateLuminaTaskParams) (*LuminaTask, bool, error) {
	return nil, false, ErrLuminaTaskNotFound
}

func (r *capturingLuminaTaskRepository) MarkLuminaTaskDeleted(context.Context, int64, int64, string, time.Time) error {
	return nil
}

// stubLuminaTransitionRepository records guarded terminal transitions and
// reports a caller-controlled applied flag for billing idempotency tests.
type stubLuminaTransitionRepository struct {
	capturingLuminaTaskRepository
	task        *LuminaTask
	applied     bool
	transitions []UpdateLuminaTaskParams
}

func (r *stubLuminaTransitionRepository) ListActiveLuminaTasks(context.Context, int) ([]*LuminaTask, error) {
	if r.task == nil {
		return nil, nil
	}
	return []*LuminaTask{r.task}, nil
}

func (r *stubLuminaTransitionRepository) TransitionLuminaTaskToTerminal(_ context.Context, _ string, params UpdateLuminaTaskParams) (*LuminaTask, bool, error) {
	r.transitions = append(r.transitions, params)
	updated := *r.task
	updated.Status = params.Status
	updated.CompletedAt = params.CompletedAt
	return &updated, r.applied, nil
}

type spyLuminaVideoTaskBiller struct {
	calls int
}

func (s *spyLuminaVideoTaskBiller) RecordVideoTaskSuccess(context.Context, *LuminaTask) {
	s.calls++
}

// TestPlanLuminaTaskRefreshUnchangedWhenErrorCodeAlreadyStored verifies the
// fix that compares the upstream snapshot's failure code/message against the
// stored task fields (not against empty strings). A non-terminal task whose
// stored ErrorCode and ErrorMessage already match what the upstream reports
// must produce unchanged=true so UpdatedAt is not advanced on every reconcile
// pass and the 24 h stale-task expiry remains effective.
func TestPlanLuminaTaskRefreshUnchangedWhenErrorCodeAlreadyStored(t *testing.T) {
	now := time.Date(2026, time.July, 22, 0, 0, 0, 0, time.UTC)
	// luminaTaskFailure with a child FailReason returns
	// ("InternalServiceError", "the video generation task failed").
	errCode := "InternalServiceError"
	errMsg := "the video generation task failed"

	stored := &LuminaTask{
		TaskID:       "task-1",
		Status:       LuminaTaskStatusRunning,
		ErrorCode:    &errCode,
		ErrorMessage: &errMsg,
	}
	// Upstream still reports running with the same partial failure reason.
	upstream := &lumina.Task{
		Status:   "running",
		Children: []lumina.SubTask{{FailReason: "upstream transient issue"}},
	}
	plan := planLuminaTaskRefresh(stored, upstream, now)

	require.True(t, plan.recognized)
	require.False(t, plan.terminal)
	require.True(t, plan.unchanged, "repeated snapshot must be unchanged so UpdatedAt is not advanced each pass")
}

// TestPlanLuminaTaskRefreshDetectsNewFailureOnPreviouslyCleanTask verifies
// that a task stored without any error code is updated when the upstream
// snapshot first reports a failure.
func TestPlanLuminaTaskRefreshDetectsNewFailureOnPreviouslyCleanTask(t *testing.T) {
	now := time.Date(2026, time.July, 22, 0, 0, 0, 0, time.UTC)
	stored := &LuminaTask{
		TaskID:       "task-2",
		Status:       LuminaTaskStatusRunning,
		ErrorCode:    nil,
		ErrorMessage: nil,
	}
	upstream := &lumina.Task{
		Status:   "running",
		Children: []lumina.SubTask{{FailReason: "child processing error"}},
	}
	plan := planLuminaTaskRefresh(stored, upstream, now)

	require.True(t, plan.recognized)
	require.False(t, plan.unchanged, "new failure information must trigger an update")
	require.NotNil(t, plan.update.ErrorCode, "update must carry the new error code")
	require.Equal(t, "InternalServiceError", *plan.update.ErrorCode)
}

// TestPlanLuminaTaskRefreshDetectsFailureClearedOnUpstream verifies that when
// the upstream snapshot no longer reports any failure reason (cleared) but the
// stored task still has a non-empty ErrorCode, the plan is not unchanged.
func TestPlanLuminaTaskRefreshDetectsFailureClearedOnUpstream(t *testing.T) {
	now := time.Date(2026, time.July, 22, 0, 0, 0, 0, time.UTC)
	errCode := "InternalServiceError"
	errMsg := "the video generation task failed"
	stored := &LuminaTask{
		TaskID:       "task-3",
		Status:       LuminaTaskStatusRunning,
		ErrorCode:    &errCode,
		ErrorMessage: &errMsg,
	}
	// Upstream now reports running with no children and no failed status.
	upstream := &lumina.Task{
		Status:   "running",
		Children: nil,
	}
	plan := planLuminaTaskRefresh(stored, upstream, now)

	require.True(t, plan.recognized)
	require.False(t, plan.unchanged, "cleared failure on upstream must not be treated as unchanged")
}
