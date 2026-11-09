package runtime

// image_transfer_engine_test.go 用 httptest 假引擎验证镜像传输的 HTTP 契约。
//
// 这些用例直接断言**请求内容**（尤其是 query 里必须保留 digest），
// 因为这类错误无法从"调用返回成功"看出来：
// 拉错版本号的镜像同样会成功。
//
// 端点用 tcp:// 指向 httptest 的监听地址：既不需要 Docker，
// 也不需要伪造 unix 套接字，走的仍是生产同一条 HTTP 客户端路径。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// newTestEngine 起一个假引擎并返回 engine。
func newTestEngine(t *testing.T, handler http.HandlerFunc) *engine {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	ep := strings.TrimPrefix(srv.URL, "http://")
	c, err := newClient("tcp://" + ep)
	if err != nil {
		t.Fatalf("构造客户端失败: %v", err)
	}
	return &engine{kind: KindDocker, client: c, endpoint: "tcp://" + ep}
}

// TestPullPreservesDigestInQuery 是本次 review 抓到的真实缺陷的回归测试。
//
// 缺陷形态：把 "nginx@sha256:xxx" 拆成 (nginx, "")，digest 被整段丢掉。
// Docker 收到没有 tag 的 fromImage 会自动补 ":latest"，
// 于是"按 digest 拉取"实际拉成了最新版，而调用方以为拿到了确定版本。
//
// 断言两点：
//  1. query 的 fromImage 必须**保留** @sha256:xxx；
//  2. 不能出现 tag=latest 之类的补全。
func TestPullPreservesDigestInQuery(t *testing.T) {
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	var mu sync.Mutex
	var gotQuery map[string]string

	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/json") {
			// 拉取后要做 digest 校验，假引擎必须能回答 inspect，
			// 且报告的 digest 与要求一致。
			_, _ = io.WriteString(w, `{"Id":"sha256:abc","RepoTags":["nginx"],"Size":100,"RepoDigests":["nginx@`+digest+`"]}`)
			return
		}
		if r.URL.Path != pathImageCreate {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		gotQuery = map[string]string{}
		for k := range r.URL.Query() {
			gotQuery[k] = r.URL.Query().Get(k)
		}
		mu.Unlock()
		_, _ = io.WriteString(w, `{"status":"Pulling from library/nginx"}`+"\n")
	})

	ref := "nginx@" + digest
	if _, err := e.PullImage(context.Background(), ref, "", "", nil); err != nil {
		t.Fatalf("拉取应成功: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotQuery == nil {
		t.Fatalf("未收到 /images/create 请求")
	}
	fromImage := gotQuery["fromImage"]
	if !strings.Contains(fromImage, "@"+digest) {
		t.Fatalf("fromImage 必须保留 digest，实际: %q", fromImage)
	}
	if tag := gotQuery["tag"]; tag != "" {
		t.Fatalf("带 digest 的引用不应额外传 tag，实际 tag=%q", tag)
	}
}

// TestPullSplitsTagCorrectly 验证普通 tag 拆分，且不会把 registry 端口当 tag。
func TestPullSplitsTagCorrectly(t *testing.T) {
	cases := []struct {
		ref      string
		wantFrom string
		wantTag  string
	}{
		{"nginx:1.25", "nginx", "1.25"},
		{"ghcr.io/a/b:latest", "ghcr.io/a/b", "latest"},
		{"nginx", "nginx", ""},
		{"127.0.0.1:5000/app", "127.0.0.1:5000/app", ""},
		{"127.0.0.1:5000/app:v1", "127.0.0.1:5000/app", "v1"},
	}
	for _, tc := range cases {
		captured := map[string]string{}
		e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
			for k := range r.URL.Query() {
				captured[k] = r.URL.Query().Get(k)
			}
			_, _ = io.WriteString(w, `{"status":"ok"}`+"\n")
		})
		if _, err := e.PullImage(context.Background(), tc.ref, "", "", nil); err != nil {
			t.Fatalf("%s 拉取失败: %v", tc.ref, err)
		}
		if captured["fromImage"] != tc.wantFrom || captured["tag"] != tc.wantTag {
			t.Fatalf("%s: fromImage=%q tag=%q，期望 %q/%q",
				tc.ref, captured["fromImage"], captured["tag"], tc.wantFrom, tc.wantTag)
		}
	}
}

// TestPullFailsOnErrorDetailOnly 验证只有 errorDetail（无顶层 error）时也判失败。
//
// 只认顶层 error 的实现会把这类失败读成"流正常结束"进而判定成功，
// 用户看到 succeeded 但镜像根本没进来。
func TestPullFailsOnErrorDetailOnly(t *testing.T) {
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == pathImageCreate {
			_, _ = io.WriteString(w, `{"errorDetail":{"message":"unauthorized: authentication required"}}`+"\n")
			return
		}
		http.NotFound(w, r)
	})

	_, err := e.PullImage(context.Background(), "private/app:1", "", "", nil)
	if err == nil {
		t.Fatalf("只有 errorDetail 的失败也必须返回错误")
	}
	if !strings.Contains(err.Error(), ErrPullRejected.Error()) {
		t.Fatalf("应归为拉取被拒绝，实际: %v", err)
	}
	if strings.Contains(err.Error(), "authentication required") {
		t.Fatalf("错误文案不得包含引擎原文: %v", err)
	}
}

// TestPullRejectsEmptyStream 验证空流不算成功。
//
// 引擎在错误情况下可能返回 200 加空响应体。若直接判成功，
// 用户会看到 succeeded 但镜像不存在。这里假引擎对 inspect 返回 404，
// 因此拉取必须失败。
func TestPullRejectsEmptyStream(t *testing.T) {
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == pathImageCreate {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	})

	if _, err := e.PullImage(context.Background(), "ghost:1", "", "", nil); err == nil {
		t.Fatalf("空流且镜像查不到时必须判失败")
	}
}

// TestPullAcceptsEmptyStreamWhenImageExists 验证空流但镜像确实存在时算成功。
//
// 有的引擎对"已是最新"的镜像返回空流。此时 inspect 能查到镜像，
// 就不该误报失败，否则重复拉取同一镜像永远不会成功。
func TestPullAcceptsEmptyStreamWhenImageExists(t *testing.T) {
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == pathImageCreate:
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/json"):
			_, _ = io.WriteString(w, `{"Id":"sha256:abc","RepoTags":["app:1"],"Size":100}`)
		default:
			http.NotFound(w, r)
		}
	})

	if _, err := e.PullImage(context.Background(), "app:1", "", "", nil); err != nil {
		t.Fatalf("镜像确实存在时空流应算成功: %v", err)
	}
}

// TestPullVerifiesDigestFromReference 验证引用自带 digest 时会校验拉取结果。
//
// payload.Digest 为空但引用里写了 @sha256:xxx 时仍必须校验，
// 这是最常见的用法（用户直接粘贴带 digest 的引用）。
func TestPullVerifiesDigestFromReference(t *testing.T) {
	const wantDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const otherDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == pathImageCreate:
			_, _ = io.WriteString(w, `{"status":"ok"}`+"\n")
		case strings.HasSuffix(r.URL.Path, "/json"):
			body, _ := json.Marshal(map[string]any{
				"Id": "sha256:abc", "Size": 100,
				"RepoDigests": []string{"app@" + otherDigest},
			})
			_, _ = w.Write(body)
		default:
			http.NotFound(w, r)
		}
	})

	_, err := e.PullImage(context.Background(), "app@"+wantDigest, "", "", nil)
	if err == nil {
		t.Fatalf("引用带 digest 但实际不符时必须失败")
	}
	if !strings.Contains(err.Error(), ErrDigestMismatch.Error()) {
		t.Fatalf("应归为 digest 不匹配，实际: %v", err)
	}
}

// TestLoadUsesTarContentType 验证导入请求的 Content-Type 与 Content-Length。
//
// 引擎对这个头敏感：发成 application/json 会被当作 JSON 解析并报错。
// 长度也必须确切给出，否则走 chunked 编码在某些引擎版本上行为不一致。
func TestLoadUsesTarContentType(t *testing.T) {
	var gotCT string
	var gotLen int64 = -1
	const payload = "fake-tar-bytes"

	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != pathImageLoad {
			http.NotFound(w, r)
			return
		}
		gotCT = r.Header.Get("Content-Type")
		gotLen = r.ContentLength
		body, _ := io.ReadAll(r.Body)
		if string(body) != payload {
			t.Errorf("请求体不符: %q", string(body))
		}
		_, _ = io.WriteString(w, `{"stream":"Loaded image: app:1\n"}`+"\n")
	})

	images, err := e.LoadImage(context.Background(), bytes.NewReader([]byte(payload)), int64(len(payload)))
	if err != nil {
		t.Fatalf("导入应成功: %v", err)
	}
	if gotCT != "application/x-tar" {
		t.Fatalf("Content-Type 必须是 application/x-tar，实际 %q", gotCT)
	}
	if gotLen != int64(len(payload)) {
		t.Fatalf("Content-Length 必须是确切字节数 %d，实际 %d", len(payload), gotLen)
	}
	if len(images) != 1 || len(images[0].Tags) != 1 || images[0].Tags[0] != "app:1" {
		t.Fatalf("应解析出加载的镜像名，实际: %+v", images)
	}
}

// TestLoadRejectsNonPositiveSize 验证必须给出确切字节数。
func TestLoadRejectsNonPositiveSize(t *testing.T) {
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	for _, size := range []int64{0, -1} {
		if _, err := e.LoadImage(context.Background(), bytes.NewReader([]byte("x")), size); err == nil {
			t.Fatalf("size=%d 应被拒绝", size)
		}
	}
}

// TestLoadFailsOnStreamError 验证 load 响应里的错误行不被吞掉。
//
// 归档损坏时引擎常返回 200，然后在流里写 errorDetail。
// 只看 HTTP 状态会把"什么都没导入"报告成成功。
func TestLoadFailsOnStreamError(t *testing.T) {
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"errorDetail":{"message":"unexpected EOF"}}`+"\n")
	})
	if _, err := e.LoadImage(context.Background(), bytes.NewReader([]byte("bad")), 3); err == nil {
		t.Fatalf("load 流里的错误行必须导致失败")
	}
}

// TestSaveReturnsStreamNotBuffered 验证导出走流式路径（不被 16MiB 截断）。
//
// 若走 do()，响应体会被 io.LimitReader 截断到 maxResponseBytes，
// 得到的是一份"看起来下载完成"的损坏归档。
func TestSaveReturnsStreamNotBuffered(t *testing.T) {
	big := bytes.Repeat([]byte("A"), maxResponseBytes+1024)
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/json"):
			_, _ = io.WriteString(w, `{"Id":"sha256:abc","RepoTags":["app:1"],"Size":1}`)
		case strings.HasSuffix(r.URL.Path, "/get"):
			_, _ = w.Write(big)
		default:
			http.NotFound(w, r)
		}
	})

	rc, err := e.SaveImage(context.Background(), "app:1")
	if err != nil {
		t.Fatalf("导出应成功: %v", err)
	}
	defer func() { _ = rc.Close() }()

	n, rerr := io.Copy(io.Discard, rc)
	if rerr != nil {
		t.Fatalf("读取导出流失败: %v", rerr)
	}
	if n != int64(len(big)) {
		t.Fatalf("导出流被截断：读到 %d 字节，期望 %d", n, len(big))
	}
}

// TestInspectImageAcceptsRFC3339Created 用**真实的 Docker inspect 形状**验证解析。
//
// 真机 export 失败过：/images/{name}/json 的 Created 是 RFC3339 字符串，
// 而结构体按 int64 解析，报
//
//	cannot unmarshal string into field Created
//
// 整个导出因此失败。这里直接内嵌一段真机返回的字段形状（含 Created 字符串、
// RepoDigests、Os/Architecture 等），防止回归。
func TestInspectImageAcceptsRFC3339Created(t *testing.T) {
	// 这段 JSON 是该端点的真实字段形态：注意 Created 是带引号的时间串。
	const realShape = `{
		"Id": "sha256:2c2c1b0fb0e0a0b0c0d0e0f0a0b0c0d0e0f0a0b0c0d0e0f0a0b0c0d0e0f0a0b0",
		"RepoTags": ["arkade-image-verification:transfer"],
		"RepoDigests": ["arkade-image-verification@sha256:2c2c1b0fb0e0a0b0c0d0e0f0a0b0c0d0e0f0a0b0c0d0e0f0a0b0c0d0e0f0a0b0"],
		"Created": "2026-11-10T02:00:00.123456789Z",
		"Size": 7712345,
		"Os": "linux",
		"Architecture": "amd64",
		"Variant": "",
		"Config": {"Env": ["PATH=/usr/bin"]}
	}`

	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/json") {
			_, _ = io.WriteString(w, realShape)
			return
		}
		http.NotFound(w, r)
	})

	d, err := e.InspectImage(context.Background(), "arkade-image-verification:transfer")
	if err != nil {
		t.Fatalf("解析真实 inspect 形状失败: %v", err)
	}
	if d.Size != 7712345 {
		t.Fatalf("Size 解析错误: %d", d.Size)
	}
	if d.Created == 0 {
		t.Fatalf("Created 字符串应被解析成 Unix 秒，实际为 0")
	}
	if d.Platform != "linux/amd64" {
		t.Fatalf("Platform 应为 linux/amd64，实际 %q", d.Platform)
	}
	if len(d.Digests) != 1 {
		t.Fatalf("RepoDigests 未解析出来: %+v", d.Digests)
	}
}

// TestInspectImageAcceptsIntegerCreated 兼容旧引擎返回整数秒的形态。
func TestInspectImageAcceptsIntegerCreated(t *testing.T) {
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/json") {
			_, _ = io.WriteString(w, `{"Id":"sha256:abc","Size":100,"Created":1700000000,"Os":"linux","Architecture":"arm64"}`)
			return
		}
		http.NotFound(w, r)
	})
	d, err := e.InspectImage(context.Background(), "app:1")
	if err != nil {
		t.Fatalf("整数 Created 也应能解析: %v", err)
	}
	if d.Created != 1700000000 {
		t.Fatalf("Created 应为 1700000000，实际 %d", d.Created)
	}
}

// TestSanitizeEngineErrorHidesDetails 验证错误脱敏。
func TestSanitizeEngineErrorHidesDetails(t *testing.T) {
	cases := []struct {
		raw       string
		mustNotIn string
	}{
		{"unauthorized: authentication required for registry.example.internal:5000", "registry.example.internal"},
		{"manifest for 10.0.0.5/secret/app not found", "10.0.0.5"},
	}
	for _, tc := range cases {
		got := sanitizeEngineError(tc.raw)
		if strings.Contains(got, tc.mustNotIn) {
			t.Fatalf("脱敏后仍含敏感串 %q: %q", tc.mustNotIn, got)
		}
		if got == "" {
			t.Fatalf("脱敏结果不应为空")
		}
	}
}
