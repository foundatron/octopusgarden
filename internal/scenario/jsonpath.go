package scenario

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	errInvalidJSONPath   = errors.New("jsonpath: invalid path, must start with dollar-dot prefix")
	errJSONPathNotFound  = errors.New("jsonpath: key not found")
	errJSONPathNotObject = errors.New("jsonpath: intermediate value is not an object")
	errJSONPathNotScalar = errors.New("jsonpath: leaf value is not a scalar")
)

// evalJSONPath extracts a scalar value from a JSON body using dot-notation.
// Supported paths: $.field, $.field.subfield, etc.
func evalJSONPath(body string, path string) (string, error) {
	if !strings.HasPrefix(path, "$.") {
		return "", errInvalidJSONPath
	}

	keys := strings.Split(path[2:], ".")
	if len(keys) == 0 || keys[0] == "" {
		return "", errInvalidJSONPath
	}

	var root any
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		return "", fmt.Errorf("jsonpath: unmarshal: %w", err)
	}

	current := root
	for i, key := range keys {
		obj, ok := current.(map[string]any)
		if !ok {
			if i == 0 {
				return "", fmt.Errorf("%w: root is not an object", errJSONPathNotObject)
			}
			return "", fmt.Errorf("%w: at key %q", errJSONPathNotObject, keys[i-1])
		}
		val, exists := obj[key]
		if !exists {
			return "", fmt.Errorf("%w: %q", errJSONPathNotFound, key)
		}
		current = val
	}

	return scalarToString(current)
}

func scalarToString(v any) (string, error) {
	switch val := v.(type) {
	case string:
		return val, nil
	case float64:
		// Format integers without decimal point.
		if float64(int64(val)) == val {
			return fmt.Sprintf("%d", int64(val)), nil
		}
		return fmt.Sprintf("%g", val), nil
	case bool:
		return fmt.Sprintf("%t", val), nil
	case nil:
		return "null", nil
	default:
		return "", errJSONPathNotScalar
	}
}
