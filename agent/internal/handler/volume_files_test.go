package handler

// volume_files_test.go：卷文件处理器的参数校验与错误码映射测试。

import (
	"context"
	"encoding/json"
	"testing"

	"arkade/protocol/envelope"
)

// volumeFilesFake 是实现了卷文件可选能力的替身。
type volumeFilesFake struct {
	fakeRuntime
	resp map[string]any
	err  error
}

func (f *volumeFilesFake) RunVolumeFileHelper(context.Context, string, map[string]any) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return json.Marshal(f.resp)
}

// runVolumeFiles 用给定载荷调用处理器，返回响应信封。
func runVolumeFiles(t *testing.T, rt *volumeFilesFake, p envelope.VolumeFilesPayload) *envelope.Envelope {
	t.Helper()
	reg := NewRegistry()
	reg.Register(envelope.MethodVolumeFiles, newVolumeFilesHandler(rt))
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return reg.Dispatch(context.Background(), &envelope.Envelope{
		V: envelope.Version, ID: "t", Type: envelope.TypeRequest, Method: envelope.MethodVolumeFiles, Payload: raw,
	})
}

// TestVolumeFilesRejectsBadPayloads 验证 Agent 侧独立拒绝非法请求。
func TestVolumeFilesRejectsBadPayloads(t *testing.T) {
	rt := &volumeFilesFake{}
	cases := map[string]envelope.VolumeFilesPayload{
		"空卷名":         {Operation: "list"},
		"卷名含斜杠":       {Volume: "../etc", Operation: "list"},
		"写但无hash":     {Volume: "v", Operation: "write", Path: "a"},
		"create与hash": {Volume: "v", Operation: "write", Path: "a", CreateOnly: true, ExpectedSHA256: "aa"},
		"删根目录":        {Volume: "v", Operation: "delete"},
		"未知操作":        {Volume: "v", Operation: "exec", Path: "a"},
		"路径穿越":        {Volume: "v", Operation: "list", Path: "../x"},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			resp := runVolumeFiles(t, rt, p)
			if resp.Error == nil || resp.Error.Code != envelope.CodeBadRequest {
				t.Fatalf("期望 bad_request，实际 %+v", resp.Error)
			}
		})
	}
}

// TestVolumeFilesHelperErrorsMapToCodes 验证 helper 错误类别映射到正确协议码。
//
// 尤其是 checksum → conflict：若落到 runtime_failed(503)，
// 前端会提示"稍后重试"，而重试永远不会成功。
func TestVolumeFilesHelperErrorsMapToCodes(t *testing.T) {
	cases := []struct {
		code string
		want string
	}{
		{"checksum", envelope.CodeConflict},
		{"exists", envelope.CodeConflict},
		{"not_empty", envelope.CodeConflict},
		{"not_found", envelope.CodeNotFound},
		{"bad_path", envelope.CodeBadRequest},
		{"too_large", envelope.CodeBadRequest},
		{"symlink", envelope.CodeBadRequest},
	}
	for _, c := range cases {
		t.Run(c.code, func(t *testing.T) {
			rt := &volumeFilesFake{resp: map[string]any{"error": "说明", "code": c.code}}
			resp := runVolumeFiles(t, rt, envelope.VolumeFilesPayload{
				Volume: "v", Operation: "list",
			})
			if resp.Error == nil || resp.Error.Code != c.want {
				t.Fatalf("%s 期望 %s，实际 %+v", c.code, c.want, resp.Error)
			}
		})
	}
}

// TestVolumeFilesListSuccess 验证 list 成功路径的字段映射。
func TestVolumeFilesListSuccess(t *testing.T) {
	rt := &volumeFilesFake{resp: map[string]any{
		"path": "",
		"entries": []map[string]any{
			{"name": "a.txt", "kind": "file", "size": 3, "modified_at": 100, "mode": "-rw-r--r--"},
			{"name": "d", "kind": "directory"},
		},
	}}
	resp := runVolumeFiles(t, rt, envelope.VolumeFilesPayload{Volume: "v", Operation: "list"})
	if resp.Error != nil {
		t.Fatalf("不应失败: %+v", resp.Error)
	}
	var out envelope.VolumeFilesResult
	if err := envelope.DecodePayload(resp, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Entries) != 2 || out.Entries[0].Name != "a.txt" || out.Entries[0].Size != 3 {
		t.Fatalf("目录项不符: %+v", out.Entries)
	}
	if out.Entries[1].Kind != "directory" {
		t.Fatalf("kind 不符: %+v", out.Entries[1])
	}
}

// TestVolumeFilesUnsupportedEngine 验证引擎不支持时返回 unsupported。
func TestVolumeFilesUnsupportedEngine(t *testing.T) {
	// plainRuntime 未实现 VolumeFileRuntime。
	reg := NewRegistry()
	reg.Register(envelope.MethodVolumeFiles, newVolumeFilesHandler(&fakeRuntime{}))
	raw, _ := json.Marshal(envelope.VolumeFilesPayload{Volume: "v", Operation: "list"})
	resp := reg.Dispatch(context.Background(), &envelope.Envelope{
		V: envelope.Version, ID: "t", Type: envelope.TypeRequest, Method: envelope.MethodVolumeFiles, Payload: raw,
	})
	if resp.Error == nil || resp.Error.Code != envelope.CodeUnsupported {
		t.Fatalf("期望 unsupported，实际 %+v", resp.Error)
	}
}
