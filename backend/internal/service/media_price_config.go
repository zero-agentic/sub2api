package service

import (
	"context"
	"strings"
)

func imagePriceConfigFromAPIKey(apiKey *APIKey) *ImagePriceConfig {
	if apiKey == nil || apiKey.Group == nil {
		return nil
	}
	return &ImagePriceConfig{
		Price1K: apiKey.Group.ImagePrice1K,
		Price2K: apiKey.Group.ImagePrice2K,
		Price4K: apiKey.Group.ImagePrice4K,
	}
}

func apiKeyHasConfiguredImagePrice(apiKey *APIKey, imageSize string) bool {
	return apiKey != nil && apiKey.Group != nil && apiKey.Group.GetImagePrice(imageSize) != nil
}

func videoPriceConfigFromAPIKey(apiKey *APIKey) *VideoPriceConfig {
	if apiKey == nil || apiKey.Group == nil {
		return nil
	}
	return &VideoPriceConfig{
		Price480P:  apiKey.Group.VideoPrice480P,
		Price720P:  apiKey.Group.VideoPrice720P,
		Price1080P: apiKey.Group.VideoPrice1080P,
		Price4K:    apiKey.Group.VideoPrice4K,
	}
}

func apiKeyHasConfiguredVideoPrice(apiKey *APIKey, resolution string) bool {
	return apiKey != nil && apiKey.Group != nil && apiKey.Group.GetVideoPrice(resolution) != nil
}

// HasImageGenerationPricing verifies that the requested image generation can
// be charged before an upstream request is started. Selling-price overrides
// take precedence over the BytePlus catalog.
func (s *OpenAIGatewayService) HasImageGenerationPricing(ctx context.Context, apiKey *APIKey, model, imageSize string) bool {
	apiKey = s.apiKeyWithFreshGroupMediaPricing(ctx, apiKey)
	if apiKeyHasConfiguredImagePrice(apiKey, imageSize) {
		return true
	}
	if resolved := s.resolveOpenAIChannelPricing(ctx, model, apiKey); resolvedChannelPricingCoversTier(resolved, NormalizeImageBillingTierOrDefault(imageSize)) {
		return true
	}
	return HasBytePlusOfficialImagePricing(model)
}

// HasVideoGenerationPricing is the video counterpart of
// HasImageGenerationPricing. The official lookup validates the requested
// model, resolution, ratio, and audio pricing combination.
func (s *OpenAIGatewayService) HasVideoGenerationPricing(
	ctx context.Context,
	apiKey *APIKey,
	model string,
	resolution string,
	ratio string,
	generateAudio bool,
) bool {
	apiKey = s.apiKeyWithFreshGroupMediaPricing(ctx, apiKey)
	if apiKeyHasConfiguredVideoPrice(apiKey, resolution) {
		return true
	}
	if resolved := s.resolveOpenAIChannelPricing(ctx, model, apiKey); resolvedChannelPricingCoversTier(resolved, NormalizeVideoBillingResolutionOrDefault(resolution)) {
		return true
	}
	return HasBytePlusOfficialVideoPricing(model, resolution, ratio, generateAudio)
}

func resolvedChannelPricingCoversTier(resolved *ResolvedPricing, tier string) bool {
	if resolved == nil || resolved.Source != PricingSourceChannel ||
		(resolved.Mode != BillingModePerRequest && resolved.Mode != BillingModeImage) {
		return false
	}
	if resolved.channelPricing != nil && resolved.channelPricing.PerRequestPrice != nil {
		return true
	}
	for _, interval := range resolved.RequestTiers {
		if interval.PerRequestPrice != nil && strings.EqualFold(strings.TrimSpace(interval.TierLabel), tier) {
			return true
		}
	}
	return false
}

func webSearchPricePerCallFromAPIKey(apiKey *APIKey) *float64 {
	if apiKey == nil || apiKey.Group == nil {
		return nil
	}
	return apiKey.Group.WebSearchPricePerCall
}
