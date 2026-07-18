package slack

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func FuzzNormalizeJSONScalarNeverPanics(f *testing.F) {
	f.Add(`"text"`)
	f.Add(`true`)
	f.Add(`12.5`)
	f.Add(`{"nested":true}`)
	f.Fuzz(func(_ *testing.T, value string) {
		_, _ = normalizeJSONScalar([]byte(value))
	})
}

func FuzzDecodeFieldsNeverPanics(f *testing.F) {
	f.Add(`{"channel":"C1","limit":20}`)
	f.Add(`{"user_ids":["U1","U2"]}`)
	f.Add(`{"channel":"C1","channel":"C2"}`)
	f.Add("not-json")
	f.Fuzz(func(_ *testing.T, value string) {
		request := httptest.NewRequest("POST", "/api/test", strings.NewReader(value))
		request.Header.Set("Content-Type", "application/json")
		_, _ = decodeFields(httptest.NewRecorder(), request)
	})
}

func FuzzNormalizeJSONListFieldNeverPanics(f *testing.F) {
	f.Add(`["U1", "U2"]`)
	f.Add(`"U1,U2"`)
	f.Add(`null`)
	f.Fuzz(func(_ *testing.T, value string) {
		_, _ = normalizeJSONListField([]byte(value))
	})
}
