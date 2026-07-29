package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/lumina"
	"github.com/Wei-Shaw/sub2api/internal/pkg/modelark"
	"github.com/stretchr/testify/require"
)

func TestLuminaCatalogModelMappingsTranslatesToModelArkIDs(t *testing.T) {
	images := []lumina.ImageService{
		{ReqKey: "high_aes_general_v40s", NameEN: "Seedream 4.0", InferenceTypes: []string{"t2i", "i2i"}},
		// Display name wins over req_key, so an upstream req_key rename cannot
		// silently drop the official ID.
		{ReqKey: "high_aes_general_v45_renamed", NameEN: "Seedream 4.5", InferenceTypes: []string{"t2i"}},
		// Lumina-only model: normalized slug, not the internal req_key.
		{ReqKey: "x2i_gpt_image2_lumina", NameEN: "gpt image2", Name: "GPT Image 2 （Beta）", InferenceTypes: []string{"x2i"}},
		// Unknown to the table: the req_key stays the public ID.
		{ReqKey: "brand_new_service", NameEN: "Something Unreleased", InferenceTypes: []string{"t2i"}},
		// Not promptable: image-to-image only.
		{ReqKey: "seededit_i2i", NameEN: "SeedEdit 3.0", InferenceTypes: []string{"i2i"}},
	}
	promptOnly := luminaTextToVideoSchema("", "Doubao-Seedance-2.0-pro")
	promptOnly.Name = "Seedance 2.0"
	needsImage := luminaTextToVideoSchema("", "Doubao-Seedance-2.0-pro")
	needsImage.Name = "Seedance 2.0"
	needsImage.TaskType = "f2v"
	needsImage.Schema.ConfigSchemas = append(needsImage.Schema.ConfigSchemas,
		lumina.SchemaField{Name: "img", InternalName: "first_prompt_pic"})
	// Seedance 1.x leaves task_type empty and only appears under t2i2v.
	legacyFamily := luminaTextToVideoSchema("", "ByteDance-Seedance-1.5-pro")
	legacyFamily.Name = "Seedance 1.5 Pro"
	legacyFamily.InferenceType = "t2i2v"
	legacyFamily.TaskType = ""
	buckets := []lumina.VideoSchemaBucket{
		{Type: "x2v", Items: []lumina.VideoSchema{promptOnly, needsImage}},
		{Type: "t2i2v", Items: []lumina.VideoSchema{legacyFamily}},
	}

	mappings, skipped := luminaCatalogModelMappings(images, buckets)

	require.Equal(t, []UpstreamModelMapping{
		{From: "brand_new_service", To: "brand_new_service"},
		{From: "dreamina-seedance-2-0-260128", To: "Doubao-Seedance-2.0-pro"},
		{From: "gpt-image-2", To: "x2i_gpt_image2_lumina"},
		{From: "seedance-1-5-pro-251215", To: "ByteDance-Seedance-1.5-pro"},
		{From: "seedream-4-0-250828", To: "high_aes_general_v40s"},
		{From: "seedream-4-5-251128", To: "high_aes_general_v45_renamed"},
	}, mappings)

	require.Len(t, skipped, 2)
	require.Contains(t, skipped, "seededit_i2i [i2i]")
	require.Contains(t, skipped, "Doubao-Seedance-2.0-pro [x2v/x2v/f2v]")
}

func TestLuminaPublicModelIDPrefersDisplayNameThenReqKey(t *testing.T) {
	require.Equal(t, "seedream-4-0-250828", luminaPublicModelID("high_aes_general_v40s"))
	require.Equal(t, "seedream-4-5-251128", luminaPublicModelID("unknown_key", "Seedream 4.5"))
	require.Equal(t, "nano-banana-pro", luminaPublicModelID("x2i_nano_pro_lumina"))
	require.Equal(t, "dola-seedream-5-0-pro-260628", luminaPublicModelID("ByteDance-Seedream-5.0-pro"))
	// 5.0 / 5.0 Lite / 5.0 Pro must not collapse into each other.
	require.Equal(t, "seedream-5-0-260128", luminaPublicModelID("some_key", "Seedream 5.0"))
	require.Equal(t, "seedream-5-0-lite-260128", luminaPublicModelID("some_key", "Seedream 5.0 Lite"))
	// Untranslatable entries pass through unchanged.
	require.Equal(t, "mystery_model", luminaPublicModelID("mystery_model", "Mystery Model"))
}

// A public ID that two catalog entries claim must resolve deterministically
// instead of overwriting itself inside the account's model_mapping object.
func TestLuminaCatalogModelMappingsReportsCollisions(t *testing.T) {
	images := []lumina.ImageService{
		{ReqKey: "high_aes_general_v40s", NameEN: "Seedream 4.0", InferenceTypes: []string{"t2i"}},
		{ReqKey: "zzz_other_v40", NameEN: "Seedream 4.0", InferenceTypes: []string{"t2i"}},
	}

	mappings, skipped := luminaCatalogModelMappings(images, nil)

	require.Equal(t, []UpstreamModelMapping{
		{From: "seedream-4-0-250828", To: "high_aes_general_v40s"},
	}, mappings)
	require.Contains(t, skipped, "zzz_other_v40 [duplicate of seedream-4-0-250828]")
}

// The gateway resolves an incoming request through model_mapping, so every
// public ID the sync emits must round-trip back to its req_key — including the
// unversioned form ai-sdk users often pass.
func TestSyncedMappingResolvesBackToReqKey(t *testing.T) {
	account := &Account{Credentials: map[string]any{
		"model_mapping": map[string]any{
			"seedream-4-0-250828":          "high_aes_general_v40s",
			"dreamina-seedance-2-0-260128": "Doubao-Seedance-2.0-pro",
		},
	}}
	services := []lumina.ImageService{{ReqKey: "high_aes_general_v40s", NameEN: "Seedream 4.0"}}
	schemas := []lumina.VideoSchema{luminaTextToVideoSchema("schema", "Doubao-Seedance-2.0-pro")}

	for _, requested := range []string{"seedream-4-0-250828", "seedream-4-0"} {
		resolved, err := resolveLuminaImageService(account, requested, services)
		require.NoErrorf(t, err, "requested %s", requested)
		require.Equal(t, "high_aes_general_v40s", resolved.ReqKey)
	}

	for _, requested := range []string{"dreamina-seedance-2-0-260128", "seedance-2-0"} {
		resolved, err := resolveLuminaVideoSchema(account, &modelark.VideoGenerationRequest{Model: requested}, schemas)
		require.NoErrorf(t, err, "requested %s", requested)
		require.Equal(t, "Doubao-Seedance-2.0-pro", resolved.ReqKey)
	}
}
