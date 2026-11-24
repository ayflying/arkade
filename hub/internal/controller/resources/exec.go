package resources

// exec.go：在容器内执行一条命令（POST /containers/{id}/exec）。
//
// ============================ 为什么需要 manage ============================
//
// 执行命令能读到容器内的所有文件、能改动数据、能杀掉进程。
// 这比"停止容器"（operate）强得多——停止至少不改变数据。
// 因此按 manage 要求，与删除/重建同一档，而不是留给 operate。
//
// ============================ 命令正文不进日志、不进审计 ============================
//
// 命令里几乎必然带敏感信息：`mysql -p'***'`、`curl -H 'Authorization: ...'`、
// 内网地址。所以：
//   - 审计只记"谁在什么时候对哪个容器执行过命令"（见 audit_routes.go）；
//   - 本控制器也不打命令正文的日志。
//
// ============================ 入参校验是第二道防线 ============================
//
// Agent 侧已经有同样的校验。这里再做一次不是重复劳动：
// Hub 是面向浏览器的边界，非法请求应该在到达 Agent 之前被拒，
// 否则一次无效输入也要占掉一条控制通道的请求槽位。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	api "arkade/hub/api/resources/v1"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/httpwire"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
	"github.com/gogf/gf/v2/net/ghttp"
)

const (
	// 请求体上限：命令向量的总长度受这些上限约束后必然远小于此值。
	maxExecBodyBytes = 64 << 10
	maxExecArgs      = 64
	maxExecArgBytes  = 8 << 10
	maxExecUserBytes = 128
	maxExecDirBytes  = 4096
	// 与 Agent 侧一致的超时区间。
	defaultExecTimeoutSec = 30
	maxExecTimeoutSec     = 120
)

// containerExecInput 是请求体形状。
//
// 刻意**不含** container_id：容器身份只认 URL 路径。
// 若允许请求体再带一个 container_id，两者不一致时就必须猜哪个为准，
// 而这种歧义一旦被利用就是越权（用 A 的权限对 B 执行命令）。
type containerExecInput struct {
	Command    []string `json:"command"`
	User       string   `json:"user"`
	WorkDir    string   `json:"workdir"`
	TimeoutSec int      `json:"timeout_sec"`
}

// ContainerExec 在容器内执行一条命令。
func (c *Controller) ContainerExec(ctx context.Context, req *api.ContainerExecReq) (*api.ContainerExecRes, error) {
	r := httpwire.RestoreRawBody(ctx)
	gf := ghttp.RequestFromCtx(ctx)
	if gf != nil {
		gf.Response.Header().Set("Cache-Control", "no-store")
	}
	if err := c.access.Require(ctx, req.AgentID, user.PermManage); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.ContainerID) == "" || len(req.ContainerID) > 256 {
		return nil, controller.Failure("bad_request", "容器标识无效")
	}
	if r == nil || gf == nil {
		return nil, controller.Failure("bad_request", "请求无效或超过大小限制")
	}
	r.Body = http.MaxBytesReader(gf.Response.Writer, r.Body, maxExecBodyBytes)
	defer r.Body.Close()
	// 读请求体也要有界：浏览器发到一半就断的情况不该永远挂住一个请求槽位。
	rc := http.NewResponseController(gf.Response.Writer.ResponseWriter)
	_ = rc.SetReadDeadline(time.Now().Add(30 * time.Second))
	defer rc.SetReadDeadline(time.Time{})

	var input containerExecInput
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		return nil, controller.Failure("bad_request", "请求格式错误：需要 command 数组")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, controller.Failure("bad_request", "请求体只能包含一个 JSON 对象")
	}
	if err := validateExecInput(&input); err != nil {
		return nil, err
	}

	out, cerr := c.deps.Ops.ExecContainer(ctx, req.AgentID, envelope.ContainerExecPayload{
		ContainerID: req.ContainerID,
		Command:     input.Command,
		User:        input.User,
		WorkDir:     input.WorkDir,
		TimeoutSec:  input.TimeoutSec,
	})
	if cerr != nil {
		return nil, controller.ConnectionError(cerr)
	}
	return &api.ContainerExecRes{Result: out}, nil
}

// validateExecInput 校验并规范化请求体（就地填默认超时）。
func validateExecInput(in *containerExecInput) error {
	if len(in.Command) == 0 {
		return controller.Failure("bad_request", "command 不能为空")
	}
	if len(in.Command) > maxExecArgs {
		return controller.Failure("bad_request", "命令参数过多（最多 64 个）")
	}
	for _, arg := range in.Command {
		if len(arg) > maxExecArgBytes {
			return controller.Failure("bad_request", "单个命令参数过大")
		}
		if strings.ContainsRune(arg, 0) {
			return controller.Failure("bad_request", "命令参数不能包含 NUL 字符")
		}
	}
	if strings.TrimSpace(in.Command[0]) == "" {
		return controller.Failure("bad_request", "命令的第一个元素必须是可执行文件路径")
	}
	if len(in.User) > maxExecUserBytes || hasExecControl(in.User) {
		return controller.Failure("bad_request", "执行用户无效")
	}
	if len(in.WorkDir) > maxExecDirBytes || hasExecControl(in.WorkDir) {
		return controller.Failure("bad_request", "工作目录无效")
	}
	if in.WorkDir != "" && !strings.HasPrefix(in.WorkDir, "/") {
		return controller.Failure("bad_request", "工作目录必须是绝对路径")
	}
	if in.TimeoutSec <= 0 {
		in.TimeoutSec = defaultExecTimeoutSec
	}
	if in.TimeoutSec > maxExecTimeoutSec {
		in.TimeoutSec = maxExecTimeoutSec
	}
	return nil
}

// hasExecControl 判断是否含控制字符。
func hasExecControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
