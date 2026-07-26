package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/modelark"
	"go.uber.org/zap"
)

// LuminaVideoTaskBiller records usage for a lumina video task that reached the
// succeeded terminal state. The gateway invokes it exactly once per task, from
// the caller that applied the guarded terminal transition.
type LuminaVideoTaskBiller interface {
	RecordVideoTaskSuccess(ctx context.Context, task *LuminaTask)
}

// luminaVideoUsageBiller bills a succeeded video task against the normalized
// request stored at creation time. Billing is bound to the terminal state
// instead of task creation, so failed, cancelled, or expired tasks are never
// charged. The captured protocol does not report an actual rendered duration,
// so the billed duration comes from the stored (validated) request.
type luminaVideoUsageBiller struct {
	usage         *OpenAIGatewayService
	apiKeyService *APIKeyService
	subscriptions *SubscriptionService
	accountRepo   AccountRepository
}

func NewLuminaVideoUsageBiller(
	usage *OpenAIGatewayService,
	apiKeyService *APIKeyService,
	subscriptions *SubscriptionService,
	accountRepo AccountRepository,
) LuminaVideoTaskBiller {
	return &luminaVideoUsageBiller{
		usage:         usage,
		apiKeyService: apiKeyService,
		subscriptions: subscriptions,
		accountRepo:   accountRepo,
	}
}

func (b *luminaVideoUsageBiller) RecordVideoTaskSuccess(ctx context.Context, task *LuminaTask) {
	if b == nil || b.usage == nil || b.apiKeyService == nil || task == nil {
		return
	}
	logFields := []zap.Field{zap.String("task_id", task.TaskID), zap.Int64("api_key_id", task.APIKeyID), zap.Int64("account_id", task.AccountID)}
	apiKey, err := b.apiKeyService.GetByID(ctx, task.APIKeyID)
	if err != nil || apiKey == nil || apiKey.User == nil || apiKey.Group == nil {
		logger.L().Error("lumina.video_billing_api_key_unavailable", append(logFields, zap.Error(err))...)
		return
	}
	// Mirrors the auth middleware: subscription groups must bill against the
	// active subscription; without one there is nothing valid to charge.
	var subscription *UserSubscription
	if apiKey.Group.IsSubscriptionType() {
		if b.subscriptions == nil {
			logger.L().Error("lumina.video_billing_subscription_unavailable", logFields...)
			return
		}
		subscription, err = b.subscriptions.GetActiveSubscription(ctx, task.UserID, task.GroupID)
		if err != nil || subscription == nil {
			logger.L().Error("lumina.video_billing_subscription_unavailable", append(logFields, zap.Error(err))...)
			return
		}
	}
	account := b.billingAccount(ctx, task.AccountID)

	var request modelark.VideoGenerationRequest
	_ = mapToStruct(task.RequestPayload, &request)
	if request.Model == "" {
		request.Model = task.Model
	}
	normalized := NormalizeLuminaVideoRequest(&request)
	// NormalizeLuminaVideoRequest always yields a validated non-nil Duration
	// and GenerateAudio; the billed duration comes from the stored request.
	durationSeconds := *normalized.Duration
	generateAudio := *normalized.GenerateAudio
	var elapsed time.Duration
	if task.CompletedAt != nil {
		elapsed = task.CompletedAt.Sub(task.CreatedAt)
	}
	payload, _ := json.Marshal(task.RequestPayload)
	result := &OpenAIForwardResult{
		RequestID: task.TaskID, Model: task.Model, UpstreamModel: task.Model, Duration: elapsed,
		VideoCount: 1, VideoResolution: normalized.Resolution, VideoRatio: normalized.Ratio,
		VideoGenerateAudio: &generateAudio, VideoDurationSeconds: durationSeconds,
	}
	err = b.usage.RecordUsage(ctx, &OpenAIRecordUsageInput{
		Result: result, APIKey: apiKey, User: apiKey.User, Account: account, Subscription: subscription,
		InboundEndpoint: "/api/v3/contents/generations/tasks", UpstreamEndpoint: "/api/inference/create_task",
		RequestPayloadHash: HashUsageRequestPayload(payload), APIKeyService: b.apiKeyService, QuotaPlatform: PlatformLumina,
	})
	if err != nil {
		logger.L().Error("lumina.video_billing_record_failed", append(logFields, zap.Error(err))...)
	}
}

func (b *luminaVideoUsageBiller) billingAccount(ctx context.Context, accountID int64) *Account {
	if b.accountRepo != nil && accountID > 0 {
		if account, err := b.accountRepo.GetByID(ctx, accountID); err == nil && account != nil {
			return account
		}
	}
	// The account may have been deleted after task creation; keep channel
	// attribution on the recorded ID instead of dropping the charge.
	return &Account{ID: accountID, Platform: PlatformLumina}
}
