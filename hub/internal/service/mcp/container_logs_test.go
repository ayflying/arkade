package mcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMCPContainerLogsDispatch(t *testing.T) {
	for _, tt := range []struct {
		args, query string
		accepted    bool
	}{
		{`"agentID":"host","containerID":"web"`, "", true},
		{`"agentID":"host","containerID":"web","tail":17`, "tail=17", true},
		{`"agentID":"host","containerID":"web","tail":0`, "", false},
		{`"agentID":"host","containerID":"web","tail":2001`, "", false},
		{`"agentID":"host","containerID":"web","tail":1.5`, "", false},
		{`"agentID":"host","containerID":"web","follow":true`, "", false},
	} {
		t.Run(tt.args, func(t *testing.T) {
			called := false
			h := NewHandler("test", func(_ context.Context, r *http.Request, w http.ResponseWriter, tool string) {
				called = true
				if tool != "arkade_get_container_logs" || r.Method != http.MethodGet || r.URL.Path != "/api/agents/host/containers/web/logs" || r.URL.RawQuery != tt.query {
					t.Errorf("unexpected dispatch: %s %s %s", tool, r.Method, r.URL)
				}
				_, _ = io.WriteString(w, `{"lines":["startup failed"],"truncated":false}`)
			})
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"arkade_get_container_logs","arguments":{`+tt.args+`}}}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			out := httptest.NewRecorder()
			h.ServeHTTP(out, req)
			body := out.Body.String()
			if out.Code != 200 || called != tt.accepted || strings.Contains(body, `"isError":true`) == tt.accepted || strings.Contains(body, "startup failed") != tt.accepted {
				t.Fatalf("unexpected result: called=%t HTTP %d %s", called, out.Code, body)
			}
		})
	}
}

func TestLimitContainerLogs(t *testing.T) {
	limit := maxMCPContainerLogBytes
	longLine := strings.Repeat("错", limit)
	newest := "newest"
	for _, tt := range []struct {
		name                     string
		lines, want              []string
		truncated, wantTruncated bool
	}{
		{"empty", []string{}, []string{}, false, false},
		{"preserve upstream truncation", []string{"recent"}, []string{"recent"}, true, true},
		{"exact fit", []string{strings.Repeat("a", limit-1)}, []string{strings.Repeat("a", limit-1)}, false, false},
		{"prefer newest", []string{"discarded", strings.Repeat("x", limit-1)}, []string{strings.Repeat("x", limit-1)}, false, true},
		{"UTF-8 suffix", []string{"discarded", longLine, newest}, []string{strings.Repeat("错", (limit-len(newest)-2)/3), newest}, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result := map[string]any{"lines": tt.lines, "truncated": tt.truncated}
			limitContainerLogs(result)
			lines := result["lines"].([]string)
			if !reflect.DeepEqual(lines, tt.want) || result["truncated"] != tt.wantTruncated {
				t.Fatalf("unexpected log suffix (%d lines, %d bytes, truncated=%v)", len(lines), len(strings.Join(lines, "\n")), result["truncated"])
			}
			if text := strings.Join(lines, "\n"); len(text) > limit || !utf8.ValidString(text) {
				t.Fatal("log suffix exceeds the budget or breaks UTF-8")
			}
		})
	}
}
