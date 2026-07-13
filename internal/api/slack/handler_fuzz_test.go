package slack

import "testing"

func FuzzNormalizeJSONScalarNeverPanics(f *testing.F) {
	f.Add(`"text"`)
	f.Add(`true`)
	f.Add(`12.5`)
	f.Add(`{"nested":true}`)
	f.Fuzz(func(_ *testing.T, value string) {
		_, _ = normalizeJSONScalar([]byte(value))
	})
}
