package resources

import (
	"encoding/base64"
	"strings"
	"testing"

	api "arkade/hub/api/resources/v1"
)

func TestTailCompatibility(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		value int
		valid bool
	}{
		{"", 200, true}, {"1", 1, true}, {"2000", 2000, true}, {"+2", 2, true}, {" 2", 0, false}, {"0", 0, false}, {"2001", 0, false}, {"1.0", 0, false},
	} {
		n, err := parseTail(tc.raw)
		if (err == nil) != tc.valid || tc.valid && n != tc.value {
			t.Fatalf("tail %q = %d,%v", tc.raw, n, err)
		}
	}
}
func TestContainerBoolCompatibility(t *testing.T) {
	for _, raw := range []string{"1", "true", "t", "yes", "y", " TRUE "} {
		value, ok := parseBoolQuery(raw)
		if !ok || !value {
			t.Fatalf("true %q", raw)
		}
	}
	for _, raw := range []string{"", "0", "false", "f", "no", "n", " FALSE "} {
		value, ok := parseBoolQuery(raw)
		if !ok || value {
			t.Fatalf("false %q", raw)
		}
	}
	if _, ok := parseBoolQuery("invalid"); ok {
		t.Fatal("accepted invalid bool")
	}
}
func TestVolumeFileValidationCompatibility(t *testing.T) {
	cases := []struct {
		p     api.VolumeFilesInput
		valid bool
	}{
		{api.VolumeFilesInput{Volume: "data", Operation: "list"}, true},
		{api.VolumeFilesInput{Volume: "data", Operation: "list", Path: "."}, true},
		{api.VolumeFilesInput{Volume: "data", Operation: "read", Path: ".."}, false},
		{api.VolumeFilesInput{Volume: "data", Operation: "read", Path: "a\\b"}, false},
		{api.VolumeFilesInput{Volume: "data", Operation: "rename", Path: "a", Destination: "a"}, false},
		{api.VolumeFilesInput{Volume: "data", Operation: "rename", Path: "a", Destination: "b"}, true},
		{api.VolumeFilesInput{Volume: "data", Operation: "write", Path: "a", CreateOnly: true, DataBase64: base64.StdEncoding.EncodeToString([]byte("x"))}, true},
		{api.VolumeFilesInput{Volume: "data", Operation: "write", Path: "a"}, false},
		{api.VolumeFilesInput{Volume: "data", Operation: "write", Path: "a", ExpectedSHA256: strings.Repeat("ab", 32)}, true},
		{api.VolumeFilesInput{Volume: "data", Operation: "read", Path: "a", CreateOnly: true}, false},
	}
	for _, tc := range cases {
		if got := validVolumeRequest(tc.p); got != tc.valid {
			t.Fatalf("%+v = %v", tc.p, got)
		}
	}
}
