package hyperliquid

import (
	"bytes"
	"encoding/json"
	"fmt"
)

func requireJSONObjectFields(data []byte, fields ...string) error {
	object := make(map[string]json.RawMessage)
	if err := json.Unmarshal(data, &object); err != nil {
		return fmt.Errorf("decoding JSON object: %w", err)
	}
	for _, field := range fields {
		if _, ok := object[field]; !ok {
			return fmt.Errorf("required field %q is missing", field)
		}
	}
	return nil
}

func requireNonNullJSONObjectFields(data []byte, fields ...string) error {
	object := make(map[string]json.RawMessage)
	if err := json.Unmarshal(data, &object); err != nil {
		return fmt.Errorf("decoding JSON object: %w", err)
	}
	for _, field := range fields {
		value, ok := object[field]
		if !ok {
			return fmt.Errorf("required field %q is missing", field)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("required field %q is null", field)
		}
	}
	return nil
}
