package runtime

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// client 是对 Docker 兼容 HTTP API 的最小封装。
//
// 为什么不引入 docker/docker SDK：
//   - 项目只用到 /version、/containers 少量端点，SDK 会带入大量与本项目无关的依赖；
//   - SDK 的错误模型、类型转换规则不受本项目控制，升级引擎时容易被动；
//   - 手写客户端只有几十行，端点集中在 api.go，排障时链路透明。
//
// 端点形态统一为 "unix:///var/run/docker.sock"、"tcp://127.0.0.1:2375" 或
// "http://host:port"：前两者分别对应 Linux 本机套接字与 Windows/macOS 开发时的
// TCP 端点。unix 端点通过自定义 DialContext 实现——这是标准库支持网络类型 unix
// 的官方做法，不需要任何第三方库。
type client struct {
	// scheme 恒为 "http"：unix socket 上是明文 HTTP，TCP 端点也按明文处理
	// （远程明文端点仅用于本机开发，生产上远程访问必须由运维放在 SSH 隧道后）。
	scheme  string
	host    string // 请求 Host 头用的逻辑主机名
	network string // "unix" 或 "tcp"
	address string // unix 时为套接字路径，tcp 时为 host:port
	httpc   *http.Client
}

// newClient 按端点字符串构造客户端。端点格式非法时返回可读错误。
func newClient(endpoint string) (*client, error) {
	ep := strings.TrimSpace(endpoint)
	if ep == "" {
		return nil, errors.New("运行时端点为空")
	}
	// 允许只写裸路径（/var/run/docker.sock），方便配置里少写前缀。
	if strings.HasPrefix(ep, "/") || strings.HasPrefix(ep, `\`) {
		ep = "unix://" + ep
	}
	u, err := url.Parse(ep)
	if err != nil {
		return nil, fmt.Errorf("运行时端点格式错误(%s): %w", ep, err)
	}

	c := &client{scheme: "http"}
	transport := &http.Transport{
		// 运行时是本机进程，连接数很少；禁用长连接复用可能反而增加握手开销，
		// 这里保留默认复用，但设置较短的闲置回收，避免引擎重启后拿到死连接。
		IdleConnTimeout:     30 * time.Second,
		MaxIdleConnsPerHost: 4,
	}

	switch strings.ToLower(u.Scheme) {
	case "unix":
		if u.Path == "" {
			return nil, fmt.Errorf("unix 端点缺少套接字路径: %s", ep)
		}
		c.network = "unix"
		c.address = u.Path
		c.host = "docker" // 走套接字时 Host 头无实际意义，固定值便于日志识别
		transport.DialContext = (&net.Dialer{Timeout: 5 * time.Second}).DialContext
	case "tcp", "http":
		if u.Host == "" {
			return nil, fmt.Errorf("tcp/http 端点缺少主机地址: %s", ep)
		}
		c.network = "tcp"
		c.address = u.Host
		c.host = u.Host
		// tcp:// 前缀不是 HTTP 认识的协议，统一归一成 http。
		if strings.EqualFold(u.Scheme, "http") {
			c.scheme = u.Scheme
		}
		if u.Scheme == "https" {
			c.scheme = "https"
			transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		transport.DialContext = (&net.Dialer{Timeout: 5 * time.Second}).DialContext
	default:
		return nil, fmt.Errorf("不支持的端点协议 %q（支持 unix / tcp / http）: %s", u.Scheme, ep)
	}

	c.httpc = &http.Client{
		Transport: transport,
		// 不设全局超时：单次调用由 context 控制，日志这类大响应需要更长窗口。
	}
	return c, nil
}

// url 拼接出完整请求地址。
func (c *client) url(path string) string {
	return c.scheme + "://" + c.host + path
}

// do 发送请求并读取完整响应体，把非 2xx 统一包装成带状态码的错误。
//
// 这是所有端点调用的唯一出口：认证、错误翻译、响应读取都只在这里做一次，
// 避免每个方法各写一遍（复制实现是这类客户端最容易出问题的地方）。
func (c *client) do(ctx context.Context, method, path string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.url(path), body)
	if err != nil {
		return nil, fmt.Errorf("构造运行时请求失败: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		// 底层连接错误（套接字不存在、权限不足、引擎未启动）在这里暴露，
		// 上层据此区分「环境不可用」与「操作失败」。
		return nil, fmt.Errorf("连接运行时失败(%s %s): %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// 日志端点可能返回较大响应，但控制面只取尾部有限行，这里限制读取上限，
	// 防止恶意/异常容器在日志里刷出 GB 级内容撑爆内存。
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("读取运行时响应失败(%s): %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return data, &statusError{Status: resp.StatusCode, Body: strings.TrimSpace(string(data)), Path: path}
	}
	return data, nil
}

// maxResponseBytes 是单次响应体上限（16 MiB），远超正常控制面使用量。
const maxResponseBytes = 16 << 20

// statusError 承载运行时的非 2xx 响应。
type statusError struct {
	Status int
	Body   string
	Path   string
}

func (e *statusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("运行时返回 HTTP %d (%s)", e.Status, e.Path)
	}
	return fmt.Sprintf("运行时返回 HTTP %d (%s): %s", e.Status, e.Path, e.Body)
}

// getJSON 发送 GET 并把 JSON 响应解码到 out。
func (c *client) getJSON(ctx context.Context, path string, out any) error {
	data, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("解析运行时响应失败(%s): %w", path, err)
	}
	return nil
}

// post 发送无载荷 POST（启停等操作端点不需要请求体）。
func (c *client) post(ctx context.Context, path string) error {
	_, err := c.do(ctx, http.MethodPost, path, nil)
	return err
}

// delete 发送 DELETE。
func (c *client) delete(ctx context.Context, path string) error {
	_, err := c.do(ctx, http.MethodDelete, path, nil)
	return err
}

// queryInt 把整数参数拼进查询串。
func queryInt(base string, key string, v int) string {
	return base + key + "=" + strconv.Itoa(v)
}

// splitLogLines 把日志文本拆成行，去掉空行尾噪声。
//
// 运行时可能返回 CRLF（Windows 容器）或空行，统一规整；同时避免返回
// 「末尾一个空字符串」这种前端看起来像多一行空白的噪声。
func splitLogLines(raw string) []string {
	if raw == "" {
		return nil
	}
	sc := bufio.NewScanner(strings.NewReader(raw))
	// 单行上限放大到 1 MiB：异常容器可能一次打出超长行，默认 64KiB 会读失败。
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	lines := make([]string, 0, 64)
	for sc.Scan() {
		lines = append(lines, strings.TrimSuffix(sc.Text(), "\r"))
	}
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// trimTail 截取最后 n 行；返回结果与是否被截断。
func trimTail(lines []string, n int) ([]string, bool) {
	if n <= 0 || len(lines) <= n {
		return lines, false
	}
	return lines[len(lines)-n:], true
}

// endpointExists 判断 unix 套接字文件是否存在（tcp 端点无法预检，一律返回 true）。
//
// 注意：这里用 os.Stat 而不是尝试连接——Podman rootless 的套接字在服务未启动时
// 文件可能残留，因此"存在"只是弱信号，真正的判定仍然靠后续 ping/version 探测。
func endpointExists(endpoint string) bool {
	ep := strings.TrimSpace(endpoint)
	if !strings.HasPrefix(ep, "unix://") {
		if strings.HasPrefix(ep, "/") {
			_, err := os.Stat(ep)
			return err == nil
		}
		return true
	}
	_, err := os.Stat(strings.TrimPrefix(ep, "unix://"))
	return err == nil
}
