package lumina

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// BytePlus serializes the same catalog field as a bool, a number or a quoted
// string depending on the endpoint (`required_lumi_resource_uri` arrives as
// "false", for example). encoding/json rejects those mismatches, and because
// the catalog is decoded as one document a single odd field fails the whole
// model list. FlexBool/FlexInt accept every scalar shape so metadata we never
// read can no longer break model discovery.
//
// Both types are deliberately lenient: an unparsable scalar decodes to the zero
// value instead of erroring. Only descriptive fields use them, so degrading one
// value is always better than losing the catalog.
type (
	FlexBool bool
	FlexInt  int
)

func (b *FlexBool) UnmarshalJSON(data []byte) error {
	scalar, ok := unquoteJSONScalar(data)
	if !ok {
		return nil
	}
	switch scalar {
	case "true", "1":
		*b = true
	case "false", "0":
		*b = false
	default:
		if number, err := strconv.ParseFloat(scalar, 64); err == nil {
			*b = number != 0
			return nil
		}
		parsed, err := strconv.ParseBool(scalar)
		if err != nil {
			return nil
		}
		*b = FlexBool(parsed)
	}
	return nil
}

func (b FlexBool) Bool() bool {
	return bool(b)
}

func (i *FlexInt) UnmarshalJSON(data []byte) error {
	scalar, ok := unquoteJSONScalar(data)
	if !ok {
		return nil
	}
	if parsed, err := strconv.ParseInt(scalar, 10, 64); err == nil {
		*i = FlexInt(parsed)
		return nil
	}
	// Upstream occasionally reports counts as floats ("4.0"); truncate instead
	// of dropping the value.
	if parsed, err := strconv.ParseFloat(scalar, 64); err == nil {
		*i = FlexInt(int64(parsed))
	}
	return nil
}

func (i FlexInt) Int() int {
	return int(i)
}

// unquoteJSONScalar reduces a JSON scalar to its textual form, reporting false
// for null, empty input and non-scalar shapes (objects, arrays) that carry no
// usable value.
func unquoteJSONScalar(data []byte) (string, bool) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", false
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return "", false
		}
		if text == "" {
			return "", false
		}
		return text, true
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		return "", false
	}
	return string(trimmed), true
}
