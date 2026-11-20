package agentlink

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"arkade/protocol/envelope"
)

// DiscoveryHandler advertises the external path selected by Hub configuration,
// never by the request Host or forwarded headers. The proxy keeps the fixed
// discovery route at the origin root and maps the advertised path to Connect.
func (s *Server) DiscoveryHandler(prefix string) (http.Handler, error) {
	path, err := envelope.AgentConnectPathForPrefix(prefix)
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if _, ok := s.authorizeTransport(w, r); !ok {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(envelope.AgentDiscovery{ConnectPath: path})
	}), nil
}

// SetAccessCheck supplies the Hub's enrollment state check before serving traffic.
// The final hello path still checks under the enrollment guard.
func (s *Server) SetAccessCheck(check func(context.Context, string) error) {
	s.checkAccess = check
}

// Both discovery and the upgrade authenticate independently. Discovery grants
// no ticket or exemption from the upgrade and final enrollment checks.
func (s *Server) authorizeTransport(w http.ResponseWriter, r *http.Request) (authResult, bool) {
	auth, err := s.authorize(r)
	if err != nil {
		s.log.Warn("Agent 接入认证失败", "remote", r.RemoteAddr, "err", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return authResult{}, false
	}
	if id := auth.boundAgentID; id != "" && s.checkAccess != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := s.checkAccess(ctx, id); err != nil {
			s.log.Warn("拒绝 Agent 接入", "agent_id", id, "err", err)
			http.Error(w, "forbidden", http.StatusForbidden)
			return authResult{}, false
		}
	}
	if v := r.Header.Get(envelope.HeaderProtoVersion); v != "" && v != strconv.Itoa(envelope.Version) {
		http.Error(w, "unsupported protocol version", http.StatusUpgradeRequired)
		return authResult{}, false
	}
	return auth, true
}
