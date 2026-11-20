package link

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"arkade/protocol/envelope"
	"github.com/gorilla/websocket"
)

const (
	headerAuth      = envelope.HeaderAuthorization
	headerAgentID   = envelope.HeaderAgentID
	headerProto     = envelope.HeaderProtoVersion
	headerUserAgent = "User-Agent"
	connectTimeout  = 10 * time.Second
)

// dialer owns HTTP discovery and the WebSocket handshake on the same origin.
type dialer struct {
	origin       *url.URL
	token        string
	agentID      string
	proto        int
	agentVersion string
	http         *http.Client
	ws           *websocket.Dialer
}

func newDialer(hubURL, token, agentID, agentVersion string, protoVer int, insecureTLS bool) (*dialer, error) {
	origin, err := envelope.ParseAgentHubOrigin(hubURL)
	if err != nil {
		return nil, err
	}
	ws := &websocket.Dialer{HandshakeTimeout: connectTimeout}
	transport := &http.Transport{DisableKeepAlives: true}
	if insecureTLS {
		// 仅用于开发自签名证书；发现和升级必须使用相同的 TLS 策略。
		ws.TLSClientConfig = insecureTLSConfig()
		transport.TLSClientConfig = insecureTLSConfig()
	}
	return &dialer{
		origin: origin, token: token, agentID: agentID, proto: protoVer, agentVersion: agentVersion,
		ws: ws,
		http: &http.Client{
			Timeout: connectTimeout, Transport: transport,
			// 不接受重定向，避免将凭据发送到其他主机或路径。
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (d *dialer) headers() http.Header {
	h := http.Header{}
	if d.token != "" {
		h.Set(headerAuth, envelope.BearerPrefix+d.token)
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

// dial discovers the current path on every reconnect; no old path fallback.
func (d *dialer) dial(ctx context.Context) (*websocket.Conn, *http.Response, error) {
	path, err := d.discover(ctx)
	if err != nil {
		return nil, nil, err
	}
	u := *d.origin
	u.Path = path
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	return d.ws.DialContext(ctx, u.String(), d.headers())
}

// setAgentID uses the Hub-assigned identity for reconnects. Only the connection
// loop reads and writes the dialer, so no locking is needed.
func (d *dialer) setAgentID(id string) { d.agentID = id }
