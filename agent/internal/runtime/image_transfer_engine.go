package runtime

// image_transfer_engine.go 实现 ImageTransferRuntime（Docker / Podman 共用）。
//
// ============================ 为什么三条端点都要流式 ============================
//
// client.do() 会把响应体读进内存（上限 16 MiB），这对控制面正确，
// 对 `docker save` 却是致命的：归档是几百 MB 到几 GB 的 tar。
// 用 do() 只有两种结局——被截断（得到损坏归档，且看起来像"下载完成"），
// 或者去掉限制后撑爆 Agent 的堆。因此这里全部走不缓冲的路径。
//
// ============================ 进度聚合 ============================
//
// /images/create 的 NDJSON 每行可能是：
//
//	{"status":"Pulling from library/nginx","id":"latest"}
//	{"status":"Downloading","progressDetail":{"current":1024,"total":8192},"id":"a1b2c3"}
//	{"status":"Extracting","progressDetail":{"current":512,"total":1024},"id":"a1b2c3"}
//
// 注意 id 是**层**的 id：多层并行拉取时，每一层各有一组 current/total。
// 直接把最后一行当整体进度会来回跳。正确做法是按层 id 记录各自进度，
// 再把所有层的 current 与 total 分别求和——总量单调递增，进度不会倒退。
//
// 某些引擎版本（尤其 Podman）不填 progressDetail，只给 status 文本。
// 那种情况下我们**不编造数字**，只更新 Status 字符串。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// 编译期断言：engine 必须实现 ImageTransferRuntime。
var _ ImageTransferRuntime = (*engine)(nil)

// imageProgressLine 是 /images/create 与 /images/load 的 NDJSON 单行。
//
// ============================ 两种错误形态都要认 ============================
//
// Docker 与 Podman 在不同版本/不同失败阶段用了两种错误表达：
//
//	{"error":"...","errorDetail":{"message":"..."}}   ← 常见（新版与 Podman）
//	{"errorDetail":{"message":"..."}}                 ← 部分版本只给 detail
//
// 只看顶层 error 会漏掉第二类：拉取实际失败了，我们却把流读完后
// 判定"成功"——用户看到"succeeded"，镜像却根本没进来。
// 因此 errorDetail.message 非空也必须算失败。
type imageProgressLine struct {
	Status      string `json:"status"`
	ID          string `json:"id"`
	Error       string `json:"error"`
	ErrorDetail struct {
		Message string `json:"message"`
	} `json:"errorDetail"`
	ProgressDetail struct {
		Current int64 `json:"current"`
		Total   int64 `json:"total"`
	} `json:"progressDetail"`
}

// errMessage 返回这一行的错误说明（两种形态任一非空即非空）。
func (l imageProgressLine) errMessage() string {
	if strings.TrimSpace(l.Error) != "" {
		return l.Error
	}
	return l.ErrorDetail.Message
}

// layerProgress 是单层的拉取进度（current / total）。
//
// 提升为包级类型而不再函数内声明：进度聚合的求和函数与拉取循环
// 都要用它，局部类型无法跨函数共享。
type layerProgress struct{ cur, tot int64 }

// PullImage 实现 ImageTransferRuntime。
func (e *engine) PullImage(ctx context.Context, reference, platform, digest string,
	onProgress func(PullProgress)) (PullResult, error) {

	ref := strings.TrimSpace(reference)
	if ref == "" {
		return PullResult{}, errors.New("镜像引用不能为空")
	}

	// fromImage 与 tag 分开传：引擎期望 fromImage 不含 tag，
	// 或整体传入（不同版本行为有差异）。这里按最兼容的方式处理——
	// 有 tag 就拆出来，没有就交给引擎补 latest。
	image, tag := splitImageRef(ref)

	q := url.Values{}
	q.Set("fromImage", image)
	if tag != "" {
		q.Set("tag", tag)
	}
	if platform != "" {
		q.Set("platform", platform)
	}
	path := pathImageCreate + "?" + q.Encode()

	body, err := e.client.postStream(ctx, path)
	if err != nil {
		return PullResult{}, e.translate(err)
	}
	defer func() { _ = body.Close() }()

	// 按层聚合进度，避免并行拉取时进度来回跳。
	layers := make(map[string]layerProgress)
	var lastStatus string

	report := func() {
		var cur, tot int64
		for _, lp := range layers {
			cur += lp.cur
			tot += lp.tot
		}
		if onProgress != nil {
			onProgress(PullProgress{BytesDone: cur, BytesTotal: tot, Status: lastStatus})
		}
	}

	dec := json.NewDecoder(body)
	lineCount := 0
	for {
		var line imageProgressLine
		if err := dec.Decode(&line); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			// 流中断（被取消或网络问题）：如实返回，不假装成功。
			return PullResult{}, fmt.Errorf("读取拉取进度失败: %w", err)
		}
		lineCount++

		// 两种错误形态都要认（见 imageProgressLine 的说明）。
		// **不把引擎原文放进返回值**：调用方会用固定文案，
		// 原文可能含 registry 认证失败详情与完整内网地址。
		if msg := line.errMessage(); msg != "" {
			return PullResult{}, fmt.Errorf("%w: %s", ErrPullRejected, sanitizeEngineError(msg))
		}

		if line.Status != "" {
			lastStatus = normalizePullStatus(line.Status)
		}
		if line.ProgressDetail.Total > 0 {
			layers[line.ID] = layerProgress{cur: line.ProgressDetail.Current, tot: line.ProgressDetail.Total}
			report()
		} else if line.Status != "" && onProgress != nil {
			// 没有数字也要让调用方看到阶段变化。
			onProgress(PullProgress{BytesDone: sumCur(layers), BytesTotal: sumTot(layers), Status: lastStatus})
		}
	}

	// ============================ 空流不能算成功 ============================
	//
	// 引擎在错误情况下可能返回 200 + 空响应体（尤其 Podman 某些版本，
	// 或中间代理吞掉了错误）。此时没有任何 error 行，上面的循环
	// 会安静退出——若直接判定成功，用户会看到"succeeded"但镜像不存在。
	//
	// 因此这里用一次 Inspect 做实证：拉取成功就必须能查到该镜像。
	// 这一步不是多余请求，它是"流式协议无法自证成功"时的唯一证据来源。
	if lineCount == 0 {
		if _, ierr := e.InspectImage(ctx, ref); ierr != nil {
			return PullResult{}, fmt.Errorf("%w: %s", ErrPullRejected, sanitizeEngineError("empty stream"))
		}
	}

	if onProgress != nil {
		onProgress(PullProgress{BytesDone: sumTot(layers), BytesTotal: sumTot(layers), Status: "done"})
	}

	// ============================ digest 校验（两条来源） ============================
	//
	// 要求来自两处，任一非空就必须校验：
	//  1. 引用本身自带 digest（name@sha256:xxx）——这是隐含的完整性要求；
	//  2. 调用方另外传了 payload.digest。
	//
	// 只校验第 2 条会漏掉最常见的情况：用户直接粘贴带 digest 的引用，
	// 而 payload.digest 为空。那时若拉到了 latest，我们会报告成功。
	res := PullResult{Reference: ref}
	wantDigest := strings.TrimSpace(digest)
	if wantDigest == "" {
		wantDigest = digestInRef(ref)
	}
	if wantDigest != "" {
		detail, ierr := e.InspectImage(ctx, ref)
		if ierr != nil {
			return PullResult{}, ierr
		}
		if !detailHasDigest(detail, wantDigest) {
			return PullResult{}, fmt.Errorf("%w: 拉取到的镜像 digest 与要求的不一致", ErrDigestMismatch)
		}
		res.Digest = wantDigest
	}
	return res, nil
}

// SaveImage 实现 ImageTransferRuntime。
func (e *engine) SaveImage(ctx context.Context, reference string) (io.ReadCloser, error) {
	ref := strings.TrimSpace(reference)
	if ref == "" {
		return nil, errors.New("镜像引用不能为空")
	}
	// 先确认镜像存在：否则调用方会在读流时才拿到 404，
	// 那时已经建立了"保存成功"的任务状态，错误位置会误导排查。
	if _, err := e.InspectImage(ctx, ref); err != nil {
		return nil, err
	}
	path := pathImageGet + url.PathEscape(ref) + "/get"
	body, err := e.client.openStream(ctx, "GET", path)
	if err != nil {
		return nil, e.translate(err)
	}
	return body, nil
}

// LoadImage 实现 ImageTransferRuntime。
func (e *engine) LoadImage(ctx context.Context, body io.Reader, size int64) ([]Image, error) {
	if body == nil {
		return nil, errors.New("归档内容为空")
	}
	if size <= 0 {
		// 没有确切长度就不发：一来无法确认收到的字节数，
		// 二来 chunked 编码在部分引擎版本的 load 上行为不一致。
		return nil, errors.New("必须提供归档的确切字节数")
	}
	resp, err := e.client.openUpload(ctx, pathImageLoad, "application/x-tar", body, size)
	if err != nil {
		return nil, e.translate(err)
	}

	// /images/load 的响应是 NDJSON：每行一个 {"stream":"Loaded image: ..."}。
	//
	// **必须检查错误行**：归档损坏时引擎常常仍返回 200，
	// 然后在流里写 {"errorDetail":{"message":"..."}}。
	// 只看 HTTP 状态会把"什么都没导入"报告成成功。
	if msg := firstStreamError(string(resp)); msg != "" {
		return nil, fmt.Errorf("%w: %s", ErrLoadRejected, sanitizeEngineError(msg))
	}

	loaded := parseLoadedImages(string(resp))
	return loaded, nil
}

// ErrLoadRejected 表示引擎拒绝了这次导入。
var ErrLoadRejected = errors.New("镜像导入被拒绝")

// firstStreamError 从 NDJSON 响应里取出第一条错误说明（两种形态都认）。
//
// 返回空串表示流里没有错误行。这不是"绝对成功"的证据——
// 调用方仍应结合 loaded 列表是否为空来判断，见 LoadImage 的说明。
func firstStreamError(resp string) string {
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var obj imageProgressLine
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}
		if msg := obj.errMessage(); msg != "" {
			return msg
		}
	}
	return ""
}

// InspectImage 实现 ImageTransferRuntime。
func (e *engine) InspectImage(ctx context.Context, reference string) (ImageDetail, error) {
	ref := strings.TrimSpace(reference)
	if ref == "" {
		return ImageDetail{}, errors.New("镜像引用不能为空")
	}
	var raw struct {
		ID       string   `json:"Id"`
		RepoTags []string `json:"RepoTags"`
		Size     int64    `json:"Size"`
		// inspect 返回的 Created 是 RFC3339 字符串，不是整数。
		// 用 flexUnixTime 兼容两种形态（见 parse_time.go）。
		Created     flexUnixTime `json:"Created"`
		Os          string       `json:"Os"`
		Arch        string       `json:"Architecture"`
		Variant     string       `json:"Variant"`
		RepoDigests []string     `json:"RepoDigests"`
	}
	path := pathImageInspect + url.PathEscape(ref) + "/json"
	if err := e.client.getJSON(ctx, path, &raw); err != nil {
		return ImageDetail{}, translateResourceError(err, "镜像")
	}
	platform := raw.Os
	if raw.Arch != "" {
		platform = raw.Os + "/" + raw.Arch
	}
	if raw.Variant != "" {
		platform += "/" + raw.Variant
	}
	return ImageDetail{
		ID:       raw.ID,
		Tags:     raw.RepoTags,
		Size:     raw.Size,
		Created:  raw.Created.unix(),
		Platform: platform,
		Digests:  raw.RepoDigests,
	}, nil
}

// ErrPullRejected 表示 registry / 引擎拒绝了这次拉取。
var ErrPullRejected = errors.New("镜像拉取被拒绝")

// ErrDigestMismatch 表示拉取结果与要求的 digest 不一致。
var ErrDigestMismatch = errors.New("镜像 digest 不匹配")

// splitImageRef 把 ref 拆成 (fromImage, tag)，供 /images/create 使用。
//
// ============================ digest 必须原样保留 ============================
//
// 这里踩过一个真实缺陷：此前把 "name@sha256:xxx" 拆成 (name, "")，
// 结果**digest 被整段丢掉**。Docker 收到没有 tag 的 fromImage 会
// 自动补 ":latest"——于是"按 digest 拉取"实际拉成了最新版，
// 而调用方与用户都以为拿到的是那个确定的 digest。
// 这类错误在测试里看不出来（拉取"成功"了），要等到生产上
// 镜像内容与预期不符、且无法复现时才被发现。
//
// 正确形态（Docker 兼容 API 的约定）：
//
//	nginx:1.25             → fromImage=nginx,             tag=1.25
//	ghcr.io/a/b:latest     → fromImage=ghcr.io/a/b,       tag=latest
//	nginx                  → fromImage=nginx,             tag=（空，引擎补 latest）
//	nginx@sha256:abc       → fromImage=nginx@sha256:abc,  tag=（空）
//
// 即 digest 整体留在 fromImage 里，不拆开、不丢弃。
//
// 冒号只在**最后一段**且不在 digest 里才算 tag 分隔符：
// registry 主机名可能带端口（127.0.0.1:5000/app），
// 不能把端口切成 tag。
func splitImageRef(ref string) (string, string) {
	// 带 digest 的整体保留，不做任何拆分。
	if strings.Contains(ref, "@") {
		return ref, ""
	}
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon > slash {
		return ref[:colon], ref[colon+1:]
	}
	return ref, ""
}

// digestInRef 取出引用里自带的 digest（形如 name@sha256:xxx 的 @ 之后部分）。
//
// 用于"引用自带 digest 时必须校验拉取结果"——不论调用方是否另外传了
// ImagePullPayload.Digest，引用里写了 digest 就是一个隐含的完整性要求。
// 忽略它等于让用户以为拉的是确定版本，实际却可能被 latest 替换。
func digestInRef(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		return ref[i+1:]
	}
	return ""
}

// detailHasDigest 判断详情里是否包含指定 digest（宽松匹配：允许带或不带 sha256: 前缀）。
func detailHasDigest(d ImageDetail, digest string) bool {
	want := strings.TrimSpace(digest)
	if want == "" {
		return true
	}
	wantBare := strings.TrimPrefix(want, "sha256:")
	for _, d := range d.Digests {
		got := d
		if i := strings.LastIndex(got, "@"); i >= 0 {
			got = got[i+1:]
		}
		if got == want || strings.TrimPrefix(got, "sha256:") == wantBare {
			return true
		}
	}
	return false
}

// sumCur / sumTot 汇总各层进度。
func sumCur(layers map[string]layerProgress) int64 {
	var n int64
	for _, lp := range layers {
		n += lp.cur
	}
	return n
}

func sumTot(layers map[string]layerProgress) int64 {
	var n int64
	for _, lp := range layers {
		n += lp.tot
	}
	return n
}

// normalizePullStatus 把引擎的状态文本归一成稳定的阶段词。
//
// 为什么要归一：引擎文案随版本变化（"Pulling fs layer" / "Pulling"），
// 直接透传会让前端的阶段判断时好时坏；而且原文可能含镜像全名与 registry 地址。
// 这里只映射到有限的几个词，前端可以稳定依赖。
func normalizePullStatus(status string) string {
	s := strings.ToLower(status)
	switch {
	case strings.Contains(s, "extract"):
		return "extracting"
	case strings.Contains(s, "download"):
		return "downloading"
	case strings.Contains(s, "pull"):
		return "pulling"
	case strings.Contains(s, "digest"), strings.Contains(s, "status"):
		return "verifying"
	case strings.Contains(s, "complete"), strings.Contains(s, "done"):
		return "done"
	}
	return "pulling"
}

// sanitizeEngineError 把引擎错误压成一句**不含敏感信息**的摘要。
//
// 为什么不直接透传：registry 的报错常包含账号名、registry 内网地址、
// 甚至 token 片段。这些一旦进入 Hub 的响应体、审计记录与浏览器，
// 就等于把部署细节永久留存下来。
//
// 做法：只保留"是否是认证/不存在/网络"这三类可操作信息，其余一律概括。
// 用户在 Agent 本机执行命令仍能看到完整原文，排查能力没有损失。
func sanitizeEngineError(raw string) string {
	s := strings.ToLower(raw)
	switch {
	case strings.Contains(s, "unauthorized"), strings.Contains(s, "authentication"),
		strings.Contains(s, "denied"), strings.Contains(s, "forbidden"):
		return "仓库要求认证（首批仅支持公共仓库）"
	case strings.Contains(s, "not found"), strings.Contains(s, "manifest unknown"),
		strings.Contains(s, "no such"):
		return "仓库或镜像不存在"
	case strings.Contains(s, "timeout"), strings.Contains(s, "connection"),
		strings.Contains(s, "dial"), strings.Contains(s, "no such host"):
		return "网络无法连接镜像仓库"
	case strings.Contains(s, "no space"), strings.Contains(s, "disk"):
		return "本机磁盘空间不足"
	}
	return "镜像仓库返回错误"
}

// parseLoadedImages 从 /images/load 的 NDJSON 响应里提取加载的镜像。
//
// 响应形如：{"stream":"Loaded image: nginx:1.25\n"}
// 解析不出就返回空切片：**不编造镜像名**，
// 让上层如实报告"已导入但未取得镜像名"。
func parseLoadedImages(resp string) []Image {
	out := make([]Image, 0, 4)
	seen := make(map[string]bool)
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var obj struct {
			Stream string `json:"stream"`
		}
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}
		const marker = "Loaded image:"
		idx := strings.Index(obj.Stream, marker)
		if idx < 0 {
			continue
		}
		name := strings.TrimSpace(strings.SplitN(obj.Stream[idx+len(marker):], "\n", 2)[0])
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, Image{Tags: []string{name}})
	}
	return out
}
