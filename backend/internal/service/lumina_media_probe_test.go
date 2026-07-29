package service

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/lumina"
	"github.com/Wei-Shaw/sub2api/internal/pkg/modelark"
	"github.com/stretchr/testify/require"
)

// luminaCaptureVideoSchemas flattens the captured buckets the way the gateway
// catalog cache does.
func luminaCaptureVideoSchemas(capture luminaCatalogCapture) []lumina.VideoSchema {
	schemas := make([]lumina.VideoSchema, 0)
	for _, bucket := range capture.Videos.Data {
		schemas = append(schemas, bucket.Items...)
	}
	return schemas
}

// luminaSyncedAccount builds the account shape model sync produces, so probes
// are classified against exactly the mapping an operator ends up with.
func luminaSyncedAccount(t *testing.T, capture luminaCatalogCapture) *Account {
	t.Helper()
	mappings, _ := luminaCatalogModelMappings(capture.Images.Data.Services, capture.Videos.Data)
	mapping := make(map[string]any, len(mappings))
	for _, entry := range mappings {
		mapping[entry.From] = entry.To
	}
	return &Account{ID: 7, Platform: PlatformLumina, Type: AccountTypeCookie, Credentials: map[string]any{"model_mapping": mapping}}
}

// The account test has to know which half of the catalog a public model ID
// belongs to before it can generate anything, and only the live catalog knows.
func TestClassifyLuminaCatalogModelSplitsImageAndVideoModels(t *testing.T) {
	capture := loadLuminaCatalogCapture(t)
	account := luminaSyncedAccount(t, capture)
	images := capture.Images.Data.Services
	videos := luminaCaptureVideoSchemas(capture)

	kind, name, err := classifyLuminaCatalogModel(account, "gpt-image-2", images, videos)
	require.NoError(t, err)
	require.Equal(t, LuminaMediaKindImage, kind)
	require.Equal(t, "GPT Image 2 （Beta）", name)

	kind, name, err = classifyLuminaCatalogModel(account, "seedream-4-5-251128", images, videos)
	require.NoError(t, err)
	require.Equal(t, LuminaMediaKindImage, kind)
	require.Equal(t, "Seedream 4.5", name)

	kind, name, err = classifyLuminaCatalogModel(account, "dreamina-seedance-2-0-260128", images, videos)
	require.NoError(t, err)
	require.Equal(t, LuminaMediaKindVideo, kind)
	require.Equal(t, "Seedance 2.0", name)

	_, _, err = classifyLuminaCatalogModel(account, "claude-sonnet-4-5", images, videos)
	parameterErr, ok := modelark.AsParameterError(err)
	require.True(t, ok)
	require.Equal(t, "InvalidEndpointOrModel.NotFound", parameterErr.Code)
}

// A video schema that needs an uploaded image is not a video test target: the
// video resolver keeps text-to-video schemas only, so such a model falls through
// to the not-found error instead of being probed with a prompt it cannot use.
func TestClassifyLuminaCatalogModelRejectsSchemasThatNeedMedia(t *testing.T) {
	account := &Account{Credentials: map[string]any{"model_mapping": map[string]any{"frames-only": "frames_only_schema"}}}
	imageDriven := lumina.VideoSchema{ID: "frames-only-id", Name: "Frames Only", ReqKey: "frames_only_schema", InferenceType: "flf"}
	imageDriven.Schema.InputSchemas = []lumina.SchemaField{
		{Name: "prompt", InternalName: "prompt"},
		{Name: "img", InternalName: "first_prompt_pic"},
	}
	require.False(t, imageDriven.IsTextToVideo())

	_, _, err := classifyLuminaCatalogModel(account, "frames-only", nil, []lumina.VideoSchema{imageDriven})
	parameterErr, ok := modelark.AsParameterError(err)
	require.True(t, ok)
	require.Equal(t, "model", parameterErr.Param)
}

// An image service without a prompt field is caught by the input builder with a
// specific reason, which is what the operator sees — classification does not
// re-derive prompt capability.
func TestBuildLuminaImageInputsRejectsSchemaWithoutPrompt(t *testing.T) {
	_, err := buildLuminaImageInputs(
		&modelark.ImageGenerationRequest{Model: "edit-only", Prompt: "draw a cat"},
		&lumina.ImageServiceSchema{Inputs: []lumina.SchemaField{{Name: "image", InternalName: "image"}}},
	)
	parameterErr, ok := modelark.AsParameterError(err)
	require.True(t, ok)
	require.Equal(t, "prompt", parameterErr.Param)
}

// Upstream types every inputs[].value as a string and rejects the whole create
// with HTTP 200 + code=400 ("cannot unmarshal number into Go struct field
// CreateTaskInput.inputs.value of type string") when a schema default arrives as
// a JSON number or boolean. Seedream 5.0 Pro's live schema carries exactly those:
// inner_min_ratio 0.07, inner_max_ratio 16, seed -1, optimize_prompt true.
func TestBuildLuminaImageInputsSendsEveryValueAsString(t *testing.T) {
	schema := &lumina.ImageServiceSchema{Inputs: []lumina.SchemaField{
		{Name: "prompt", InternalName: "prompt", Type: "string", Format: "text_area"},
		{Name: "min_ratio", InternalName: "inner_min_ratio", Format: "input_number", DefaultValue: float64(0.07)},
		{Name: "max_ratio", InternalName: "inner_max_ratio", Format: "input_number", DefaultValue: float64(16)},
		{Name: "use_pre_llm", InternalName: "optimize_prompt", Format: "input_boolean", DefaultValue: true},
		{Name: "seed", InternalName: "seed", Format: "input_number", DefaultValue: float64(-1)},
		{Name: "size", InternalName: "size", Format: "custom", DefaultValue: "1024x1024"},
		{Name: "annotation_info", Format: "", DefaultValue: map[string]any{}},
		{Name: "large_number", InternalName: "large_number", DefaultValue: float64(1000000)},
	}}

	inputs, err := buildLuminaImageInputs(
		&modelark.ImageGenerationRequest{Model: "dola-seedream-5-0-pro-260628", Prompt: "A cute orange cat astronaut."},
		schema,
	)
	require.NoError(t, err)

	rendered := make(map[string]string, len(inputs))
	for _, input := range inputs {
		_, isString := input.Value.(string)
		require.Truef(t, isString, "input %q must carry a string value, got %T", input.Name, input.Value)
		rendered[input.Name] = input.Value.(string)
	}
	require.Equal(t, "A cute orange cat astronaut.", rendered["prompt"])
	require.Equal(t, "0.07", rendered["min_ratio"])
	require.Equal(t, "16", rendered["max_ratio"])
	require.Equal(t, "true", rendered["use_pre_llm"])
	require.Equal(t, "-1", rendered["seed"])
	require.Equal(t, "1024x1024", rendered["size"])
	require.Equal(t, "{}", rendered["annotation_info"])
	// Formatted without an exponent: fmt would render this float64 as "1e+06".
	require.Equal(t, "1000000", rendered["large_number"])
}

// The live Seedream 5.0 Pro schema declares annotation_info and origin_img with
// null defaults. The browser still posts "{}" / "[]" for them on every t2i
// create; omitting those placeholders is what turned a valid session into
// InternalServiceError during account probes. The visible img upload stays
// omitted when empty — the console does the same.
func TestBuildLuminaImageInputsInjectsConsolePlaceholdersForNilDefaults(t *testing.T) {
	schema := &lumina.ImageServiceSchema{Inputs: []lumina.SchemaField{
		{Name: "prompt", InternalName: "prompt", Type: "string", Format: "text_area"},
		{Name: "min_ratio", InternalName: "inner_min_ratio", Format: "input_number", DefaultValue: float64(0.07)},
		{Name: "max_ratio", InternalName: "inner_max_ratio", Format: "input_number", DefaultValue: float64(16)},
		{Name: "cot_mode", InternalName: "optimize_prompt_options.thinking", Format: "select", DefaultValue: "enabled"},
		{Name: "use_pre_llm", InternalName: "optimize_prompt", Format: "input_boolean", DefaultValue: true},
		{Name: "size", InternalName: "size", Format: "custom", DefaultValue: "1024x1024"},
		{Name: "img", InternalName: "image", Format: "image_upload", Transformer: "tos_uri_to_url"},
		{Name: "origin_img", Format: "image_upload", Transformer: "tos_uri_to_url"},
		{Name: "annotation_info", Type: "string"},
		{Name: "seed", InternalName: "seed", Format: "input_number", DefaultValue: float64(-1)},
	}}

	inputs, err := buildLuminaImageInputs(
		&modelark.ImageGenerationRequest{
			Model:  "dola-seedream-5-0-pro-260628",
			Prompt: "A cute orange cat astronaut floating in a pastel nebula.",
		},
		schema,
	)
	require.NoError(t, err)

	byName := make(map[string]lumina.InferenceInput, len(inputs))
	for _, input := range inputs {
		byName[input.Name] = input
	}
	require.Equal(t, "{}", byName["annotation_info"].Value)
	require.Equal(t, "[]", byName["origin_img"].Value)
	require.Equal(t, "tos_uri_to_url", byName["origin_img"].Transformer)
	require.Equal(t, "enabled", byName["cot_mode"].Value)
	_, hasVisibleImg := byName["img"]
	require.False(t, hasVisibleImg, "empty visible image upload must stay omitted")
	require.Len(t, inputs, 9)

	// Wire shape must match the browser create_task body: no type/props echo.
	for _, input := range inputs {
		raw, err := json.Marshal(input)
		require.NoError(t, err)
		var object map[string]any
		require.NoError(t, json.Unmarshal(raw, &object))
		_, hasType := object["type"]
		_, hasProps := object["props"]
		require.Falsef(t, hasType, "input %q must not serialize type", input.Name)
		require.Falsef(t, hasProps, "input %q must not serialize props", input.Name)
		require.Contains(t, object, "label")
		require.IsType(t, "", object["value"])
	}
	maxRatio, err := json.Marshal(byName["max_ratio"])
	require.NoError(t, err)
	require.JSONEq(t, `{
		"name":"max_ratio",
		"internal_name":"inner_max_ratio",
		"label":"max_ratio",
		"format":"input_number",
		"value":"16"
	}`, string(maxRatio))
}

func TestLuminaTaskFailReasonsCollectsChildReasons(t *testing.T) {
	require.Empty(t, luminaTaskFailReasons(nil))
	require.Empty(t, luminaTaskFailReasons(&lumina.Task{Children: []lumina.SubTask{{Status: "failed"}}}))
	require.Equal(t, []string{"quota exceeded", "sensitive content"}, luminaTaskFailReasons(&lumina.Task{
		Children: []lumina.SubTask{
			{FailReason: "quota exceeded"},
			{FailReason: "  "},
			{FailReason: "sensitive content"},
		},
	}))
}

// The probe request must pass the provider capability gate on its own, and must
// leave every optional field unset so NormalizeLuminaVideoRequest stays the one
// source of defaults.
func TestNewLuminaProbeVideoRequestSatisfiesProviderCapabilityGate(t *testing.T) {
	request := newLuminaProbeVideoRequest("dreamina-seedance-2-0-260128", "A quiet lake at dawn")

	require.NoError(t, request.Validate())
	require.NoError(t, validateLuminaVideoSupportedParameters(request))
	require.Empty(t, request.Resolution)
	require.Empty(t, request.Ratio)
	require.Nil(t, request.Duration)
	require.Nil(t, request.GenerateAudio)
}

// Every model the sync exposes can be picked in the test dropdown, so the probe
// request has to build against every captured text-to-video schema — including
// the 1.x family, which enumerates fixed aspect ratios instead of accepting the
// adaptive default.
func TestNewLuminaProbeVideoRequestBuildsAgainstEveryCapturedSchema(t *testing.T) {
	capture := loadLuminaCatalogCapture(t)
	promptable := 0
	for _, schema := range luminaCaptureVideoSchemas(capture) {
		if !schema.IsTextToVideo() {
			continue
		}
		promptable++
		candidate := schema
		t.Run(candidate.ReqKey+"/"+candidate.Type, func(t *testing.T) {
			inputs, normalized, err := buildLuminaVideoInputs(
				newLuminaProbeVideoRequest("probe-model", "A quiet lake at dawn"), &candidate,
			)
			require.NoError(t, err)
			require.Equal(t, VideoBillingResolution720P, normalized.Resolution)
			require.Equal(t, 5, *normalized.Duration)
			require.NotEmpty(t, normalized.Ratio)

			prompts := 0
			for _, input := range inputs {
				if input.InternalName == "prompt" || input.Name == "prompt" {
					prompts++
					require.Equal(t, "A quiet lake at dawn", input.Value)
				}
			}
			require.Equal(t, 1, prompts, "the probe must send the operator prompt exactly once")
		})
	}
	require.NotZero(t, promptable, "the capture must contain text-to-video schemas")
}

func TestLuminaTaskVideoURLFallsBackToGenericOutputValue(t *testing.T) {
	require.Empty(t, luminaTaskVideoURL(nil))
	require.Empty(t, luminaTaskVideoURL(&lumina.Task{Children: []lumina.SubTask{{Output: nil}}}))

	dedicated := &lumina.Task{Children: []lumina.SubTask{{Output: &lumina.TaskOutput{
		VideoURL: "https://example.com/clip.mp4",
		Value:    "https://example.com/frame.png",
	}}}}
	require.Equal(t, "https://example.com/clip.mp4", luminaTaskVideoURL(dedicated))

	generic := &lumina.Task{Children: []lumina.SubTask{
		{Output: nil},
		{Output: &lumina.TaskOutput{Value: "https://example.com/generic.mp4"}},
	}}
	require.Equal(t, "https://example.com/generic.mp4", luminaTaskVideoURL(generic))
}

func TestLuminaProbeImageMIMETypeDefaultsToPNG(t *testing.T) {
	require.Equal(t, "image/jpeg", luminaProbeImageMIMEType("jpeg"))
	require.Equal(t, "image/png", luminaProbeImageMIMEType("png"))
	require.Equal(t, "image/png", luminaProbeImageMIMEType(""))
}
