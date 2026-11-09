package link

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// 认证协议约定（与 Hub 侧实现对齐，变更需两端同步）：
//
//	Authorization: Bearer <agent token>
//	X-Arkade-Agent-Id: <agent_id>      // 首连时可能为空，由 HelloAck 下发
//	X-Arkade-Proto: <协议版本>
//	User-Agent: arkade-agent/<版本>
//
// 为什么认证信息放请求头而不是 URL 查询串：查询串会被各级代理与访问日志原样记录，
// 令牌泄漏在日志里比泄漏在网络里更难察觉。Hub 的首期实现若只支持查询串，
// 这里会同时带上（见 query 里的 token 参数），保证兼容期两边都能跑通。
const (
	headerAuth      = "Authorization"
	headerAgentID   = "X-Arkade-Agent-Id"
	headerProto     = "X-Arkade-Proto"
	headerUserAgent = "User-Agent"
	// headerHostSecurity 是握手时的额外防护：服务器端本不应校验 Origin
	// （Agent 不是浏览器），但某些反向代理会因为没有 Origin 而拒绝，
	// 这里显式把 Origin 设为空字符串（gorilla 默认不发该头）。
	authScheme = "Bearer "
)

// defaultConnectPath 是 Hub 侧的 Agent 接入路径。
const defaultConnectPath = "/agent/connect"

// dialer 封装 WebSocket 握手参数。
//
// 超时控制：HandshakeTimeout 必须有值，否则在链路黑洞（丢包不回 RST）时
// 建连会卡到 TCP 自身超时（分钟级），导致退避节奏完全失效。
type dialer struct {
	u            *url.URL
	token        string
	agentID      string
	proto        int
	agentVersion string
	insecureTLS  bool
	ws           *websocket.Dialer
}

// newDialer 归一化 Hub 地址并构造拨号器。
//
// 地址归一化规则：
//   - http:// → ws://，https:// → wss://（用户常把面板地址直接粘过来）；
//   - 未带路径或路径为 "/" → 补 /agent/connect；
//   - 已有路径（如自定义网关前缀 /arkade/agent/connect）→ 原样保留。
func newDialer(hubURL, token, agentID, agentVersion string, protoVer int, insecureTLS bool) (*dialer, error) {
	raw := strings.TrimSpace(hubURL)
	if raw == "" {
		return nil, fmt.Errorf("hub 地址为空")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("hub 地址格式非法(%s): %w", raw, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "ws", "wss":
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return nil, fmt.Errorf("hub 地址协议不支持: %q（只支持 ws:// / wss:// / http:// / https://）", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("hub 地址缺少主机名: %s", raw)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = defaultConnectPath
	}

	ws := &websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		// 心跳周期远小于 60s，允许 60s 的读超时作为"最后一道防线"：
		// 正常情况下由应用层心跳驱动，这里的超时只兜住"TCP 假连通"的情况。
		EnableCompression: false,
	}
	if insecureTLS {
		// 仅用于自签名证书的测试环境；生产必须让系统信任链生效。
		ws.TLSClientConfig = insecureTLSConfig()
	}

	return &dialer{
		u:            u,
		token:        token,
		agentID:      agentID,
		proto:        protoVer,
		agentVersion: agentVersion,
		insecureTLS:  insecureTLS,
		ws:           ws,
	}, nil
}

// headers 构造握手请求头。
func (d *dialer) headers() http.Header {
	h := http.Header{}
	if d.token != "" {
		h.Set(headerAuth, authScheme+d.token)
	}
	if d.agentID != "" {
		h.Set(headerAgentID, d.agentID)
	}
	h.Set(headerProto, itoa(d.proto))
	if d.agentVersion != "" {
		h.Set(headerUserAgent, "arkade-agent/"+d.agentVersion)
	}
	return h
}

// connectURL 返回最终连接地址（含兼容期的 token 查询参数）。
func (d *dialer) connectURL() string {
	u := *d.u
	if d.token != "" {
		// 兼容期同时提供查询串参数：老版本 Hub 可能只读 ?token=。
		// 一旦 Hub 侧确认只读请求头，这一行即可删除（已在 docs 对齐项中登记）。
		q := u.Query()
		if q.Get("token") == "" {
			q.Set("token", d.token)
		}
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// dial 执行一次握手。
func (d *dialer) dial() (*websocket.Conn, *http.Response, error) {
	return d.ws.Dial(d.connectURL(), d.headers())
}

// setAgentID 更新握手时要带的身份。
//
// 握手成功后 Hub 会下发正式 agent_id，重连时必须带着它，否则 Hub 会误判
// 成一台新机器。dialer 只在单 goroutine（连接循环）里被读写，故不加锁。
func (d *dialer) setAgentID(id string) {
	d.agentID = id
}
