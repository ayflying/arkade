package httpx

import (
	"arkade/protocol/envelope"
	"encoding/json"
	"net/http"
	"testing"
)

// All 14 native routes must preserve the old permission/validation/op ordering.
func TestResourcesNativeRoutesAndSources(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.login(t)
	e.seedAgent(t, "native-resources")
	base := "/api/agents/native-resources"
	for _, tc := range []struct {
		method, path, body string
		status             int
		message            string
	}{
		{"GET", "/containers", "", 503, ""},
		{"GET", "/containers/c", "", 503, ""},
		{"POST", "/containers/c/start", "not JSON", 503, ""},
		{"POST", "/containers/c/stop", "not JSON", 503, ""},
		{"POST", "/containers/c/restart", "not JSON", 503, ""},
		{"DELETE", "/containers/c?force=invalid&force=1", "", 400, "force 只能是 true/false/1/0"},
		{"DELETE", "/containers/c?force=1&force=invalid&remove_volumes=n", "ignored", 503, ""},
		{"DELETE", "/containers/c?remove_volumes=invalid", "", 400, "remove_volumes 只能是 true/false/1/0"},
		{"GET", "/containers/c/logs?tail=invalid&tail=1", "", 400, "tail 必须是 1 到 2000 之间的整数"},
		{"GET", "/containers/c/logs?tail=1&tail=invalid&follow=garbage", "", 503, ""},
		{"GET", "/images", "", 503, ""},
		{"DELETE", "/images/image?force=invalid", "ignored", 503, ""},
		{"GET", "/networks", "", 503, ""},
		{"DELETE", "/networks/network", "ignored", 503, ""},
		{"GET", "/volumes", "", 503, ""},
		{"DELETE", "/volumes/volume", "ignored", 503, ""},
		{"POST", "/volume-files", `{"volume":"valid","operation":"list","path":""}`, 503, ""},
		{"POST", "/volume-files", `{"volume":"valid","operation":"list","unknown":1}`, 400, "文件操作请求无效或超过4MiB限制"},
		{"POST", "/volume-files", `{"volume":"valid","operation":"list"} {}`, 400, "文件路径、操作或内容无效；覆盖已有文件须提供原始校验值"},
	} {
		rec, body := e.do(t, tc.method, base+tc.path, tc.body, cookies)
		if rec.Code != tc.status || tc.message != "" && body.Error.Message != tc.message {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
	for _, key := range []string{"agentID", "AgentID", "agent_id", "AGENTID"} {
		rec, _ := e.do(t, http.MethodGet, base+"/containers?"+key+"=missing", "", cookies)
		if rec.Code != 503 {
			t.Fatalf("query %s overrode agent path: %d %s", key, rec.Code, rec.Body.String())
		}
	}
	rec, _ := e.do(t, http.MethodPost, "/api/agents/missing/volume-files", "invalid", cookies)
	if rec.Code != 404 {
		t.Fatalf("decode preceded resource lookup: %d %s", rec.Code, rec.Body.String())
	}
	rec, _ = e.do(t, http.MethodPost, base+"/volume-files", "invalid", nil)
	if rec.Code != 401 {
		t.Fatalf("decode preceded authentication: %d %s", rec.Code, rec.Body.String())
	}
}

func TestResourcesNativeEmptyArraysAndForceFirst(t *testing.T) {
	e := newTestEnv(t)
	a := startImportAgent(t, e, func(req envelope.Envelope, _ int) any {
		switch req.Method {
		case envelope.MethodImageList:
			return envelope.ImageListResult{}
		case envelope.MethodNetworkList:
			return envelope.NetworkListResult{}
		case envelope.MethodVolumeList:
			return envelope.VolumeListResult{}
		case envelope.MethodImageRemove:
			var p envelope.ImageRemovePayload
			if err := envelope.DecodePayload(&req, &p); err != nil {
				t.Error(err)
			}
			if p.ImageID != "target" || p.Force {
				t.Errorf("first force/path lost: %+v", p)
			}
			return struct{}{}
		}
		t.Errorf("unexpected RPC %s", req.Method)
		return struct{}{}
	})
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	for _, resource := range []string{"images", "networks", "volumes"} {
		resp, body := doHTTP(t, http.MethodGet, a.url+"/api/agents/"+a.id+"/"+resource, "", cookies)
		var out map[string]json.RawMessage
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 || string(out[resource]) != "[]" {
			t.Fatalf("%s: %d %s", resource, resp.StatusCode, body)
		}
	}
	resp, body := doHTTP(t, http.MethodDelete, a.url+"/api/agents/"+a.id+"/images/target?force=false&force=true&imageID=wrong", "ignored", cookies)
	if resp.StatusCode != 204 || len(body) != 0 {
		t.Fatalf("delete: %d %s", resp.StatusCode, body)
	}
}
