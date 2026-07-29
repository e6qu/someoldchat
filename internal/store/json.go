package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// MergeJSONObjects overlays a JSON object onto another and returns canonical
// JSON. Datastore stores call it while holding their transaction/lock so an
// apps.datastore.update cannot lose a concurrent update between a read and
// write.
func MergeJSONObjects(existing, patch string) (string, error) {
	merged := make(map[string]any)
	if strings.TrimSpace(existing) != "" {
		decoder := json.NewDecoder(strings.NewReader(existing))
		decoder.UseNumber()
		if err := decoder.Decode(&merged); err != nil {
			return "", fmt.Errorf("stored app datastore item is invalid: %w", err)
		}
		if merged == nil {
			return "", errors.New("stored app datastore item is not an object")
		}
		if err := requireJSONEnd(decoder); err != nil {
			return "", fmt.Errorf("stored app datastore item is invalid: %w", err)
		}
	}
	decoder := json.NewDecoder(strings.NewReader(patch))
	decoder.UseNumber()
	var changes map[string]any
	if err := decoder.Decode(&changes); err != nil || changes == nil {
		return "", InvalidArgument("app datastore patch must be a JSON object")
	}
	if err := requireJSONEnd(decoder); err != nil {
		return "", InvalidArgument("app datastore patch must contain one JSON object")
	}
	for name, value := range changes {
		merged[name] = value
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func requireJSONEnd(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("contains multiple JSON values")
	}
	return err
}
