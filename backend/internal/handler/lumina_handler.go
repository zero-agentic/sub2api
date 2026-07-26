package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/lumina"
	"github.com/Wei-Shaw/sub2api/internal/pkg/modelark"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const maxLuminaAccountSelectionAttempts = 32

// ModelArkHandler owns the public BytePlus protocol. Provider-specific account
// behavior remains behind the injected gateway service.
type ModelArkHandler struct {
	lumina                   *service.LuminaGatewayService
	scheduler                *service.GatewayService
	concurrency              *service.ConcurrencyService
	billingCache             *service.BillingCacheService
	usage                    *service.OpenAIGatewayService
	apiKeyService            *service.APIKeyService
	contentModerationService *service.ContentModerationService
	securityAuditCoordinator *securityaudit.Coordinator
}

func NewModelArkHandler(
	luminaGateway *service.LuminaGatewayService,
	scheduler *service.GatewayService,
	concurrency *service.ConcurrencyService,
	billingCache *service.BillingCacheService,
	usage *service.OpenAIGatewayService,
	apiKeyService *service.APIKeyService,
	contentModerationService *service.ContentModerationService,
	coordinator *securityaudit.Coordinator,
) *ModelArkHandler {
	return &ModelArkHandler{
		lumina: luminaGateway, scheduler: scheduler, concurrency: concurrency, billingCache: billingCache,
		usage: usage, apiKeyService: apiKeyService, contentModerationService: contentModerationService,
		securityAuditCoordinator: coordinator,
	}
}

func (h *ModelArkHandler) GenerateImage(c *gin.Context) {
	apiKey, subject, subscription, ok := h.authorizeForWrite(c)
	if !ok {
		return
	}
	body, err := httputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		modelArkWriteError(c, err)
		return
	}
	var request modelark.ImageGenerationRequest
	if err := decodeStrictModelArkJSON(body, &request); err != nil {
		modelArkWriteError(c, modelark.InvalidParameter("body", "invalid JSON request: "+err.Error()))
		return
	}
	if err := request.Validate(); err != nil {
		modelArkWriteError(c, err)
		return
	}
	imageSize := request.Size
	if imageSize == "" {
		imageSize = service.ImageBillingSize2K
	}
	if h.usage == nil || !h.usage.HasImageGenerationPricing(c.Request.Context(), apiKey, request.Model, imageSize) {
		modelArkWriteError(c, service.ErrModelPricingUnavailable)
		return
	}
	if !h.runSecurityAudit(c, apiKey, subject, request.Model, body) {
		return
	}

	userRelease, ok := h.acquireUserConcurrency(c, apiKey)
	if !ok {
		return
	}
	defer userRelease()

	startedAt := time.Now()
	failedAccounts := make(map[int64]struct{})
	for attempt := 0; attempt < maxLuminaAccountSelectionAttempts; attempt++ {
		account, accountRelease, selectErr := h.selectLuminaAccount(c, apiKey, failedAccounts)
		if selectErr != nil {
			modelArkWriteError(c, selectErr)
			return
		}
		result, generateErr := func() (*modelark.ImageGenerationResponse, error) {
			defer accountRelease()
			return h.lumina.GenerateImage(c.Request.Context(), account, &request)
		}()
		if generateErr != nil {
			if shouldTryAnotherLuminaAccount(generateErr) {
				failedAccounts[account.ID] = struct{}{}
				continue
			}
			modelArkWriteError(c, generateErr)
			return
		}
		h.recordImageUsage(c, apiKey, subscription, account, &request, result, body, time.Since(startedAt))
		c.JSON(http.StatusOK, result)
		return
	}
	modelArkWriteError(c, service.ErrNoAvailableAccounts)
}

func (h *ModelArkHandler) CreateVideoTask(c *gin.Context) {
	apiKey, subject, _, ok := h.authorizeForWrite(c)
	if !ok {
		return
	}
	body, err := httputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		modelArkWriteError(c, err)
		return
	}
	var request modelark.VideoGenerationRequest
	if err := decodeStrictModelArkJSON(body, &request); err != nil {
		modelArkWriteError(c, modelark.InvalidParameter("body", "invalid JSON request: "+err.Error()))
		return
	}
	if err := request.Validate(); err != nil {
		modelArkWriteError(c, err)
		return
	}
	// Billing is bound to the terminal task state: creation only checks that
	// the normalized request is priced; the charge happens once the task
	// succeeds (see lumina_video_billing.go).
	normalized := service.NormalizeLuminaVideoRequest(&request)
	if h.usage == nil || !h.usage.HasVideoGenerationPricing(
		c.Request.Context(), apiKey, request.Model, normalized.Resolution, normalized.Ratio, *normalized.GenerateAudio,
	) {
		modelArkWriteError(c, service.ErrModelPricingUnavailable)
		return
	}
	if !h.runSecurityAudit(c, apiKey, subject, request.Model, body) {
		return
	}

	userRelease, ok := h.acquireUserConcurrency(c, apiKey)
	if !ok {
		return
	}
	defer userRelease()

	owner := service.LuminaTaskOwnership{UserID: subject.UserID, APIKeyID: apiKey.ID, GroupID: apiKey.Group.ID}
	failedAccounts := make(map[int64]struct{})
	for attempt := 0; attempt < maxLuminaAccountSelectionAttempts; attempt++ {
		account, accountRelease, selectErr := h.selectLuminaAccount(c, apiKey, failedAccounts)
		if selectErr != nil {
			modelArkWriteError(c, selectErr)
			return
		}
		result, createErr := func() (*modelark.CreateVideoTaskResponse, error) {
			defer accountRelease()
			return h.lumina.CreateVideoTask(c.Request.Context(), account, owner, &request)
		}()
		if createErr != nil {
			if shouldTryAnotherLuminaAccount(createErr) {
				failedAccounts[account.ID] = struct{}{}
				continue
			}
			modelArkWriteError(c, createErr)
			return
		}
		c.JSON(http.StatusOK, result)
		return
	}
	modelArkWriteError(c, service.ErrNoAvailableAccounts)
}

func (h *ModelArkHandler) GetVideoTask(c *gin.Context) {
	apiKey, subject, _, ok := h.authorizeForRead(c)
	if !ok {
		return
	}
	result, err := h.lumina.GetVideoTask(c.Request.Context(), service.LuminaTaskOwnership{
		UserID: subject.UserID, APIKeyID: apiKey.ID, GroupID: apiKey.Group.ID,
	}, c.Param("id"))
	if err != nil {
		modelArkWriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *ModelArkHandler) ListVideoTasks(c *gin.Context) {
	apiKey, subject, _, ok := h.authorizeForRead(c)
	if !ok {
		return
	}
	pageNum, err := parseBoundedQueryInt(c, "page_num", 1, 500, 1)
	if err != nil {
		modelArkWriteError(c, err)
		return
	}
	pageSize, err := parseBoundedQueryInt(c, "page_size", 1, 500, 20)
	if err != nil {
		modelArkWriteError(c, err)
		return
	}
	status := strings.TrimSpace(c.Query("filter.status"))
	if status != "" && !validModelArkTaskStatus(status) {
		modelArkWriteError(c, modelark.InvalidParameter("filter.status", "filter.status is not supported"))
		return
	}
	serviceTier := strings.TrimSpace(c.Query("filter.service_tier"))
	if serviceTier != "" && serviceTier != "default" {
		modelArkWriteError(c, modelark.UnsupportedParameter("filter.service_tier", "only filter.service_tier=default is supported"))
		return
	}
	filter := service.LuminaTaskFilter{
		Status:  status,
		Model:   strings.TrimSpace(c.Query("filter.model")),
		TaskIDs: compactQueryValues(c.QueryArray("filter.task_ids")),
		Limit:   pageSize,
		Offset:  (pageNum - 1) * pageSize,
	}
	result, err := h.lumina.ListVideoTasks(c.Request.Context(), service.LuminaTaskOwnership{
		UserID: subject.UserID, APIKeyID: apiKey.ID, GroupID: apiKey.Group.ID,
	}, filter)
	if err != nil {
		modelArkWriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *ModelArkHandler) DeleteVideoTask(c *gin.Context) {
	apiKey, subject, _, ok := h.authorizeForRead(c)
	if !ok {
		return
	}
	err := h.lumina.DeleteVideoTask(c.Request.Context(), service.LuminaTaskOwnership{
		UserID: subject.UserID, APIKeyID: apiKey.ID, GroupID: apiKey.Group.ID,
	}, c.Param("id"))
	if err != nil {
		modelArkWriteError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// authorizeForWrite authorizes generation endpoints: platform/group checks
// plus billing eligibility, since these paths create billable work.
func (h *ModelArkHandler) authorizeForWrite(c *gin.Context) (*service.APIKey, middleware.AuthSubject, *service.UserSubscription, bool) {
	return h.authorize(c, true)
}

// authorizeForRead authorizes task query/cancel endpoints: platform/group
// checks only, so an insufficient balance never blocks users from inspecting
// or cancelling tasks they already created.
func (h *ModelArkHandler) authorizeForRead(c *gin.Context) (*service.APIKey, middleware.AuthSubject, *service.UserSubscription, bool) {
	return h.authorize(c, false)
}

func (h *ModelArkHandler) authorize(c *gin.Context, requireBilling bool) (*service.APIKey, middleware.AuthSubject, *service.UserSubscription, bool) {
	apiKey, ok := middleware.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil || apiKey.User == nil {
		modelArkWriteError(c, infraerrors.New(http.StatusUnauthorized, "APIKeyRequired", "API key is required"))
		return nil, middleware.AuthSubject{}, nil, false
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		modelArkWriteError(c, infraerrors.New(http.StatusUnauthorized, "APIKeyRequired", "API key user context is missing"))
		return nil, middleware.AuthSubject{}, nil, false
	}
	if apiKey.GroupID == nil || apiKey.Group == nil || apiKey.Group.ID <= 0 {
		modelArkWriteError(c, infraerrors.New(http.StatusForbidden, "GroupRequired", "API key must be assigned to a group"))
		return nil, middleware.AuthSubject{}, nil, false
	}
	if apiKey.Group.Platform != service.PlatformLumina {
		modelArkWriteError(c, infraerrors.New(http.StatusForbidden, "InvalidGroupPlatform", "API key group is not a Lumina group"))
		return nil, middleware.AuthSubject{}, nil, false
	}
	if !apiKey.Group.AllowImageGeneration {
		modelArkWriteError(c, infraerrors.New(http.StatusForbidden, "MediaGenerationDisabled", "media generation is disabled for this group"))
		return nil, middleware.AuthSubject{}, nil, false
	}
	subscription, _ := middleware.GetSubscriptionFromContext(c)
	if requireBilling && h.billingCache != nil {
		if err := h.billingCache.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.PlatformLumina); err != nil {
			modelArkWriteError(c, err)
			return nil, middleware.AuthSubject{}, nil, false
		}
	}
	return apiKey, subject, subscription, true
}

func (h *ModelArkHandler) runSecurityAudit(c *gin.Context, apiKey *service.APIKey, subject middleware.AuthSubject, model string, body []byte) bool {
	reqLog := requestLogger(c, "handler.lumina.security_audit", zap.Int64("user_id", subject.UserID), zap.Int64("api_key_id", apiKey.ID), zap.String("model", model))
	decision := runSecurityAudit(c, reqLog, h.securityAuditCoordinator, h.contentModerationService, apiKey, subject, "modelark", model, body, "http")
	if decision == nil || decision.AllowNextStage {
		return true
	}
	status, code := modelArkSecurityAuditError(decision)
	modelArkWriteErrorDetail(c, status, code, securityAuditMessage(decision), "prompt")
	return false
}

func modelArkSecurityAuditError(decision *securityaudit.Decision) (int, string) {
	status := securityAuditStatus(decision)
	switch status {
	case http.StatusTooManyRequests:
		return status, "ServerOverloaded"
	case http.StatusBadRequest, http.StatusForbidden:
		return http.StatusBadRequest, "InputTextSensitiveContentDetected"
	default:
		return http.StatusInternalServerError, "InternalServiceError"
	}
}

func (h *ModelArkHandler) acquireUserConcurrency(c *gin.Context, apiKey *service.APIKey) (func(), bool) {
	if h.concurrency == nil {
		return func() {}, true
	}
	trackedRelease := h.concurrency.TrackAPIKeySlot(c.Request.Context(), apiKey.ID)
	result, err := h.concurrency.AcquireUserSlot(c.Request.Context(), apiKey.User.ID, apiKey.User.Concurrency)
	if err != nil || result == nil || !result.Acquired {
		trackedRelease()
		if err == nil {
			err = infraerrors.New(http.StatusTooManyRequests, "UserConcurrencyExceeded", "user concurrency limit exceeded")
		}
		modelArkWriteError(c, err)
		return nil, false
	}
	return func() {
		result.ReleaseFunc()
		trackedRelease()
	}, true
}

func (h *ModelArkHandler) selectLuminaAccount(c *gin.Context, apiKey *service.APIKey, excluded map[int64]struct{}) (*service.Account, func(), error) {
	if h.scheduler == nil {
		return nil, nil, service.ErrNoAvailableAccounts
	}
	ctx := c.Request.Context()
	for attempt := 0; attempt < maxLuminaAccountSelectionAttempts; attempt++ {
		account, err := h.scheduler.SelectAccountForModelWithExclusions(ctx, apiKey.GroupID, "", "", excluded)
		if err != nil {
			return nil, nil, err
		}
		if account == nil || !account.IsLuminaCookie() {
			if account != nil {
				excluded[account.ID] = struct{}{}
			}
			continue
		}
		setOpsSelectedAccount(c, account.ID, account.Platform)
		if h.concurrency == nil {
			return account, func() {}, nil
		}
		slot, err := h.concurrency.AcquireAccountSlot(ctx, account.ID, account.Concurrency)
		if err != nil {
			return nil, nil, err
		}
		if slot != nil && slot.Acquired {
			return account, slot.ReleaseFunc, nil
		}
		excluded[account.ID] = struct{}{}
	}
	return nil, nil, service.ErrNoAvailableAccounts
}

func (h *ModelArkHandler) recordImageUsage(c *gin.Context, apiKey *service.APIKey, subscription *service.UserSubscription, account *service.Account, request *modelark.ImageGenerationRequest, response *modelark.ImageGenerationResponse, body []byte, duration time.Duration) {
	if h.usage == nil || response == nil {
		return
	}
	outputSizes := make([]string, 0, len(response.Data))
	for _, item := range response.Data {
		if item.Size != "" {
			outputSizes = append(outputSizes, item.Size)
		}
	}
	result := &service.OpenAIForwardResult{
		RequestID: requestIDFromContext(c.Request.Context()), Model: request.Model, UpstreamModel: request.Model,
		Duration: duration, ImageCount: len(response.Data), ImageInputCount: len(request.Image),
		ImageInputSize: request.Size, ImageOutputSizes: outputSizes,
	}
	h.recordUsage(c, apiKey, subscription, account, result, body, "/api/v3/images/generations", "/api/inference/v2/create_task")
}

func (h *ModelArkHandler) recordUsage(c *gin.Context, apiKey *service.APIKey, subscription *service.UserSubscription, account *service.Account, result *service.OpenAIForwardResult, body []byte, inbound, upstream string) {
	err := h.usage.RecordUsage(c.Request.Context(), &service.OpenAIRecordUsageInput{
		Result: result, APIKey: apiKey, User: apiKey.User, Account: account, Subscription: subscription,
		InboundEndpoint: inbound, UpstreamEndpoint: upstream, UserAgent: c.GetHeader("User-Agent"), IPAddress: c.ClientIP(),
		RequestPayloadHash: service.HashUsageRequestPayload(body), APIKeyService: h.apiKeyService, QuotaPlatform: service.PlatformLumina,
	})
	if err != nil {
		logger.L().Error("lumina.usage_record_failed", zap.Int64("api_key_id", apiKey.ID), zap.Int64("account_id", account.ID), zap.String("request_id", result.RequestID), zap.Error(err))
	}
}

func decodeStrictModelArkJSON(body []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain a single JSON object")
		}
		return err
	}
	return nil
}

func modelArkWriteError(c *gin.Context, err error) {
	if err == nil {
		return
	}
	if maxErr, ok := extractMaxBytesError(err); ok {
		modelArkWriteErrorDetail(c, http.StatusBadRequest, "InvalidParameter", buildBodyTooLargeMessage(maxErr.Limit), "body")
		return
	}
	if parameterErr, ok := modelark.AsParameterError(err); ok {
		status := parameterErr.Status
		if status == 0 {
			status = http.StatusBadRequest
		}
		modelArkWriteErrorDetail(c, status, parameterErr.Code, parameterErr.Message, parameterErr.Param)
		return
	}
	if errors.Is(err, service.ErrLuminaTaskNotFound) {
		modelArkWriteErrorDetail(c, http.StatusNotFound, "NotFound.id", "the specified video generation task was not found", "id")
		return
	}
	if errors.Is(err, service.ErrNoAvailableAccounts) {
		modelArkWriteErrorDetail(c, http.StatusTooManyRequests, "ServerOverloaded", "the service is currently unable to handle additional requests; retry later", "")
		return
	}
	if errors.Is(err, service.ErrLuminaSessionUnavailable) {
		modelArkWriteErrorDetail(c, http.StatusInternalServerError, "InternalServiceError", "the service encountered an internal error; retry later", "")
		return
	}
	if lumina.IsInteractiveAuthError(err) || lumina.IsCredentialAuthError(err) || errors.Is(err, service.ErrLuminaAccountIdentityChanged) {
		modelArkWriteErrorDetail(c, http.StatusInternalServerError, "InternalServiceError", "the service encountered an internal error; retry later", "")
		return
	}
	var upstreamErr *lumina.APIError
	if errors.As(err, &upstreamErr) {
		status, code, message := modelArkUpstreamError(upstreamErr)
		modelArkWriteErrorDetail(c, status, code, message, "")
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		modelArkWriteErrorDetail(c, http.StatusInternalServerError, "InternalServiceError", "the service encountered an internal error; retry later", "")
		return
	}
	status := infraerrors.Code(err)
	if status < 400 || status > 599 {
		status = http.StatusInternalServerError
	}
	message := infraerrors.Message(err)
	if message == "" {
		message = "the service encountered an internal error; retry later"
	}
	normalizedStatus, code := modelark.NormalizeError(status, infraerrors.Reason(err))
	if code == "InternalServiceError" {
		message = "the service encountered an internal error; retry later"
	}
	modelArkWriteErrorDetail(c, normalizedStatus, code, message, "")
}

func modelArkWriteErrorDetail(c *gin.Context, status int, code, message, param string) {
	c.JSON(status, modelark.NewErrorResponse(status, code, message, param))
}

func modelArkUpstreamError(err *lumina.APIError) (int, string, string) {
	if err == nil {
		return http.StatusInternalServerError, "InternalServiceError", "the service encountered an internal error; retry later"
	}
	codeAndMessage := strings.ToLower(err.Code + " " + err.Message)
	switch {
	case strings.Contains(codeAndMessage, "input") && strings.Contains(codeAndMessage, "image") && strings.Contains(codeAndMessage, "sensitive"):
		return http.StatusBadRequest, "InputImageSensitiveContentDetected", "the request failed because the input image may contain sensitive information"
	case strings.Contains(codeAndMessage, "output") && strings.Contains(codeAndMessage, "image") && strings.Contains(codeAndMessage, "sensitive"):
		return http.StatusBadRequest, "OutputImageSensitiveContentDetected", "the request failed because the generated image may contain sensitive information"
	case strings.Contains(codeAndMessage, "output") && strings.Contains(codeAndMessage, "video") && strings.Contains(codeAndMessage, "sensitive"):
		return http.StatusBadRequest, "OutputVideoSensitiveContentDetected", "the request failed because the generated video may contain sensitive information"
	case strings.Contains(codeAndMessage, "sensitive") || strings.Contains(codeAndMessage, "moderation"):
		return http.StatusBadRequest, "SensitiveContentDetected", "the request failed because the input may contain sensitive information"
	case err.Status == http.StatusTooManyRequests || strings.Contains(codeAndMessage, "quota") || strings.Contains(codeAndMessage, "limit"):
		return http.StatusTooManyRequests, "QuotaExceeded", "the request has exceeded the available quota; retry later"
	case err.Status == http.StatusBadRequest:
		return http.StatusBadRequest, "InvalidParameter", "one or more request parameters are invalid"
	default:
		return http.StatusInternalServerError, "InternalServiceError", "the service encountered an internal error; retry later"
	}
}

func shouldTryAnotherLuminaAccount(err error) bool {
	if lumina.IsAuthenticationError(err) || lumina.IsCredentialAuthError(err) || lumina.IsInteractiveAuthError(err) ||
		errors.Is(err, service.ErrLuminaSessionUnavailable) || errors.Is(err, service.ErrLuminaAccountIdentityChanged) {
		return true
	}
	parameterErr, ok := modelark.AsParameterError(err)
	return ok && parameterErr.Param == "model"
}

func parseBoundedQueryInt(c *gin.Context, key string, min, max, fallback int) (int, error) {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < min || value > max {
		return 0, modelark.InvalidParameter(key, fmt.Sprintf("%s must be between %d and %d", key, min, max))
	}
	return value, nil
}

func validModelArkTaskStatus(status string) bool {
	switch status {
	case service.LuminaTaskStatusQueued, service.LuminaTaskStatusRunning, service.LuminaTaskStatusCancelled,
		service.LuminaTaskStatusSucceeded, service.LuminaTaskStatusFailed, service.LuminaTaskStatusExpired:
		return true
	default:
		return false
	}
}

func compactQueryValues(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func requestIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(ctxkey.RequestID).(string)
	return strings.TrimSpace(value)
}

