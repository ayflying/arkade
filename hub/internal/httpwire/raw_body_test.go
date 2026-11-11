package httpwire

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHideRawBodyPreservesReaderWithoutParsing(t *testing.T) {
	original := httptest.NewRequest(http.MethodPost, "/api/upload", strings.NewReader("not JSON, just archive bytes"))
	original.Header.Set("Content-Type", "application/json")
	hidden := HideRawBody(original)
	if hidden.Body != http.NoBody || hidden.ContentLength != 0 || !HasRawBody(hidden.Context()) {
		t.Fatal("body exposed to framework")
	}
	raw := hidden.Context().Value(rawBodyKey{}).(rawRequestBody)
	if raw.body != original.Body || raw.length != original.ContentLength {
		t.Fatal("original transport lost")
	}
	first := make([]byte, 4)
	if _, err := io.ReadFull(raw.body, first); err != nil {
		t.Fatal(err)
	}
	// Reading stored state twice points to the same stream; it must not replay bytes.
	again := hidden.Context().Value(rawBodyKey{}).(rawRequestBody)
	rest, err := io.ReadAll(again.body)
	if err != nil || string(first)+string(rest) != "not JSON, just archive bytes" {
		t.Fatalf("reader replayed/truncated: %s %s %v", first, rest, err)
	}
	if original.Header.Get("Content-Type") != "application/json" {
		t.Fatal("header changed")
	}
}
