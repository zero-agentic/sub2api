package service

import (
	"strings"
)

const (
	bytePlusPricingSourceURL       = "https://docs.byteplus.com/en/docs/ModelArk/1544106"
	bytePlusPricingLastUpdated     = "2026-07-13"
	bytePlusSeedream5ProPixelLimit = int64(2_360_000)
)

type bytePlusImagePricing struct {
	outputPrice            float64
	outputPriceWithinLimit float64
	outputPriceAboveLimit  float64
	additionalInputPrice   float64
}

type bytePlusImageCost struct {
	inputCost  float64
	outputCost float64
}

var bytePlusImagePricingCatalog = map[string]bytePlusImagePricing{
	"seedream-5-0-pro": {
		outputPriceWithinLimit: 0.045,
		outputPriceAboveLimit:  0.09,
		additionalInputPrice:   0.003,
	},
	"seedream-5-0-lite": {outputPrice: 0.035},
	"seedream-4-5":      {outputPrice: 0.04},
	"seedream-4-0":      {outputPrice: 0.03},
	"seededit-3-0-i2i":  {outputPrice: 0.03},
}

var bytePlusPricingModelVersions = map[string]string{
	"seedream-5-0-pro":      "260628",
	"seedream-5-0-lite":     "260128",
	"seedream-4-5":          "251128",
	"seedream-4-0":          "250828",
	"seededit-3-0-i2i":      "250628",
	"seedance-2-0":          "260128",
	"seedance-2-0-pro":      "260128",
	"seedance-2-0-fast":     "260128",
	"seedance-2-0-mini":     "260615",
	"seedance-1-5-pro":      "251215",
	"seedance-1-0-pro":      "250528",
	"seedance-1-0-pro-fast": "251015",
}

var bytePlusSeedance2PricePerMillionTokens = map[string]map[string]float64{
	"seedance-2-0": {
		VideoBillingResolution480P:  7.0,
		VideoBillingResolution720P:  7.0,
		VideoBillingResolution1080P: 7.7,
		VideoBillingResolution4K:    4.0,
	},
	"seedance-2-0-pro": {
		VideoBillingResolution480P:  7.0,
		VideoBillingResolution720P:  7.0,
		VideoBillingResolution1080P: 7.7,
		VideoBillingResolution4K:    4.0,
	},
	"seedance-2-0-fast": {
		VideoBillingResolution480P: 5.6,
		VideoBillingResolution720P: 5.6,
	},
	"seedance-2-0-mini": {
		VideoBillingResolution480P: 3.5,
		VideoBillingResolution720P: 3.5,
	},
}

var bytePlusSeedanceDimensions = map[string]map[string][2]int{
	VideoBillingResolution480P: {
		"16:9": {864, 496},
		"4:3":  {752, 560},
		"1:1":  {640, 640},
		"3:4":  {560, 752},
		"9:16": {496, 864},
		"21:9": {992, 432},
	},
	VideoBillingResolution720P: {
		"16:9": {1280, 720},
		"4:3":  {1112, 834},
		"1:1":  {960, 960},
		"3:4":  {834, 1112},
		"9:16": {720, 1280},
		"21:9": {1470, 630},
	},
	VideoBillingResolution1080P: {
		"16:9": {1920, 1080},
		"4:3":  {1664, 1248},
		"1:1":  {1440, 1440},
		"3:4":  {1248, 1664},
		"9:16": {1080, 1920},
		"21:9": {2206, 946},
	},
	VideoBillingResolution4K: {
		"16:9": {3840, 2160},
		"4:3":  {3326, 2494},
		"1:1":  {2880, 2880},
		"3:4":  {2494, 3326},
		"9:16": {2160, 3840},
		"21:9": {4398, 1886},
	},
}

// Seedance 1.0 examples expose exact output token usage for each resolution and
// ratio. Rates below use those token counts with the documented online token
// unit price instead of rounded per-video examples.
var bytePlusSeedance10TokensPerSecond = map[string]map[string]float64{
	VideoBillingResolution480P: {
		"16:9": 9_720,
		"4:3":  9_384,
		"1:1":  9_600,
		"3:4":  9_384,
		"9:16": 9_720,
		"21:9": 9_360,
	},
	VideoBillingResolution720P: {
		"16:9": 20_592,
		"4:3":  21_840,
		"1:1":  21_600,
		"3:4":  21_840,
		"9:16": 20_592,
		"21:9": 22_560,
	},
	VideoBillingResolution1080P: {
		"16:9": 48_960,
		"4:3":  48_672,
		"1:1":  48_600,
		"3:4":  48_672,
		"9:16": 48_960,
		"21:9": 47_328,
	},
}

func HasBytePlusOfficialImagePricing(model string) bool {
	_, ok := bytePlusImagePricingCatalog[normalizeBytePlusPricingModel(model)]
	return ok
}

func HasBytePlusOfficialVideoPricing(model, resolution, ratio string, generateAudio bool) bool {
	_, ok := bytePlusVideoPricePerSecond(model, resolution, ratio, generateAudio)
	return ok
}

func calculateBytePlusImageCost(model string, inputImageCount, outputImageCount int, outputSizes []string, fallbackTier string) (bytePlusImageCost, bool) {
	pricing, ok := bytePlusImagePricingCatalog[normalizeBytePlusPricingModel(model)]
	if !ok || outputImageCount <= 0 {
		return bytePlusImageCost{}, false
	}

	cost := bytePlusImageCost{}
	if inputImageCount > 1 && pricing.additionalInputPrice > 0 {
		cost.inputCost = float64(inputImageCount-1) * pricing.additionalInputPrice
	}
	for index := 0; index < outputImageCount; index++ {
		size := fallbackTier
		if index < len(outputSizes) && strings.TrimSpace(outputSizes[index]) != "" {
			size = outputSizes[index]
		}
		cost.outputCost += bytePlusImageOutputPrice(pricing, size)
	}
	return cost, true
}

func bytePlusImageOutputPrice(pricing bytePlusImagePricing, size string) float64 {
	if pricing.outputPrice > 0 {
		return pricing.outputPrice
	}
	if width, height, ok := parseImageBillingDimensions(size); ok {
		pixels := int64(width) * int64(height)
		if pixels <= bytePlusSeedream5ProPixelLimit {
			return pricing.outputPriceWithinLimit
		}
		return pricing.outputPriceAboveLimit
	}
	if tier, ok := ClassifyImageBillingTier(size); ok && tier == ImageBillingSize1K {
		return pricing.outputPriceWithinLimit
	}
	// A 2K label can be either side of the 2.36 MP boundary depending on ratio.
	// Without exact output dimensions, choose the higher official tier.
	return pricing.outputPriceAboveLimit
}

func bytePlusVideoPricePerSecond(model, resolution, ratio string, generateAudio bool) (float64, bool) {
	model = normalizeBytePlusPricingModel(model)
	resolution = NormalizeVideoBillingResolutionOrDefault(resolution)
	if prices, ok := bytePlusSeedance2PricePerMillionTokens[model]; ok {
		pricePerMillionTokens, supported := prices[resolution]
		if !supported {
			return 0, false
		}
		return bytePlusPixelVideoPricePerSecond(resolution, ratio, pricePerMillionTokens)
	}
	if model == "seedance-1-5-pro" {
		pricePerMillionTokens := 1.2
		if generateAudio {
			pricePerMillionTokens = 2.4
		}
		if resolution == VideoBillingResolution4K {
			return 0, false
		}
		return bytePlusPixelVideoPricePerSecond(resolution, ratio, pricePerMillionTokens)
	}
	if model == "seedance-1-0-pro" || model == "seedance-1-0-pro-fast" {
		tokensPerSecond, supported := bytePlusSeedance10TokensPerSecond[resolution]
		if !supported {
			return 0, false
		}
		ratio = strings.ToLower(strings.TrimSpace(ratio))
		tokens, exact := tokensPerSecond[ratio]
		if !exact {
			for _, candidate := range tokensPerSecond {
				if candidate > tokens {
					tokens = candidate
				}
			}
		}
		pricePerMillionTokens := 2.5
		if model == "seedance-1-0-pro-fast" {
			pricePerMillionTokens = 1.0
		}
		return tokens * pricePerMillionTokens / 1_000_000, true
	}
	return 0, false
}

func bytePlusPixelVideoPricePerSecond(resolution, ratio string, pricePerMillionTokens float64) (float64, bool) {
	dimensionsByRatio, supported := bytePlusSeedanceDimensions[resolution]
	if !supported {
		return 0, false
	}
	ratio = strings.ToLower(strings.TrimSpace(ratio))
	dimensions, exact := dimensionsByRatio[ratio]
	if !exact {
		for _, candidate := range dimensionsByRatio {
			if candidate[0]*candidate[1] > dimensions[0]*dimensions[1] {
				dimensions = candidate
			}
		}
	}
	tokensPerSecond := float64(dimensions[0]*dimensions[1]*24) / 1024
	return tokensPerSecond * pricePerMillionTokens / 1_000_000, true
}

// bytePlusBrandPrefixes is the shared list of brand prefixes stripped when
// normalizing BytePlus/Doubao model names. Both the pricing normalizer below
// and the routing normalizer (normalizeModelName in lumina_gateway.go) derive
// from it so a newly-shipped brand prefix cannot match for routing while
// silently missing the pricing lookup, or vice versa.
var bytePlusBrandPrefixes = []string{"bytedance-", "dreamina-", "dola-", "doubao-"}

func normalizeBytePlusPricingModel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer(".", "-", "_", "-", "/", "-").Replace(value)
	for {
		original := value
		for _, prefix := range bytePlusBrandPrefixes {
			value = strings.TrimPrefix(value, prefix)
		}
		if value == original {
			break
		}
	}
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == '-' || r == ' ' })
	if len(parts) > 1 && isSixDigitPriceVersion(parts[len(parts)-1]) {
		candidate := strings.Join(parts[:len(parts)-1], "-")
		if bytePlusPricingModelVersions[candidate] == parts[len(parts)-1] {
			parts = parts[:len(parts)-1]
		}
	}
	return strings.Join(parts, "-")
}

func isSixDigitPriceVersion(value string) bool {
	if len(value) != 6 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}
