package httpx

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModuleBodyPreservesFirstJSONValue(t *testing.T) {
	for _, suffix := range []string{` {"name":"second"}`, ` trailing garbage`} {
		req := httptest.NewRequest("POST", "/api/groups?name=query", strings.NewReader(`{"name":"first","Name":"last"}`+suffix))
		var decoded struct {
			Name string `json:"name"`
		}
		guarded, err := sanitizeModuleBody(req, &decoded)
		if err != nil {
			t.Fatal(err)
		}
		if decoded.Name != "last" || guarded.URL.RawQuery != "" {
			t.Fatalf("legacy decode: %+v %s", decoded, guarded.URL)
		}
		raw, err := io.ReadAll(guarded.Body)
		if err != nil || string(raw) != `{"name":"first","Name":"last"}` {
			t.Fatalf("GF input %s %v", raw, err)
		}
	}
}
