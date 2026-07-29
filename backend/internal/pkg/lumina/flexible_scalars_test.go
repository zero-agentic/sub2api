package lumina

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// The production catalog sent "false" for required_lumi_resource_uri, which
// failed the whole video schema list and blocked model sync entirely.
func TestVideoSchemaDecodesStringTypedScalars(t *testing.T) {
	payload := `[{"type":"x2v","items":[{
		"id":"schema-1",
		"req_key":"Doubao-Seedance-2.0-pro",
		"inference_type":"x2v",
		"task_type":"t2v",
		"max_image_count":"4",
		"max_prompt_length":"2000",
		"need_return_data_config":1,
		"required_lumi_resource_uri":"false",
		"schema":{"config_schemas":[{"name":"duration","data_type":"int","visible":"true"}]}
	}]}]`

	var buckets []VideoSchemaBucket
	require.NoError(t, json.Unmarshal([]byte(payload), &buckets))
	require.Len(t, buckets, 1)
	require.Len(t, buckets[0].Items, 1)

	schema := buckets[0].Items[0]
	require.Equal(t, "x2v", schema.InferenceType)
	require.Equal(t, "t2v", schema.TaskType)
	require.Equal(t, 4, schema.MaxImageCount.Int())
	require.Equal(t, 2000, schema.MaxPromptLength.Int())
	require.True(t, schema.NeedReturnDataConfig.Bool())
	require.False(t, schema.RequiredLumiResourceURI.Bool())
	require.Len(t, schema.Schema.ConfigSchemas, 1)
	require.True(t, schema.Schema.ConfigSchemas[0].Visible.Bool())
}

// Some endpoints double-encode nested schemas as a JSON string.
func TestVideoSchemaDecodesDoubleEncodedSchema(t *testing.T) {
	payload := `{"id":"schema-2","schema":"{\"input_schemas\":[{\"name\":\"prompt\",\"data_type\":\"string\"}]}"}`

	var schema VideoSchema
	require.NoError(t, json.Unmarshal([]byte(payload), &schema))
	require.Len(t, schema.Schema.InputSchemas, 1)
	require.Equal(t, "prompt", schema.Schema.InputSchemas[0].Name)

	// A missing or null schema decodes to an empty field set instead of failing.
	var nullSchema VideoSchema
	require.NoError(t, json.Unmarshal([]byte(`{"id":"schema-3","schema":null}`), &nullSchema))
	require.Empty(t, nullSchema.AllSchemaFields())
}

func TestImageServiceDecodesStringTypedScalars(t *testing.T) {
	payload := `{
		"id":"svc-1",
		"req_key":"key-1",
		"inference_types":["t2i"],
		"sub_task_count":"2",
		"req_content":{"contents":[{"type":"text","multiple":"false","max_length":"1000"}]}
	}`

	var service ImageService
	require.NoError(t, json.Unmarshal([]byte(payload), &service))
	require.Equal(t, 2, service.SubTaskCount.Int())
	require.Len(t, service.ReqContent.Contents, 1)
	require.False(t, service.ReqContent.Contents[0].Multiple.Bool())
	require.Equal(t, 1000, service.ReqContent.Contents[0].MaxLength.Int())
}

// Unusable scalars degrade to the zero value: descriptive metadata must never
// cost us the catalog.
func TestFlexScalarsDegradeInsteadOfFailing(t *testing.T) {
	boolCases := map[string]bool{
		`true`: true, `"true"`: true, `"TRUE"`: true, `1`: true, `"1"`: true, `2`: true,
		`false`: false, `"false"`: false, `0`: false, `""`: false, `null`: false,
		`"maybe"`: false, `{}`: false, `[]`: false,
	}
	for raw, expected := range boolCases {
		var value FlexBool
		require.NoErrorf(t, json.Unmarshal([]byte(raw), &value), "bool input %s", raw)
		require.Equalf(t, expected, value.Bool(), "bool input %s", raw)
	}

	intCases := map[string]int{
		`7`: 7, `"7"`: 7, `-3`: -3, `"4.9"`: 4, `4.9`: 4, `""`: 0, `null`: 0, `"abc"`: 0, `{}`: 0, `[]`: 0,
	}
	for raw, expected := range intCases {
		var value FlexInt
		require.NoErrorf(t, json.Unmarshal([]byte(raw), &value), "int input %s", raw)
		require.Equalf(t, expected, value.Int(), "int input %s", raw)
	}
}
