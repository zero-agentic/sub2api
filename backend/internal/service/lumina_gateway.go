package service

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/lumina"
	"github.com/Wei-Shaw/sub2api/internal/pkg/modelark"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	luminaCatalogTTL           = 10 * time.Minute
	luminaImagePollInterval    = 1500 * time.Millisecond
	luminaImagePollMaxInterval = 5 * time.Second
	luminaImagePollTimeout     = 5 * time.Minute
	luminaImageDownloadMax     = 64 << 20

	luminaTaskReconcileInterval  = 2 * time.Minute
	luminaTaskReconcileBatchSize = 200
	luminaVideoTaskStaleTimeout  = 24 * time.Hour

	// The reconcile pass runs single-flight across instances; the pass timeout
	// stays below the lock TTL so the lock can never expire mid-run.
	luminaTaskReconcileLeaderLockKey = "lumina:task_reconcile:leader"
	luminaTaskReconcileLeaderLockTTL = 10 * time.Minute
	luminaTaskReconcilePassTimeout   = 8 * time.Minute
)

type LuminaTaskOwnership struct {
	UserID   int64
	APIKeyID int64
	GroupID  int64
}

type luminaCatalogCacheEntry struct {
	images    []lumina.ImageService
	videos    []lumina.VideoSchema
	expiresAt time.Time
}

type LuminaGatewayService struct {
	accountRepo     AccountRepository
	taskRepo        LuminaTaskRepository
	sessionProvider *LuminaSessionProvider
	videoTaskBiller LuminaVideoTaskBiller

	catalogMu sync.Mutex
	catalogs  map[int64]luminaCatalogCacheEntry
	now       func() time.Time

	reconcileOnce   sync.Once
	reconcileCancel context.CancelFunc
	reconcileWg     sync.WaitGroup

	leaderLockCache LeaderLockCache
	leaderLockDB    *sql.DB
	instanceID      string
}

func NewLuminaGatewayService(
	accountRepo AccountRepository,
	taskRepo LuminaTaskRepository,
	sessionProvider *LuminaSessionProvider,
) *LuminaGatewayService {
	return &LuminaGatewayService{
		accountRepo:     accountRepo,
		taskRepo:        taskRepo,
		sessionProvider: sessionProvider,
		catalogs:        make(map[int64]luminaCatalogCacheEntry),
		now:             time.Now,
		instanceID:      uuid.NewString(),
	}
}

// SetLeaderLock injects the cross-instance coordination used to elect a single
// reconciler per cycle; without it every replica would poll the cookie-session
// upstream for the same tasks.
func (s *LuminaGatewayService) SetLeaderLock(lockCache LeaderLockCache, db *sql.DB) {
	s.leaderLockCache = lockCache
	s.leaderLockDB = db
}

// SetVideoTaskBiller injects the usage recorder invoked exactly once per task,
// when a guarded transition into succeeded is applied to the store.
func (s *LuminaGatewayService) SetVideoTaskBiller(biller LuminaVideoTaskBiller) {
	s.videoTaskBiller = biller
}

// StartTaskReconciler launches the background loop that converges non-terminal
// video tasks: it refreshes them from upstream, lets successful completions
// bill through the guarded transition, and expires tasks that stopped making
// progress. Extra calls are no-ops.
func (s *LuminaGatewayService) StartTaskReconciler(interval time.Duration) {
	if s == nil || s.taskRepo == nil {
		return
	}
	if interval <= 0 {
		interval = luminaTaskReconcileInterval
	}
	s.reconcileOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		s.reconcileCancel = cancel
		s.reconcileWg.Add(1)
		go s.taskReconcileLoop(ctx, interval)
	})
}

// StopTaskReconciler stops the background loop; safe when never started.
func (s *LuminaGatewayService) StopTaskReconciler() {
	if s == nil || s.reconcileCancel == nil {
		return
	}
	s.reconcileCancel()
	s.reconcileWg.Wait()
}

func (s *LuminaGatewayService) taskReconcileLoop(ctx context.Context, interval time.Duration) {
	defer s.reconcileWg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	s.ReconcileVideoTasks(ctx)
	for {
		select {
		case <-ticker.C:
			s.ReconcileVideoTasks(ctx)
		case <-ctx.Done():
			return
		}
	}
}

// ReconcileVideoTasks performs one bounded pass over stored non-terminal video
// tasks so orphaned tasks converge even when their owner never polls. The pass
// is single-flight across instances: N replicas polling the cookie-session
// upstream for the same tasks would multiply upstream load and widen refresh
// races for no benefit.
func (s *LuminaGatewayService) ReconcileVideoTasks(ctx context.Context) {
	if s == nil || s.taskRepo == nil {
		return
	}
	release, ok := tryAcquireSingletonLeaderLock(ctx, s.leaderLockCache, s.leaderLockDB, luminaTaskReconcileLeaderLockKey, s.instanceID, luminaTaskReconcileLeaderLockTTL)
	if !ok {
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, luminaTaskReconcilePassTimeout)
	defer cancel()
	tasks, err := s.taskRepo.ListActiveLuminaTasks(ctx, luminaTaskReconcileBatchSize)
	if err != nil {
		logger.L().Warn("lumina.task_reconcile_list_failed", zap.Error(err))
		return
	}
	now := s.currentTime()
	for _, task := range tasks {
		if ctx.Err() != nil {
			return
		}
		// UpdatedAt only advances on real state changes, so a task stuck beyond
		// the stale timeout is expired instead of polled forever.
		if now.Sub(task.UpdatedAt) >= luminaVideoTaskStaleTimeout {
			if _, err := s.expireStoredVideoTask(ctx, task, "the video generation task did not finish within the allowed time"); err != nil {
				logger.L().Warn("lumina.task_reconcile_expire_failed", zap.String("task_id", task.TaskID), zap.Error(err))
			}
			continue
		}
		if _, err := s.refreshStoredVideoTask(ctx, task); err != nil {
			logger.L().Debug("lumina.task_reconcile_refresh_failed", zap.String("task_id", task.TaskID), zap.Error(err))
		}
	}
}

func (s *LuminaGatewayService) GenerateImage(
	ctx context.Context,
	account *Account,
	request *modelark.ImageGenerationRequest,
) (*modelark.ImageGenerationResponse, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if len(request.Image) > 0 {
		return nil, modelark.UnsupportedParameter("image", "image input is not supported by the selected account provider")
	}
	if request.SequentialImageGeneration == "auto" || request.SequentialImageGenerationOptions != nil {
		return nil, modelark.UnsupportedParameter("sequential_image_generation", "sequential image generation is not supported by the selected account provider")
	}
	if request.Stream {
		return nil, modelark.UnsupportedParameter("stream", "streaming image output is not supported by the selected account provider")
	}
	if request.OptimizePromptOptions != nil {
		return nil, modelark.UnsupportedParameter("optimize_prompt_options", "prompt optimization is not supported by the selected account provider")
	}
	if request.OutputFormat != "" {
		return nil, modelark.UnsupportedParameter("output_format", "output format selection is not supported by the selected account provider")
	}
	if request.Watermark != nil && !*request.Watermark {
		return nil, modelark.UnsupportedParameter("watermark", "watermark=false is not supported by the selected account provider")
	}

	catalog, err := s.catalogForAccount(ctx, account)
	if err != nil {
		return nil, err
	}
	imageService, err := resolveLuminaImageService(account, request.Model, catalog.images)
	if err != nil {
		return nil, err
	}
	schema, err := imageService.ParsedSchema()
	if err != nil {
		return nil, err
	}
	inputs, err := buildLuminaImageInputs(request, schema)
	if err != nil {
		return nil, err
	}
	upstreamRequest := lumina.ImageCreateTaskRequest{
		Inputs: inputs,
		InferenceConfig: lumina.ImageInferenceConfig{
			InferencePipeline: imageService.InferencePipeline,
			InferenceID:       imageService.ID,
			InferenceVerID:    imageService.ID,
			Name:              imageService.Name,
			ReqKey:            imageService.ReqKey,
		},
		// Image inputs are rejected above, so only text-to-image reaches upstream.
		InferenceType: "t2i",
		RequestSource: 1,
		Count:         1,
	}

	var created *lumina.CreateTaskResponse
	account, err = s.withAuthenticatedClient(ctx, account, func(client *lumina.Client) error {
		var createErr error
		created, createErr = client.CreateImageTask(ctx, upstreamRequest)
		return createErr
	})
	if err != nil {
		return nil, err
	}
	task, err := s.pollImageTask(ctx, account, created.TaskID())
	if err != nil {
		return nil, err
	}
	response, err := mapLuminaImageResponse(request.Model, task, s.currentTime())
	if err != nil {
		return nil, err
	}
	if request.ResponseFormat == "b64_json" {
		_, err = s.withAuthenticatedClient(ctx, account, func(client *lumina.Client) error {
			return encodeLuminaImageResponse(ctx, client, response)
		})
		if err != nil {
			return nil, err
		}
	}
	return response, nil
}

func (s *LuminaGatewayService) CreateVideoTask(
	ctx context.Context,
	account *Account,
	owner LuminaTaskOwnership,
	request *modelark.VideoGenerationRequest,
) (*modelark.CreateVideoTaskResponse, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if err := validateLuminaVideoSupportedParameters(request); err != nil {
		return nil, err
	}
	catalog, err := s.catalogForAccount(ctx, account)
	if err != nil {
		return nil, err
	}
	schema, err := resolveLuminaVideoSchema(account, request, catalog.videos)
	if err != nil {
		return nil, err
	}
	inputs, normalized, err := buildLuminaVideoInputs(request, schema)
	if err != nil {
		return nil, err
	}
	upstreamRequest := lumina.VideoCreateTaskRequest{
		BAVersion: 2,
		Type:      schema.InferenceType,
		ModelID:   schema.ID,
		Inputs:    inputs,
	}
	var created *lumina.CreateTaskResponse
	account, err = s.withAuthenticatedClient(ctx, account, func(client *lumina.Client) error {
		var createErr error
		created, createErr = client.CreateVideoTask(ctx, upstreamRequest)
		return createErr
	})
	if err != nil {
		return nil, err
	}

	publicTaskID, err := NewLuminaTaskID()
	if err != nil {
		return nil, err
	}
	requestPayload := structToMap(normalized)
	_, err = s.taskRepo.CreateLuminaTask(ctx, CreateLuminaTaskParams{
		TaskID:         publicTaskID,
		UserID:         owner.UserID,
		APIKeyID:       owner.APIKeyID,
		GroupID:        owner.GroupID,
		AccountID:      account.ID,
		UpstreamTaskID: created.TaskID(),
		TaskType:       LuminaTaskTypeVideo,
		Model:          request.Model,
		Status:         LuminaTaskStatusQueued,
		RequestPayload: requestPayload,
	})
	if err != nil {
		_, _ = s.withAuthenticatedClient(context.WithoutCancel(ctx), account, func(client *lumina.Client) error {
			return client.StopTask(context.WithoutCancel(ctx), created.TaskID())
		})
		return nil, fmt.Errorf("persist lumina video task: %w", err)
	}
	return &modelark.CreateVideoTaskResponse{ID: publicTaskID}, nil
}

func (s *LuminaGatewayService) GetVideoTask(ctx context.Context, owner LuminaTaskOwnership, taskID string) (*modelark.VideoTask, error) {
	task, err := s.taskRepo.GetLuminaTaskForOwner(ctx, owner.UserID, owner.APIKeyID, taskID)
	if err != nil {
		return nil, err
	}
	return s.refreshPublicVideoTask(ctx, task)
}

func (s *LuminaGatewayService) ListVideoTasks(ctx context.Context, owner LuminaTaskOwnership, filter LuminaTaskFilter) (*modelark.ListVideoTasksResponse, error) {
	now := s.currentTime().UTC()
	filter.CreatedAfter = now.Add(-7 * 24 * time.Hour)
	filter.CreatedBefore = now
	tasks, total, err := s.taskRepo.ListLuminaTasksForOwner(ctx, owner.UserID, owner.APIKeyID, filter)
	if err != nil {
		return nil, err
	}
	// The background reconciler keeps stored statuses fresh, so listing is a
	// pure store read: no per-task upstream refresh and total stays consistent
	// with the returned items.
	items := make([]*modelark.VideoTask, 0, len(tasks))
	for _, task := range tasks {
		items = append(items, modelArkVideoTaskFromStored(task))
	}
	return &modelark.ListVideoTasksResponse{Items: items, Total: total}, nil
}

func (s *LuminaGatewayService) DeleteVideoTask(ctx context.Context, owner LuminaTaskOwnership, taskID string) error {
	task, err := s.taskRepo.GetLuminaTaskForOwner(ctx, owner.UserID, owner.APIKeyID, taskID)
	if err != nil {
		return err
	}
	publicTask, err := s.refreshPublicVideoTask(ctx, task)
	if err != nil {
		return err
	}
	account, err := s.accountRepo.GetByID(ctx, task.AccountID)
	if err != nil || account == nil || !account.IsLuminaCookie() {
		return ErrLuminaSessionUnavailable
	}
	switch publicTask.Status {
	case LuminaTaskStatusQueued:
		_, err = s.withAuthenticatedClient(ctx, account, func(client *lumina.Client) error {
			return client.StopTask(ctx, task.UpstreamTaskID)
		})
		if err != nil && !isLuminaUpstreamTaskGone(err) {
			return err
		}
		now := s.currentTime()
		// Guarded transition: a concurrent refresh may have finalized the task
		// between the status check above and the upstream stop.
		_, applied, transErr := s.taskRepo.TransitionLuminaTaskToTerminal(ctx, task.TaskID, UpdateLuminaTaskParams{Status: LuminaTaskStatusCancelled, CompletedAt: &now})
		if transErr != nil {
			return transErr
		}
		if !applied {
			return modelark.InvalidParameter("id", "video task status does not permit deletion")
		}
		return nil
	case LuminaTaskStatusRunning:
		return modelark.InvalidParameter("id", "a running video task cannot be deleted")
	case LuminaTaskStatusCancelled, LuminaTaskStatusSucceeded, LuminaTaskStatusFailed, LuminaTaskStatusExpired:
		_, err = s.withAuthenticatedClient(ctx, account, func(client *lumina.Client) error {
			return client.RemoveTask(ctx, task.UpstreamTaskID)
		})
		// The upstream task may already be gone (e.g. expired upstream); the
		// local record is still safe to mark deleted.
		if err != nil && !isLuminaUpstreamTaskGone(err) {
			return err
		}
		return s.taskRepo.MarkLuminaTaskDeleted(ctx, owner.UserID, owner.APIKeyID, taskID, s.currentTime())
	default:
		return modelark.InvalidParameter("id", "video task status does not permit deletion")
	}
}

func (s *LuminaGatewayService) refreshPublicVideoTask(ctx context.Context, task *LuminaTask) (*modelark.VideoTask, error) {
	if task == nil {
		return nil, ErrLuminaTaskNotFound
	}
	if IsTerminalLuminaTaskStatus(task.Status) {
		return modelArkVideoTaskFromStored(task), nil
	}
	refreshed, err := s.refreshStoredVideoTask(ctx, task)
	if err != nil {
		return nil, err
	}
	return modelArkVideoTaskFromStored(refreshed), nil
}

// refreshStoredVideoTask queries upstream once and persists the outcome. An
// unrecognized upstream status produces no state migration; a terminal
// transition goes through the guarded store update so billing fires at most
// once per task.
func (s *LuminaGatewayService) refreshStoredVideoTask(ctx context.Context, task *LuminaTask) (*LuminaTask, error) {
	account, err := s.accountRepo.GetByID(ctx, task.AccountID)
	if err != nil || account == nil || !account.IsLuminaCookie() {
		return nil, ErrLuminaSessionUnavailable
	}
	var upstream *lumina.Task
	_, err = s.withAuthenticatedClient(ctx, account, func(client *lumina.Client) error {
		var queryErr error
		upstream, queryErr = client.QueryTask(ctx, task.UpstreamTaskID)
		return queryErr
	})
	if err != nil {
		if isLuminaUpstreamTaskGone(err) {
			return s.expireStoredVideoTask(ctx, task, "the upstream video generation task no longer exists")
		}
		return nil, err
	}
	plan := planLuminaTaskRefresh(task, upstream, s.currentTime())
	switch {
	case !plan.recognized || plan.unchanged:
		return task, nil
	case plan.terminal:
		return s.completeStoredVideoTask(ctx, task, plan.update)
	default:
		return s.taskRepo.UpdateLuminaTask(ctx, task.TaskID, plan.update)
	}
}

// completeStoredVideoTask persists a terminal status through the guarded
// transition and bills exactly the caller that applied the move into
// succeeded; failed/cancelled/expired tasks are never billed.
func (s *LuminaGatewayService) completeStoredVideoTask(ctx context.Context, task *LuminaTask, update UpdateLuminaTaskParams) (*LuminaTask, error) {
	updated, applied, err := s.taskRepo.TransitionLuminaTaskToTerminal(ctx, task.TaskID, update)
	if err != nil {
		return nil, err
	}
	if applied && update.Status == LuminaTaskStatusSucceeded {
		s.billVideoTaskSuccess(ctx, updated)
	}
	return updated, nil
}

func (s *LuminaGatewayService) expireStoredVideoTask(ctx context.Context, task *LuminaTask, message string) (*LuminaTask, error) {
	now := s.currentTime()
	return s.completeStoredVideoTask(ctx, task, UpdateLuminaTaskParams{
		Status:       LuminaTaskStatusExpired,
		ErrorCode:    stringPointer("TaskExpired"),
		ErrorMessage: stringPointer(message),
		CompletedAt:  &now,
	})
}

func (s *LuminaGatewayService) billVideoTaskSuccess(ctx context.Context, task *LuminaTask) {
	if s == nil || s.videoTaskBiller == nil || task == nil {
		return
	}
	// The guarded terminal transition is one-shot: once applied, no other path
	// re-enters billing for this task. Detach from the caller's cancellation so
	// a client disconnect mid-poll cannot permanently lose the charge.
	s.videoTaskBiller.RecordVideoTaskSuccess(context.WithoutCancel(ctx), task)
}

// isLuminaUpstreamTaskGone reports whether upstream answered 404 for the task,
// which maps to the expired terminal state rather than an internal error.
func isLuminaUpstreamTaskGone(err error) bool {
	var apiErr *lumina.APIError
	return errors.As(err, &apiErr) && apiErr != nil && apiErr.Status == http.StatusNotFound
}

// luminaTaskRefreshPlan describes how one upstream snapshot should be persisted.
type luminaTaskRefreshPlan struct {
	update     UpdateLuminaTaskParams
	recognized bool
	terminal   bool
	unchanged  bool
}

// planLuminaTaskRefresh computes the persistence plan for an upstream snapshot.
// An unrecognized upstream status yields recognized=false and never migrates
// the stored state. A snapshot that changes nothing yields unchanged=true and
// skips the write entirely, so UpdatedAt only advances on real state changes
// (the reconciler relies on that to detect stalled tasks).
func planLuminaTaskRefresh(task *LuminaTask, upstream *lumina.Task, now time.Time) luminaTaskRefreshPlan {
	plan := luminaTaskRefreshPlan{}
	status, recognized := mapLuminaTaskStatus(upstream.Status)
	if !recognized {
		return plan
	}
	plan.recognized = true
	plan.terminal = IsTerminalLuminaTaskStatus(status)
	code, message := luminaTaskFailure(upstream, LuminaTaskTypeVideo)
	// Unchanged means the snapshot repeats the stored state, including an
	// already-persisted failure code/message (a non-terminal task with a
	// partially failed child reports the same failure on every poll; treating
	// that as a change would bump UpdatedAt each pass and defeat the
	// stale-task expiry, which relies on UpdatedAt only advancing on real
	// state changes).
	if status == task.Status && code == luminaStringValue(task.ErrorCode) && message == luminaStringValue(task.ErrorMessage) {
		plan.unchanged = true
		return plan
	}
	update := UpdateLuminaTaskParams{Status: status, ResponsePayload: structToMap(upstream)}
	if code != "" || message != "" {
		update.ErrorCode = stringPointer(code)
		update.ErrorMessage = stringPointer(message)
	}
	if plan.terminal {
		update.CompletedAt = &now
	}
	plan.update = update
	return plan
}

func (s *LuminaGatewayService) pollImageTask(ctx context.Context, account *Account, upstreamTaskID string) (*lumina.Task, error) {
	pollCtx, cancel := context.WithTimeout(ctx, luminaImagePollTimeout)
	defer cancel()
	// Poll quickly at first for snappy short generations, then back off so a
	// slow task does not hammer the upstream console API for the full timeout.
	interval := luminaImagePollInterval
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		var task *lumina.Task
		var err error
		account, err = s.withAuthenticatedClient(pollCtx, account, func(client *lumina.Client) error {
			var queryErr error
			task, queryErr = client.QueryTask(pollCtx, upstreamTaskID)
			return queryErr
		})
		if err != nil {
			return nil, err
		}
		status, recognized := mapLuminaTaskStatus(task.Status)
		if recognized {
			switch status {
			case LuminaTaskStatusSucceeded:
				return task, nil
			case LuminaTaskStatusFailed, LuminaTaskStatusCancelled, LuminaTaskStatusExpired:
				code, message := luminaTaskFailure(task, LuminaTaskTypeImage)
				if message == "" {
					message = "Lumina image generation failed"
				}
				return nil, &lumina.APIError{Status: 200, Code: code, Message: message}
			}
		}
		// Unrecognized upstream statuses keep polling instead of failing the task.
		select {
		case <-pollCtx.Done():
			return nil, pollCtx.Err()
		case <-timer.C:
		}
		interval = min(interval*3/2, luminaImagePollMaxInterval)
		timer.Reset(interval)
	}
}

func (s *LuminaGatewayService) catalogForAccount(ctx context.Context, account *Account) (luminaCatalogCacheEntry, error) {
	if account == nil || !account.IsLuminaCookie() {
		return luminaCatalogCacheEntry{}, ErrLuminaSessionUnavailable
	}
	now := s.currentTime()
	s.catalogMu.Lock()
	cached, ok := s.catalogs[account.ID]
	if ok && now.Before(cached.expiresAt) {
		s.catalogMu.Unlock()
		return cached, nil
	}
	s.catalogMu.Unlock()

	images, buckets, err := s.CatalogForAccount(ctx, account, false)
	if err != nil {
		return luminaCatalogCacheEntry{}, err
	}
	videos := make([]lumina.VideoSchema, 0)
	for _, bucket := range buckets {
		videos = append(videos, bucket.Items...)
	}
	entry := luminaCatalogCacheEntry{images: images, videos: videos, expiresAt: now.Add(luminaCatalogTTL)}
	s.catalogMu.Lock()
	s.catalogs[account.ID] = entry
	s.catalogMu.Unlock()
	return entry, nil
}

// CatalogForAccount lists image services and video schema buckets through the
// shared authenticated-client flow (one re-login retry included), bypassing
// the gateway catalog cache. Account connection tests use it directly and may
// probe inactive accounts via allowInactive.
func (s *LuminaGatewayService) CatalogForAccount(ctx context.Context, account *Account, allowInactive bool) ([]lumina.ImageService, []lumina.VideoSchemaBucket, error) {
	if account == nil || !account.IsLuminaCookie() {
		return nil, nil, ErrLuminaSessionUnavailable
	}
	var images []lumina.ImageService
	var buckets []lumina.VideoSchemaBucket
	_, err := s.withAuthenticatedClientMode(ctx, account, allowInactive, func(client *lumina.Client) error {
		var listErr error
		images, listErr = client.ListImageServices(ctx, 1, 500)
		if listErr != nil {
			return listErr
		}
		buckets, listErr = client.ListVideoSchemas(ctx)
		return listErr
	})
	if err != nil {
		return nil, nil, err
	}
	return images, buckets, nil
}

func (s *LuminaGatewayService) withAuthenticatedClient(ctx context.Context, account *Account, operation func(*lumina.Client) error) (*Account, error) {
	return s.withAuthenticatedClientMode(ctx, account, false, operation)
}

func (s *LuminaGatewayService) withAuthenticatedClientMode(ctx context.Context, account *Account, allowInactive bool, operation func(*lumina.Client) error) (*Account, error) {
	if s == nil || s.sessionProvider == nil {
		return account, ErrLuminaSessionUnavailable
	}
	client, fresh, err := s.sessionProvider.EnsureAuthenticated(ctx, account, allowInactive)
	if err != nil {
		return fresh, err
	}
	err = operation(client)
	_ = s.sessionProvider.PersistRotatedCookies(ctx, fresh, client)
	if err == nil || !lumina.IsAuthenticationError(err) {
		return fresh, err
	}
	client, fresh, refreshErr := s.sessionProvider.RefreshAfterAuthFailure(ctx, fresh)
	if refreshErr != nil {
		return fresh, refreshErr
	}
	err = operation(client)
	_ = s.sessionProvider.PersistRotatedCookies(ctx, fresh, client)
	return fresh, err
}

func (s *LuminaGatewayService) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

func validateLuminaVideoSupportedParameters(request *modelark.VideoGenerationRequest) error {
	for _, content := range request.Content {
		if content.Type != "text" {
			return modelark.UnsupportedParameter("content", "this content type is not supported by the selected account provider")
		}
	}
	if request.CallbackURL != "" {
		return modelark.UnsupportedParameter("callback_url", "callback_url is not supported by the selected account provider")
	}
	if request.ReturnLastFrame != nil && *request.ReturnLastFrame {
		return modelark.UnsupportedParameter("return_last_frame", "return_last_frame is not supported by the selected account provider")
	}
	if request.ServiceTier != "" && request.ServiceTier != "default" {
		return modelark.UnsupportedParameter("service_tier", "only service_tier=default is supported by the selected account provider")
	}
	if request.ExecutionExpiresAfter != nil {
		return modelark.UnsupportedParameter("execution_expires_after", "execution_expires_after is not supported by the selected account provider")
	}
	if request.Draft != nil && *request.Draft {
		return modelark.UnsupportedParameter("draft", "draft mode is not supported by the selected account provider")
	}
	if request.Priority != nil && *request.Priority != 0 {
		return modelark.UnsupportedParameter("priority", "non-zero priority is not supported by the selected account provider")
	}
	if request.Frames != nil {
		return modelark.UnsupportedParameter("frames", "frames is not supported by the selected account provider; use duration")
	}
	if request.CameraFixed != nil && *request.CameraFixed {
		return modelark.UnsupportedParameter("camera_fixed", "camera_fixed=true is not supported by the selected account provider")
	}
	if request.Watermark == nil || !*request.Watermark {
		return modelark.UnsupportedParameter("watermark", "the selected account provider requires watermark=true")
	}
	if request.Duration != nil && *request.Duration == -1 {
		return modelark.UnsupportedParameter("duration", "automatic duration (-1) is not supported by the selected account provider")
	}
	return nil
}

func buildLuminaImageInputs(request *modelark.ImageGenerationRequest, schema *lumina.ImageServiceSchema) ([]lumina.InferenceInput, error) {
	values := map[string]any{
		"prompt": request.Prompt,
	}
	if request.Size != "" {
		values["size"] = normalizeLuminaImageSize(request.Size)
	}
	if request.Seed != nil {
		values["seed"] = *request.Seed
	}
	if request.GuidanceScale != nil {
		values["guidance_scale"] = *request.GuidanceScale
	}
	inputs := make([]lumina.InferenceInput, 0, len(schema.Inputs))
	used := make(map[string]bool, len(values))
	for _, field := range schema.Inputs {
		value, exists := values[field.InternalName]
		if !exists {
			value, exists = values[field.Name]
		}
		if !exists {
			value = field.DefaultValue
		}
		if value == nil {
			continue
		}
		if field.InternalName != "" {
			used[field.InternalName] = true
		}
		used[field.Name] = true
		inputs = append(inputs, lumina.InferenceInput{
			Name:         field.Name,
			InternalName: field.InternalName,
			Type:         luminaFirstNonEmpty(field.Type, field.DataType),
			Value:        value,
			Label:        field.Label,
			Format:       field.Format,
			Props:        field.Props,
			Transformer:  field.Transformer,
		})
	}
	for key := range values {
		if !used[key] {
			return nil, modelark.UnsupportedParameter(key, "selected Lumina image model does not support this parameter")
		}
	}
	if size, ok := values["size"].(string); ok && !imageSchemaSupportsSize(schema, size) {
		return nil, modelark.InvalidParameter("size", "selected Lumina image model does not support the requested size")
	}
	return inputs, nil
}

// NormalizeLuminaVideoRequest returns a copy of the request with the Lumina
// provider defaults applied. It is the single source of those defaults: the
// handler billing gate, the upstream request builder, and usage recording all
// derive from it, so they can never disagree on resolution or ratio.
func NormalizeLuminaVideoRequest(request *modelark.VideoGenerationRequest) *modelark.VideoGenerationRequest {
	normalized := *request
	normalized.Resolution = strings.ToLower(strings.TrimSpace(normalized.Resolution))
	if normalized.Resolution == "" {
		normalized.Resolution = VideoBillingResolution720P
	}
	normalized.Ratio = strings.TrimSpace(normalized.Ratio)
	if normalized.Ratio == "" {
		normalized.Ratio = "adaptive"
	}
	if normalized.Duration == nil {
		value := 5
		normalized.Duration = &value
	}
	if normalized.GenerateAudio == nil {
		value := true
		normalized.GenerateAudio = &value
	}
	if normalized.ServiceTier == "" {
		normalized.ServiceTier = "default"
	}
	if normalized.Priority == nil {
		value := 0
		normalized.Priority = &value
	}
	return &normalized
}

func buildLuminaVideoInputs(request *modelark.VideoGenerationRequest, schema *lumina.VideoSchema) ([]lumina.InferenceInput, *modelark.VideoGenerationRequest, error) {
	normalized := NormalizeLuminaVideoRequest(request)
	if !schema.SupportsValue("resolution", normalized.Resolution) {
		return nil, nil, modelark.InvalidParameter("resolution", "selected Lumina video model does not support the requested resolution")
	}
	if !schema.SupportsValue("aspect_ratio", normalized.Ratio) && !(normalized.Ratio == "adaptive" && schema.SupportsValue("aspect_ratio", "auto")) {
		return nil, nil, modelark.InvalidParameter("ratio", "selected Lumina video model does not support the requested ratio")
	}
	values := map[string]any{
		"task_type":      schema.TaskType,
		"prompt":         videoPrompt(normalized.Content),
		"resolution":     normalized.Resolution,
		"frames":         *normalized.Duration*24 + 1,
		"aspect_ratio":   normalized.Ratio,
		"seed":           int64(-1),
		"generate_audio": *normalized.GenerateAudio,
	}
	if normalized.Seed != nil {
		values["seed"] = *normalized.Seed
	}
	// explicit marks keys the caller actually supplied. Provider defaults that
	// the schema does not declare are dropped silently instead of failing with
	// a misleading "model does not support this parameter" error for a
	// parameter the user never sent.
	explicit := map[string]bool{
		"prompt":         true,
		"resolution":     strings.TrimSpace(request.Resolution) != "",
		"frames":         request.Duration != nil,
		"aspect_ratio":   strings.TrimSpace(request.Ratio) != "",
		"seed":           request.Seed != nil,
		"generate_audio": request.GenerateAudio != nil,
	}
	inputs := make([]lumina.InferenceInput, 0, len(schema.AllSchemaFields()))
	seen := make(map[string]bool)
	for _, field := range schema.AllSchemaFields() {
		key := field.InternalName
		if key == "" {
			key = field.Name
		}
		value, exists := values[key]
		if !exists {
			value = field.DefaultValue
		}
		if value == nil || seen[key] {
			continue
		}
		seen[key] = true
		inputs = append(inputs, lumina.InferenceInput{
			Name:         field.Name,
			InternalName: field.InternalName,
			Type:         luminaFirstNonEmpty(field.DataType, field.Type),
			Value:        luminaInputString(value),
			Label:        field.Label,
			Format:       field.Format,
			Props:        field.Props,
			Transformer:  field.Transformer,
		})
	}
	for key := range values {
		if !seen[key] && explicit[key] {
			return nil, nil, modelark.UnsupportedParameter(publicVideoParameterName(key), "selected Lumina video model does not support this parameter")
		}
	}
	return inputs, normalized, nil
}

func resolveLuminaImageService(account *Account, requested string, services []lumina.ImageService) (*lumina.ImageService, error) {
	selector := luminaModelSelectorForAccount(account, requested)
	for index := range services {
		if selector.matchesImage(services[index]) {
			return &services[index], nil
		}
	}
	return nil, modelark.ModelNotFound("the requested image model or endpoint does not exist or is not available")
}

func resolveLuminaVideoSchema(account *Account, request *modelark.VideoGenerationRequest, schemas []lumina.VideoSchema) (*lumina.VideoSchema, error) {
	selector := luminaModelSelectorForAccount(account, request.Model)
	for index := range schemas {
		if schemas[index].InferenceType != "x2v" || schemas[index].TaskType != "t2v" {
			continue
		}
		if selector.matchesVideo(schemas[index]) {
			return &schemas[index], nil
		}
	}
	return nil, modelark.ModelNotFound("the requested video model or endpoint does not exist or is not available")
}

type luminaModelSelector struct {
	requested string
	id        string
	reqKey    string
	name      string
}

func luminaModelSelectorForAccount(account *Account, requested string) luminaModelSelector {
	selector := luminaModelSelector{requested: requested}
	if account == nil || account.Credentials == nil {
		return selector
	}
	mapping, _ := account.Credentials["model_mapping"].(map[string]any)
	value, ok := mapping[requested]
	if !ok {
		for key, candidate := range mapping {
			if normalizeModelName(key) == normalizeModelName(requested) {
				value = candidate
				ok = true
				break
			}
		}
	}
	if !ok {
		return selector
	}
	switch typed := value.(type) {
	case string:
		selector.reqKey = typed
		selector.id = typed
		selector.name = typed
	case map[string]any:
		selector.id, _ = typed["id"].(string)
		selector.reqKey, _ = typed["req_key"].(string)
		selector.name, _ = typed["name"].(string)
	}
	return selector
}

func (s luminaModelSelector) matchesImage(service lumina.ImageService) bool {
	return modelSelectorMatches(s, service.ID, service.ReqKey, service.Name, service.NameEN)
}

func (s luminaModelSelector) matchesVideo(schema lumina.VideoSchema) bool {
	return modelSelectorMatches(s, schema.ID, schema.ReqKey, schema.Name)
}

func modelSelectorMatches(selector luminaModelSelector, id, reqKey string, names ...string) bool {
	if selector.id != "" && selector.id == id {
		return true
	}
	if selector.reqKey != "" && strings.EqualFold(selector.reqKey, reqKey) {
		return true
	}
	for _, name := range names {
		if selector.name != "" && strings.EqualFold(selector.name, name) {
			return true
		}
	}
	candidates := append([]string{id, reqKey}, names...)
	requested := normalizeModelName(selector.requested)
	simplifiedRequested := simplifyModelName(selector.requested)
	for _, candidate := range candidates {
		if requested != "" && requested == normalizeModelName(candidate) {
			return true
		}
		if simplifiedRequested != "" && simplifiedRequested == simplifyModelName(candidate) {
			return true
		}
	}
	return false
}

func normalizeModelName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, prefix := range bytePlusBrandPrefixes {
		value = strings.TrimPrefix(value, prefix)
	}
	fields := strings.FieldsFunc(value, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(fields) > 0 && len(fields[len(fields)-1]) == 6 {
		if _, err := strconv.Atoi(fields[len(fields)-1]); err == nil {
			fields = fields[:len(fields)-1]
		}
	}
	return strings.Join(fields, "")
}

func simplifyModelName(value string) string {
	normalized := normalizeModelName(value)
	return strings.ReplaceAll(normalized, "pro", "")
}

func imageSchemaSupportsSize(schema *lumina.ImageServiceSchema, size string) bool {
	for _, field := range schema.Inputs {
		if field.Name != "size" && field.InternalName != "size" {
			continue
		}
		if strings.Contains(size, "x") {
			return true
		}
		enums, _ := field.Props["area_enums"].([]any)
		if len(enums) == 0 {
			return true
		}
		for _, item := range enums {
			entry, _ := item.(map[string]any)
			if strings.EqualFold(fmt.Sprint(entry["value"]), size) {
				return true
			}
		}
		return false
	}
	return false
}

func normalizeLuminaImageSize(size string) string {
	if strings.Contains(strings.ToLower(size), "x") {
		return strings.ToLower(size)
	}
	return strings.ToLower(strings.TrimSpace(size))
}

func videoPrompt(content []modelark.VideoContent) string {
	for _, item := range content {
		if item.Type == "text" {
			return item.Text
		}
	}
	return ""
}

func mapLuminaImageResponse(model string, task *lumina.Task, now time.Time) (*modelark.ImageGenerationResponse, error) {
	response := &modelark.ImageGenerationResponse{Model: model, Created: millisecondsToSeconds(task.CreatedAt, now)}
	var totalPixels int64
	for _, child := range task.Children {
		outputs := make([]lumina.TaskOutput, 0, 1+len(child.MultiOutputs))
		if child.Output != nil {
			outputs = append(outputs, *child.Output)
		}
		outputs = append(outputs, child.MultiOutputs...)
		for _, output := range outputs {
			if strings.TrimSpace(output.Value) == "" {
				continue
			}
			size := outputImageSize(output.MetaInfo)
			response.Data = append(response.Data, modelark.ImageGenerationData{
				URL:          output.Value,
				OutputFormat: luminaImageOutputFormat(output),
				Size:         size,
			})
			if pixels, ok := imagePixelCount(size); ok {
				totalPixels += pixels
			}
		}
	}
	if len(response.Data) == 0 {
		return nil, fmt.Errorf("lumina image task succeeded without output")
	}
	response.Usage.GeneratedImages = len(response.Data)
	if totalPixels > 0 {
		response.Usage.OutputTokens = int(math.Round(float64(totalPixels) / 256))
		response.Usage.TotalTokens = response.Usage.OutputTokens
	}
	return response, nil
}

type luminaImageDownloader interface {
	DownloadImage(ctx context.Context, rawURL string, maxBytes int64) (*lumina.DownloadedMedia, error)
}

func encodeLuminaImageResponse(ctx context.Context, downloader luminaImageDownloader, response *modelark.ImageGenerationResponse) error {
	if downloader == nil || response == nil {
		return fmt.Errorf("image response encoder is not configured")
	}
	for index := range response.Data {
		item := &response.Data[index]
		if strings.TrimSpace(item.URL) == "" {
			return fmt.Errorf("generated image %d has no download URL", index)
		}
		downloaded, err := downloader.DownloadImage(ctx, item.URL, luminaImageDownloadMax)
		if err != nil {
			return fmt.Errorf("download generated image %d: %w", index, err)
		}
		item.B64JSON = base64.StdEncoding.EncodeToString(downloaded.Data)
		item.URL = ""
		if outputFormat := modelArkImageFormat(downloaded.ContentType); outputFormat != "" {
			item.OutputFormat = outputFormat
		}
	}
	return nil
}

func luminaImageOutputFormat(output lumina.TaskOutput) string {
	for _, candidate := range []string{
		output.Format,
		fmt.Sprint(output.MetaInfo["output_format"]),
		fmt.Sprint(output.MetaInfo["format"]),
		imageFormatFromURL(output.Value),
	} {
		if normalized := modelArkImageFormat(candidate); normalized != "" {
			return normalized
		}
	}
	return ""
}

func modelArkImageFormat(value string) string {
	value = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(value, ".")))
	value = strings.TrimPrefix(value, "image/")
	switch value {
	case "jpg", "jpeg", "pjpeg":
		return "jpeg"
	case "png", "x-png":
		return "png"
	default:
		return ""
	}
}

func imageFormatFromURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return path.Ext(parsed.Path)
}

func imagePixelCount(size string) (int64, bool) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(size)), "x")
	if len(parts) != 2 {
		return 0, false
	}
	width, widthErr := strconv.ParseInt(parts[0], 10, 64)
	height, heightErr := strconv.ParseInt(parts[1], 10, 64)
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		return 0, false
	}
	return width * height, true
}

func modelArkVideoTaskFromStored(task *LuminaTask) *modelark.VideoTask {
	if task == nil {
		return nil
	}
	result := &modelark.VideoTask{
		ID:        task.TaskID,
		Model:     task.Model,
		Status:    task.Status,
		CreatedAt: task.CreatedAt.Unix(),
		UpdatedAt: task.UpdatedAt.Unix(),
	}
	if task.Status == LuminaTaskStatusSucceeded {
		framesPerSecond := 24
		result.FramesPerSecond = &framesPerSecond
	}
	if task.ErrorCode != nil || task.ErrorMessage != nil {
		result.Error = &modelark.VideoTaskError{Code: luminaStringValue(task.ErrorCode), Message: luminaStringValue(task.ErrorMessage)}
	}
	decodeStoredVideoRequest(task.RequestPayload, result)
	var upstream lumina.Task
	if mapToStruct(task.ResponsePayload, &upstream) == nil {
		for _, child := range upstream.Children {
			if child.Output == nil {
				continue
			}
			videoURL := luminaFirstNonEmpty(child.Output.VideoURL, child.Output.Value)
			if videoURL == "" {
				continue
			}
			result.Content = &modelark.VideoTaskContent{VideoURL: videoURL}
			break
		}
	}
	return result
}

func decodeStoredVideoRequest(payload map[string]any, result *modelark.VideoTask) {
	var request modelark.VideoGenerationRequest
	if result == nil || mapToStruct(payload, &request) != nil {
		return
	}
	result.Seed = request.Seed
	result.Resolution = request.Resolution
	result.Ratio = request.Ratio
	result.Duration = request.Duration
	result.Frames = request.Frames
	result.GenerateAudio = request.GenerateAudio
	result.SafetyIdentifier = request.SafetyIdentifier
	result.Priority = request.Priority
	result.Draft = request.Draft
	result.ServiceTier = request.ServiceTier
	result.ExecutionExpiresAfter = request.ExecutionExpiresAfter
}

// mapLuminaTaskStatus maps an upstream task status to a stored status. The
// second return value is false for unrecognized values: the captured BytePlus
// console protocol may add new statuses at any time, and an unknown status
// must never be persisted as a terminal state (which would also set
// CompletedAt and, for failures, surface a bogus error to the user).
func mapLuminaTaskStatus(status string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "queue", "queued", "pending", "waiting":
		return LuminaTaskStatusQueued, true
	case "running", "processing":
		return LuminaTaskStatusRunning, true
	case "complete", "completed", "partial_success", "succeeded", "success":
		return LuminaTaskStatusSucceeded, true
	case "fail", "failed", "failure", "error":
		return LuminaTaskStatusFailed, true
	case "cancel", "cancelled", "canceled":
		return LuminaTaskStatusCancelled, true
	case "timeout", "expired":
		return LuminaTaskStatusExpired, true
	default:
		return "", false
	}
}

func luminaTaskFailure(task *lumina.Task, mediaType string) (string, string) {
	if mediaType == "" {
		mediaType = LuminaTaskTypeVideo
	}
	failureMessage := "the " + mediaType + " generation task failed"
	if task == nil {
		return "InternalServiceError", failureMessage
	}
	for _, child := range task.Children {
		if strings.TrimSpace(child.FailReason) != "" {
			reason := strings.TrimSpace(child.FailReason)
			lowerReason := strings.ToLower(reason)
			if strings.Contains(lowerReason, "sensitive") || strings.Contains(lowerReason, "moderation") {
				if mediaType == LuminaTaskTypeImage {
					return "OutputImageSensitiveContentDetected", "the generated image may contain sensitive information"
				}
				return "OutputVideoSensitiveContentDetected", "the generated video may contain sensitive information"
			}
			return "InternalServiceError", failureMessage
		}
	}
	if status, recognized := mapLuminaTaskStatus(task.Status); recognized && status == LuminaTaskStatusFailed {
		return "InternalServiceError", failureMessage
	}
	return "", ""
}

func outputImageSize(meta map[string]any) string {
	width := intFromAny(meta["width"])
	height := intFromAny(meta["height"])
	if width <= 0 || height <= 0 {
		return ""
	}
	return strconv.Itoa(width) + "x" + strconv.Itoa(height)
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	default:
		parsed, _ := strconv.Atoi(fmt.Sprint(value))
		return parsed
	}
}

func luminaInputString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprint(value)
	}
}

func publicVideoParameterName(internal string) string {
	switch internal {
	case "frames":
		return "duration"
	case "aspect_ratio":
		return "ratio"
	case "generate_audio":
		return "generate_audio"
	default:
		return internal
	}
}

func structToMap(value any) map[string]any {
	payload, err := json.Marshal(value)
	if err != nil {
		return map[string]any{}
	}
	var result map[string]any
	if json.Unmarshal(payload, &result) != nil || result == nil {
		return map[string]any{}
	}
	return result
}

func mapToStruct(value map[string]any, destination any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, destination)
}

func millisecondsToSeconds(value int64, fallback time.Time) int64 {
	if value <= 0 {
		return fallback.Unix()
	}
	return value / 1000
}

func luminaFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stringPointer(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func luminaStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
