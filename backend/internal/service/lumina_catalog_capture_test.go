package service

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/lumina"
	"github.com/Wei-Shaw/sub2api/internal/pkg/modelark"
	"github.com/stretchr/testify/require"
)

// luminaCatalogCapture mirrors the live responses of list_ai_services and
// get_schema_list, captured from ai.byteplus.com on 2026-07-28 and trimmed to the
// fields we decode. It exists so the capability filters and the ModelArk alias
// table are pinned against real upstream shapes rather than hand-written guesses
// — every earlier Lumina defect came from guessing those shapes.
type luminaCatalogCapture struct {
	Images struct {
		Data struct {
			Services []lumina.ImageService `json:"services"`
		} `json:"data"`
	} `json:"images"`
	Videos struct {
		Data []lumina.VideoSchemaBucket `json:"data"`
	} `json:"videos"`
}

func loadLuminaCatalogCapture(t *testing.T) luminaCatalogCapture {
	t.Helper()
	payload, err := os.ReadFile("testdata/lumina_catalog_capture.json")
	require.NoError(t, err)
	var capture luminaCatalogCapture
	require.NoError(t, json.Unmarshal(payload, &capture))
	require.Len(t, capture.Images.Data.Services, 9)
	require.Len(t, capture.Videos.Data, 8)
	return capture
}

func TestLuminaCatalogCaptureProducesTheFullModelSet(t *testing.T) {
	capture := loadLuminaCatalogCapture(t)

	mappings, skipped := luminaCatalogModelMappings(capture.Images.Data.Services, capture.Videos.Data)

	require.Equal(t, []UpstreamModelMapping{
		{From: "dola-seedream-5-0-pro-260628", To: "ByteDance-Seedream-5.0-pro"},
		{From: "dreamina-seedance-2-0-260128", To: "Doubao-Seedance-2.0-pro"},
		{From: "dreamina-seedance-2-0-fast-260128", To: "Doubao-Seedance-2.0-pro-fast"},
		{From: "dreamina-seedance-2-0-mini-260615", To: "Doubao-Seedance-2.0-mini"},
		{From: "gpt-image-2", To: "x2i_gpt_image2_lumina"},
		{From: "nano-banana-2", To: "x2i_nano_lumina"},
		{From: "nano-banana-pro", To: "x2i_nano_pro_lumina"},
		{From: "seedance-1-0", To: "seedance_i2v_pro_com"},
		{From: "seedance-1-0-pro-fast-251015", To: "ByteDance-Seedance-1.0-pro-fast"},
		{From: "seedance-1-5-pro-251215", To: "ByteDance-Seedance-1.5-pro"},
		{From: "seedream-3-0l", To: "high_aes_general_v30l"},
		{From: "seedream-3-0l-art", To: "high_aes_general_v30l_art"},
		{From: "seedream-4-0-250828", To: "high_aes_general_v40s"},
		{From: "seedream-4-5-251128", To: "high_aes_general_v45_ba"},
		{From: "seedream-5-0-lite-260128", To: "seedream_v50_ba"},
	}, mappings)

	// Everything dropped needs an uploaded image, an existing video or a
	// multimodal reference, so the prompt-only gateway cannot drive it.
	require.NotEmpty(t, skipped)
	require.Contains(t, skipped, "dreamactor_m20_gen_video [motion/motion]")
	require.Contains(t, skipped, "ByteDance-Seedance-1.0-pro [flf/flf]")
	require.Contains(t, skipped, "Doubao-Seedance-2.0-pro [x2v/x2v/f2v]")
}

// GPT Image 2 ships inference_types: null, which the declared-capability filter
// dropped silently — that is why it was missing from the synced list.
func TestLuminaCaptureImageCapabilitiesIncludeUndeclaredServices(t *testing.T) {
	capture := loadLuminaCatalogCapture(t)

	byReqKey := make(map[string]lumina.ImageService, len(capture.Images.Data.Services))
	for _, service := range capture.Images.Data.Services {
		byReqKey[service.ReqKey] = service
	}

	gptImage := byReqKey["x2i_gpt_image2_lumina"]
	require.Empty(t, gptImage.InferenceTypes, "capture must keep the null inference_types that caused the bug")
	require.True(t, gptImage.SupportsTextToImage())

	require.True(t, byReqKey["high_aes_general_v40s"].SupportsTextToImage())
	require.True(t, byReqKey["seedream_v50_ba"].SupportsTextToImage())
}

// Seedance 1.x leaves task_type empty and never appears in an "x2v" bucket, so
// the old inference_type/task_type filter hid the whole family.
func TestLuminaCaptureVideoCapabilitiesSpanBothBucketLayouts(t *testing.T) {
	capture := loadLuminaCatalogCapture(t)

	promptOnly := map[string][]string{}
	for _, bucket := range capture.Videos.Data {
		for _, schema := range bucket.Items {
			if schema.IsTextToVideo() {
				promptOnly[schema.ReqKey] = append(promptOnly[schema.ReqKey], bucket.Type+":"+schema.TaskType)
			}
		}
	}

	require.Equal(t, []string{"x2v:t2v", "t2i2v:t2v"}, promptOnly["Doubao-Seedance-2.0-pro"])
	require.Equal(t, []string{"t2i2v:"}, promptOnly["ByteDance-Seedance-1.5-pro"])
	require.Equal(t, []string{"t2i2v:"}, promptOnly["seedance_i2v_pro_com"])
	// Only offered with first/last frame inputs in this capture.
	require.NotContains(t, promptOnly, "ByteDance-Seedance-1.0-pro")
	require.NotContains(t, promptOnly, "dreamactor_m20_gen_video")
}

// The 1.x schemas enumerate fixed ratios and name the audio toggle differently,
// which made every default-parameter request fail once they became reachable.
func TestLuminaCaptureVideoRequestBuildsForSeedance1x(t *testing.T) {
	capture := loadLuminaCatalogCapture(t)

	var schema *lumina.VideoSchema
	for _, bucket := range capture.Videos.Data {
		for index := range bucket.Items {
			if bucket.Items[index].ReqKey == "ByteDance-Seedance-1.5-pro" && bucket.Items[index].IsTextToVideo() {
				schema = &bucket.Items[index]
			}
		}
	}
	require.NotNil(t, schema)

	audio := true
	request := &modelark.VideoGenerationRequest{
		Model:         "seedance-1-5-pro-251215",
		Content:       []modelark.VideoContent{{Type: "text", Text: "a cat"}},
		GenerateAudio: &audio,
	}
	inputs, normalized, err := buildLuminaVideoInputs(request, schema)
	require.NoError(t, err)

	// The request never named a ratio, so the model's own default replaces our
	// adaptive default instead of failing the call.
	require.Equal(t, "16:9", normalized.Ratio)

	byName := make(map[string]any, len(inputs))
	for _, input := range inputs {
		byName[input.Name] = input.Value
	}
	require.Equal(t, "a cat", byName["prompt"])
	require.Equal(t, "16:9", byName["aspect_ratio"])
	require.Equal(t, "true", byName["with_audio"], "generate_audio must reach the with_audio field 1.x declares")
	require.NotContains(t, byName, "task_type", "1.x schemas do not declare task_type")
	require.Equal(t, "seedance_v1.5", byName["model_version"], "schema defaults must be forwarded")
}

// An explicitly requested ratio the model cannot honour still fails loudly.
func TestLuminaCaptureVideoRejectsExplicitUnsupportedRatio(t *testing.T) {
	capture := loadLuminaCatalogCapture(t)

	var schema *lumina.VideoSchema
	for _, bucket := range capture.Videos.Data {
		for index := range bucket.Items {
			if bucket.Items[index].ReqKey == "ByteDance-Seedance-1.5-pro" && bucket.Items[index].IsTextToVideo() {
				schema = &bucket.Items[index]
			}
		}
	}
	require.NotNil(t, schema)

	_, _, err := buildLuminaVideoInputs(&modelark.VideoGenerationRequest{
		Model:   "seedance-1-5-pro-251215",
		Content: []modelark.VideoContent{{Type: "text", Text: "a cat"}},
		Ratio:   "adaptive",
	}, schema)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ratio")
}
