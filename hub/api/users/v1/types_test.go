package v1

import (
	"encoding/json"
	"testing"

	"github.com/gogf/gf/v2/util/gconv"
)

func TestOptionalJSON(t *testing.T) {
	for _, tc := range []struct {
		body       string
		set, value bool
	}{
		{`{}`, false, false}, {`{"disabled":null}`, true, false},
		{`{"disabled":false}`, true, false}, {`{"disabled":true}`, true, true},
	} {
		var req UpdateReq
		if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
			t.Fatal(err)
		}
		if req.Disabled.Set != tc.set || req.Disabled.Value != tc.value {
			t.Fatalf("%s: got %+v", tc.body, req.Disabled)
		}
	}
}

// GF's generic struct conversion must not be assumed to preserve JSON null.
func TestOptionalGFConversion(t *testing.T) {
	for _, value := range []any{true, false, "x", nil} {
		var req UpdateReq
		field := "disabled"
		if _, ok := value.(string); ok {
			field = "display_name"
		}
		if err := gconv.Struct(map[string]any{field: value}, &req); err != nil {
			t.Fatal(err)
		}
		switch typed := value.(type) {
		case bool:
			if !req.Disabled.Set || req.Disabled.Value != typed {
				t.Fatalf("bool conversion: %+v", req.Disabled)
			}
		case string:
			if !req.DisplayName.Set || req.DisplayName.Value != typed {
				t.Fatalf("string conversion: %+v", req.DisplayName)
			}
		case nil:
			// This deliberately documents the GF converter limitation; the binding
			// must use the strict JSON-decoded request for nullable PATCH bodies.
			if req.Disabled.Set {
				t.Fatal("GF nil behavior changed: revisit native request recovery")
			}
		}
	}
}
