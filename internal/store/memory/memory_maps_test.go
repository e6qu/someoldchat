package memory

import (
	"reflect"
	"testing"
)

// TestNewInitialisesEveryMap requires New to build every map and slice-of-map
// field Store declares. A field the constructor omits compiles, reads as empty,
// and panics on the first write, which is how authPolicyEntities reached the
// parity harness.
func TestNewInitialisesEveryMap(t *testing.T) {
	value := reflect.ValueOf(New()).Elem()
	structure := value.Type()
	checked := 0
	for index := 0; index < structure.NumField(); index++ {
		field := structure.Field(index)
		if field.Type.Kind() != reflect.Map {
			continue
		}
		checked++
		if value.Field(index).IsNil() {
			t.Errorf("Store.%s is a map that New leaves nil, so the first write to it panics", field.Name)
		}
	}
	if checked == 0 {
		t.Fatal("read no map fields on Store; the reflection walk is wrong")
	}
}
