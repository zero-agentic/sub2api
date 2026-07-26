package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestBytePlusPricingModelAliases(t *testing.T) {
	for _, model := range []string{
		"dola-seedream-5-0-pro-260628",
		"ByteDance-Seedream-5.0-Pro",
		"doubao-dola-seedream_5_0_pro_260628",
	} {
		require.True(t, HasBytePlusOfficialImagePricing(model), model)
	}

	for _, model := range []string{
		"dreamina-seedance-2-0-260128",
		"Doubao-Seedance-2.0-Pro-260128",
	} {
		require.True(t, HasBytePlusOfficialVideoPricing(model, "720p", "16:9", true), model)
	}

	require.False(t, HasBytePlusOfficialImagePricing("seedream-unknown"))
	require.False(t, HasBytePlusOfficialVideoPricing("seedance-unknown", "720p", "16:9", true))
	require.False(t, HasBytePlusOfficialImagePricing("seedream-5-0-pro-999999"))
	require.False(t, HasBytePlusOfficialVideoPricing("seedance-2-0-999999", "720p", "16:9", true))
}

func TestCalculateBytePlusSeedream5ProCost(t *testing.T) {
	cost, ok := calculateBytePlusImageCost(
		"dola-seedream-5-0-pro-260628",
		3,
		2,
		[]string{"2000x1180", "2000x1181"},
		ImageBillingSize2K,
	)

	require.True(t, ok)
	require.InDelta(t, 0.006, cost.inputCost, 1e-12)
	require.InDelta(t, 0.135, cost.outputCost, 1e-12)
}

func TestCalculateBytePlusSeedream5ProUsesConservativeUnknown2KPrice(t *testing.T) {
	cost, ok := calculateBytePlusImageCost("seedream-5-0-pro", 0, 1, nil, ImageBillingSize2K)

	require.True(t, ok)
	require.InDelta(t, 0.09, cost.outputCost, 1e-12)
}

func TestCalculateBytePlusFlatSeedreamPrices(t *testing.T) {
	tests := map[string]float64{
		"seedream-5-0-lite": 0.035,
		"seedream-4-5":      0.04,
		"seedream-4-0":      0.03,
		"seededit-3-0-i2i":  0.03,
	}
	for model, expected := range tests {
		cost, ok := calculateBytePlusImageCost(model, 4, 2, nil, ImageBillingSize4K)
		require.True(t, ok, model)
		require.Zero(t, cost.inputCost, model)
		require.InDelta(t, expected*2, cost.outputCost, 1e-12, model)
	}
}

func TestBytePlusSeedance2UsesOfficialTokenFormula(t *testing.T) {
	tests := []struct {
		model      string
		resolution string
		ratio      string
		width      int
		height     int
		rate       float64
	}{
		{"dreamina-seedance-2-0-260128", "480p", "16:9", 864, 496, 7.0},
		{"seedance-2-0-pro", "720p", "4:3", 1112, 834, 7.0},
		{"seedance-2-0", "1080p", "21:9", 2206, 946, 7.7},
		{"seedance-2-0", "4k", "1:1", 2880, 2880, 4.0},
		{"seedance-2-0-fast", "720p", "9:16", 720, 1280, 5.6},
		{"seedance-2-0-mini-260615", "480p", "1:1", 640, 640, 3.5},
	}

	for _, test := range tests {
		actual, ok := bytePlusVideoPricePerSecond(test.model, test.resolution, test.ratio, true)
		require.True(t, ok, "%s %s %s", test.model, test.resolution, test.ratio)
		require.InDelta(t, expectedPixelVideoPrice(test.width, test.height, test.rate), actual, 1e-12)
	}

	_, ok := bytePlusVideoPricePerSecond("seedance-2-0-fast", "1080p", "16:9", true)
	require.False(t, ok)
	_, ok = bytePlusVideoPricePerSecond("seedance-2-0-mini", "4k", "16:9", true)
	require.False(t, ok)
}

func TestBytePlusSeedanceAdaptiveUsesConservativePixelMaximum(t *testing.T) {
	actual, ok := bytePlusVideoPricePerSecond("seedance-2-0", "4k", "adaptive", true)

	require.True(t, ok)
	require.InDelta(t, expectedPixelVideoPrice(3326, 2494, 4.0), actual, 1e-12)
}

func TestBytePlusSeedance15AudioAndSilentRates(t *testing.T) {
	audio, ok := bytePlusVideoPricePerSecond("seedance-1-5-pro-251215", "720p", "4:3", true)
	require.True(t, ok)
	require.InDelta(t, expectedPixelVideoPrice(1112, 834, 2.4), audio, 1e-12)

	silent, ok := bytePlusVideoPricePerSecond("seedance-1-5-pro", "720p", "4:3", false)
	require.True(t, ok)
	require.InDelta(t, expectedPixelVideoPrice(1112, 834, 1.2), silent, 1e-12)

	_, ok = bytePlusVideoPricePerSecond("seedance-1-5-pro", "4k", "16:9", true)
	require.False(t, ok)
}

func TestBytePlusSeedance10RatioAndAdaptiveRates(t *testing.T) {
	portrait, ok := bytePlusVideoPricePerSecond("seedance-1-0-pro", "720p", "3:4", false)
	require.True(t, ok)
	require.InDelta(t, 21_840*2.5/1_000_000, portrait, 1e-12)

	adaptive, ok := bytePlusVideoPricePerSecond("seedance-1-0-pro-fast", "720p", "adaptive", false)
	require.True(t, ok)
	require.InDelta(t, 22_560.0/1_000_000, adaptive, 1e-12)
}

func TestBillingServiceBytePlusDetailedBreakdownAndMultiplier(t *testing.T) {
	svc := newBytePlusPricingTestBillingService()

	image := svc.CalculateImageCostWithMetadata(
		"seedream-5-0-pro",
		ImageBillingSize1K,
		2,
		1,
		[]string{"1024x1024"},
		nil,
		1.5,
	)
	require.InDelta(t, 0.003, image.ImageInputCost, 1e-12)
	require.InDelta(t, 0.045, image.ImageOutputCost, 1e-12)
	require.InDelta(t, 0.048, image.TotalCost, 1e-12)
	require.InDelta(t, 0.072, image.ActualCost, 1e-12)

	video := svc.CalculateVideoCostWithMetadata(
		"seedance-2-0",
		"720p",
		"16:9",
		true,
		1,
		5,
		nil,
		2,
	)
	require.InDelta(t, expectedPixelVideoPrice(1280, 720, 7.0)*5, video.TotalCost, 1e-12)
	require.InDelta(t, video.TotalCost*2, video.ActualCost, 1e-12)
}

func TestBillingServiceManualMediaPricesOverrideBytePlusCatalog(t *testing.T) {
	svc := newBytePlusPricingTestBillingService()
	imagePrice := 0.045
	videoPrice := expectedPixelVideoPrice(1280, 720, 7.0)

	image := svc.CalculateImageCostWithMetadata(
		"seedream-5-0-pro",
		ImageBillingSize1K,
		2,
		1,
		[]string{"1024x1024"},
		&ImagePriceConfig{Price1K: &imagePrice},
		1,
	)
	require.InDelta(t, imagePrice, image.TotalCost, 1e-12)
	require.Zero(t, image.ImageInputCost)

	video := svc.CalculateVideoCostWithMetadata(
		"seedance-2-0",
		"720p",
		"4:3",
		true,
		1,
		5,
		&VideoPriceConfig{Price720P: &videoPrice},
		1,
	)
	require.InDelta(t, videoPrice*5, video.TotalCost, 1e-12)
}

func TestOpenAIGatewayMediaPricingPreflight(t *testing.T) {
	ctx := context.Background()
	svc := &OpenAIGatewayService{}
	groupID := int64(701)
	apiKey := &APIKey{GroupID: &groupID, Group: &Group{ID: groupID}}

	require.True(t, svc.HasImageGenerationPricing(ctx, apiKey, "seedream-5-0-pro", "2K"))
	require.True(t, svc.HasVideoGenerationPricing(ctx, apiKey, "seedance-2-0", "720p", "16:9", true))
	require.False(t, svc.HasImageGenerationPricing(ctx, apiKey, "custom-image", "2K"))
	require.False(t, svc.HasVideoGenerationPricing(ctx, apiKey, "custom-video", "720p", "16:9", true))

	free := 0.0
	apiKey.Group.ImagePrice2K = &free
	apiKey.Group.VideoPrice720P = &free
	require.True(t, svc.HasImageGenerationPricing(ctx, apiKey, "custom-image", "2K"))
	require.True(t, svc.HasVideoGenerationPricing(ctx, apiKey, "custom-video", "720p", "16:9", true))
}

func TestOpenAIGatewayMediaPricingPreflightAcceptsChannelOverride(t *testing.T) {
	ctx := context.Background()
	groupID := int64(702)
	apiKey := &APIKey{GroupID: &groupID, Group: &Group{ID: groupID}}
	svc := &OpenAIGatewayService{
		resolver: newOpenAIImageChannelPricingResolverForTest(t, groupID, "custom-media", 0.25),
	}

	require.True(t, svc.HasImageGenerationPricing(ctx, apiKey, "custom-media", "4K"))
	require.True(t, svc.HasVideoGenerationPricing(ctx, apiKey, "custom-media", "1080p", "16:9", true))
}

func TestOpenAIGatewayMediaPricingPreflightRequiresMatchingChannelTierForUnknownModel(t *testing.T) {
	ctx := context.Background()
	groupID := int64(703)
	price := 0.19
	cache := newEmptyChannelCache()
	cache.pricingByGroupModel[channelModelKey{groupID: groupID, model: "third-party-custom-image"}] = &ChannelModelPricing{
		BillingMode: BillingModeImage,
		Intervals: []PricingInterval{{
			TierLabel:       "4k",
			PerRequestPrice: &price,
		}},
	}
	cache.channelByGroupID[groupID] = &Channel{ID: groupID, Status: StatusActive}
	cache.loadedAt = time.Now()
	channelService := &ChannelService{}
	channelService.cache.Store(cache)
	svc := &OpenAIGatewayService{
		resolver: NewModelPricingResolver(channelService, newBytePlusPricingTestBillingService()),
	}
	apiKey := &APIKey{GroupID: &groupID, Group: &Group{ID: groupID}}

	require.True(t, svc.HasImageGenerationPricing(ctx, apiKey, "third-party-custom-image", "4K"))
	require.False(t, svc.HasImageGenerationPricing(ctx, apiKey, "third-party-custom-image", "2K"))
	require.True(t, svc.HasImageGenerationPricing(ctx, apiKey, "seedream-5-0-pro", "2K"))
	resolved := svc.resolver.Resolve(ctx, PricingInput{Model: "third-party-custom-image", GroupID: &groupID})
	require.InDelta(t, price, svc.resolver.GetRequestTierPrice(resolved, "4K"), 1e-12)
}

func TestChannelTierExplicitZeroPriceOverridesDefaultPrice(t *testing.T) {
	ctx := context.Background()
	groupID := int64(704)
	free := 0.0
	defaultPrice := 0.33
	cache := newEmptyChannelCache()
	cache.pricingByGroupModel[channelModelKey{groupID: groupID, model: "free-4k-custom-image"}] = &ChannelModelPricing{
		BillingMode:     BillingModeImage,
		PerRequestPrice: &defaultPrice,
		Intervals: []PricingInterval{{
			TierLabel:       "4k",
			PerRequestPrice: &free,
		}},
	}
	cache.channelByGroupID[groupID] = &Channel{ID: groupID, Status: StatusActive}
	cache.loadedAt = time.Now()
	channelService := &ChannelService{}
	channelService.cache.Store(cache)
	billingService := newBytePlusPricingTestBillingService()
	resolver := NewModelPricingResolver(channelService, billingService)
	resolved := resolver.Resolve(ctx, PricingInput{Model: "free-4k-custom-image", GroupID: &groupID})

	cost, err := billingService.CalculateCostUnified(CostInput{
		Ctx:            ctx,
		Model:          "free-4k-custom-image",
		GroupID:        &groupID,
		RequestCount:   1,
		SizeTier:       "4K",
		RateMultiplier: 1,
		Resolver:       resolver,
		Resolved:       resolved,
	})

	require.NoError(t, err)
	require.Zero(t, cost.TotalCost)
	require.Zero(t, cost.ActualCost)
}

func expectedPixelVideoPrice(width, height int, pricePerMillionTokens float64) float64 {
	return float64(width*height*24) / 1024 * pricePerMillionTokens / 1_000_000
}

func newBytePlusPricingTestBillingService() *BillingService {
	return NewBillingService(&config.Config{}, nil)
}
