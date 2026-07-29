package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/lumina"
	"github.com/Wei-Shaw/sub2api/internal/pkg/modelark"
	"go.uber.org/zap"
)

// LuminaMediaKind names the half of the catalog a public model ID belongs to. A
// Lumina account exposes two disjoint prompt-driven catalogs — text-to-image
// services and text-to-video schemas — and nothing in the model ID itself says
// which one applies, so every probe classifies against the live catalog first.
type LuminaMediaKind string

const (
	LuminaMediaKindImage LuminaMediaKind = "image"
	LuminaMediaKindVideo LuminaMediaKind = "video"
)

// luminaProbeVideoTimeout bounds how long one probe waits for a video task.
// Generation continues upstream past the deadline, so the probe reports the
// pending task instead of holding the operator's request open for the full run.
const luminaProbeVideoTimeout = 3 * time.Minute

// LuminaMediaProbe describes one account-test generation.
type LuminaMediaProbe struct {
	// Model is the client-facing model ID, exactly as the account's model
	// mapping exposes it.
	Model string
	// Prompt drives the generation; both catalog halves are prompt-only.
	Prompt string
	// OnProgress, when set, receives operator-facing progress lines while the
	// probe runs. It fires on every upstream poll and therefore repeats the same
	// text while a status persists: video generation takes minutes, and the
	// caller needs those repeats both to show progress and to keep a streaming
	// response from idling out. Callers dedupe what they display.
	OnProgress func(text string)
}

// LuminaProbeImage carries one generated image inline: Lumina's media URLs are
// scoped to the account's authenticated session, so an operator's browser
// cannot load them directly.
type LuminaProbeImage struct {
	Base64   string
	MimeType string
}

// LuminaMediaProbeResult reports what one probe produced. Image probes fill
// Images; video probes fill VideoURL, or set Pending when the task outlived the
// probe window.
type LuminaMediaProbeResult struct {
	Kind LuminaMediaKind
	// Model is the catalog display name of the resolved service or schema.
	Model    string
	Images   []LuminaProbeImage
	VideoURL string
	// TaskID is the upstream task ID of a video probe, so an operator can find a
	// generation that outlived the probe window.
	TaskID string
	// Pending marks a video task still running when the probe window closed.
	Pending bool
}

func (p LuminaMediaProbe) reportProgress(text string) {
	if p.OnProgress != nil && strings.TrimSpace(text) != "" {
		p.OnProgress(text)
	}
}

// ProbeMediaModel classifies the requested model against the account's live
// catalog and runs the matching generation. Probes deliberately accept paused
// accounts: finding out whether the upstream still works is the whole point of
// an account test.
func (s *LuminaGatewayService) ProbeMediaModel(
	ctx context.Context,
	account *Account,
	probe LuminaMediaProbe,
) (*LuminaMediaProbeResult, error) {
	if s == nil {
		return nil, ErrLuminaSessionUnavailable
	}
	probe.Model = strings.TrimSpace(probe.Model)
	probe.Prompt = strings.TrimSpace(probe.Prompt)
	if probe.Model == "" {
		return nil, modelark.MissingParameter("model", "a test model is required")
	}
	if probe.Prompt == "" {
		return nil, modelark.MissingParameter("prompt", "a text prompt is required to test an image or video model")
	}
	catalog, err := s.catalogForAccount(ctx, account, true)
	if err != nil {
		return nil, err
	}
	kind, name, err := classifyLuminaCatalogModel(account, probe.Model, catalog.images, catalog.videos)
	if err != nil {
		return nil, err
	}
	probe.reportProgress("Resolved " + string(kind) + " model \"" + name + "\"")
	if kind == LuminaMediaKindImage {
		result, probeErr := s.probeImageModel(ctx, account, probe, name)
		if probeErr != nil {
			logLuminaProbeFailure(probe.Model, kind, name, probeErr)
			return nil, probeErr
		}
		return result, nil
	}
	result, probeErr := s.probeVideoModel(ctx, account, probe, name)
	if probeErr != nil {
		logLuminaProbeFailure(probe.Model, kind, name, probeErr)
		return nil, probeErr
	}
	return result, nil
}

// logLuminaProbeFailure records the unredacted probe failure. Operators only see
// the sanitized text (an upstream code at best), so without this the actual
// upstream reason for a failed account test is lost entirely.
func logLuminaProbeFailure(model string, kind LuminaMediaKind, catalogName string, err error) {
	logger.L().Warn("lumina.media_probe_failed",
		zap.String("model", model),
		zap.String("kind", string(kind)),
		zap.String("catalog_name", catalogName),
		zap.Error(err),
	)
}

// classifyLuminaCatalogModel reports which catalog half the model resolves to,
// together with the catalog's display name for it. Resolution goes through the
// very same resolvers the gateway uses, so a probe can never be classified onto
// a service the real generation would not pick. A model in neither half is
// rejected instead of guessed: guessing would send a prompt to the wrong
// endpoint and burn a real generation on a request upstream cannot answer.
//
// Prompt-only capability is deliberately not re-checked here. The test model
// list is built from model sync, which already keeps prompt-only entries, and
// the input builders reject a schema without a prompt field with a specific
// reason — while capability inference on an unparseable schema fails closed,
// which would hide a model that actually works.
func classifyLuminaCatalogModel(
	account *Account,
	model string,
	images []lumina.ImageService,
	videos []lumina.VideoSchema,
) (LuminaMediaKind, string, error) {
	if imageService, err := resolveLuminaImageService(account, model, images); err == nil {
		// Name carries the product label ("GPT Image 2 （Beta）"); name_en is often a
		// lowercased internal string, so it is only a fallback.
		name := luminaFirstNonEmpty(imageService.Name, imageService.NameEN, imageService.ReqKey, imageService.ID)
		return LuminaMediaKindImage, name, nil
	}
	// resolveLuminaVideoSchema keeps text-to-video schemas only, so an image-driven
	// video schema never reaches the video probe.
	if schema, err := resolveLuminaVideoSchema(account, &modelark.VideoGenerationRequest{Model: model}, videos); err == nil {
		name := luminaFirstNonEmpty(schema.Name, schema.ReqKey, schema.ID)
		return LuminaMediaKindVideo, name, nil
	}
	return "", "", modelark.ModelNotFound("the selected model is not a text-to-image or text-to-video model in this account's Lumina catalog")
}

func (s *LuminaGatewayService) probeImageModel(
	ctx context.Context,
	account *Account,
	probe LuminaMediaProbe,
	modelName string,
) (*LuminaMediaProbeResult, error) {
	response, err := s.generateImage(ctx, account, &modelark.ImageGenerationRequest{
		Model:          probe.Model,
		Prompt:         probe.Prompt,
		ResponseFormat: "b64_json",
	}, luminaGenerateOptions{
		allowInactive: true,
		onPoll:        func(status string) { probe.reportProgress("Image task " + status) },
	})
	if err != nil {
		return nil, err
	}
	result := &LuminaMediaProbeResult{Kind: LuminaMediaKindImage, Model: modelName}
	for _, item := range response.Data {
		if strings.TrimSpace(item.B64JSON) == "" {
			continue
		}
		result.Images = append(result.Images, LuminaProbeImage{
			Base64:   item.B64JSON,
			MimeType: luminaProbeImageMIMEType(item.OutputFormat),
		})
	}
	if len(result.Images) == 0 {
		return nil, errors.New("lumina image probe returned no inline image data")
	}
	return result, nil
}

func (s *LuminaGatewayService) probeVideoModel(
	ctx context.Context,
	account *Account,
	probe LuminaMediaProbe,
	modelName string,
) (*LuminaMediaProbeResult, error) {
	upstreamRequest, _, err := s.prepareVideoTask(ctx, account, newLuminaProbeVideoRequest(probe.Model, probe.Prompt), true)
	if err != nil {
		return nil, err
	}
	var created *lumina.CreateTaskResponse
	account, err = s.withAuthenticatedClientMode(ctx, account, true, func(client *lumina.Client) error {
		var createErr error
		created, createErr = client.CreateVideoTask(ctx, upstreamRequest)
		return createErr
	})
	if err != nil {
		return nil, err
	}
	result := &LuminaMediaProbeResult{Kind: LuminaMediaKindVideo, Model: modelName, TaskID: created.TaskID()}
	probe.reportProgress("Upstream video task created: " + result.TaskID)

	// No stored task is created: an account test is not a billable gateway
	// request, and the reconciler is only responsible for tasks a real owner
	// submitted. The upstream task is likewise left alone on timeout — stopping
	// it would waste a generation the account has already paid for.
	task, err := s.pollTask(ctx, account, result.TaskID, luminaTaskPollOptions{
		mediaType:     LuminaTaskTypeVideo,
		timeout:       luminaProbeVideoTimeout,
		allowInactive: true,
		onPoll:        func(status string) { probe.reportProgress("Video task " + status) },
	})
	if err != nil {
		// Only the probe's own deadline means "still generating"; a cancelled
		// caller context is a real abort and has to surface as a failure.
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			result.Pending = true
			return result, nil
		}
		return nil, err
	}
	result.VideoURL = luminaTaskVideoURL(task)
	if result.VideoURL == "" {
		return nil, errors.New("lumina video probe succeeded without output")
	}
	return result, nil
}

// newLuminaProbeVideoRequest builds the least demanding request every
// text-to-video schema can accept: a prompt plus the watermark the provider
// mandates. Nothing else is set, so NormalizeLuminaVideoRequest stays the single
// source of resolution, ratio, duration and audio defaults, and a schema that
// does not declare one of them drops it silently instead of failing the probe.
func newLuminaProbeVideoRequest(model, prompt string) *modelark.VideoGenerationRequest {
	watermark := true
	return &modelark.VideoGenerationRequest{
		Model:     model,
		Content:   []modelark.VideoContent{{Type: "text", Text: prompt}},
		Watermark: &watermark,
	}
}

// luminaProbeImageMIMEType maps a ModelArk output format onto the MIME type of
// the data URL an operator's browser renders. Lumina only ever returns PNG or
// JPEG, and PNG is the safe default when the format is missing.
func luminaProbeImageMIMEType(outputFormat string) string {
	if strings.EqualFold(strings.TrimSpace(outputFormat), "jpeg") {
		return "image/jpeg"
	}
	return "image/png"
}
