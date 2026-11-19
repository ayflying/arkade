package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/gogf/gf/v2/errors/gerror"
)

// Tool describes one allowlisted Hub operation, not a caller-supplied HTTP request.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Method      string
	Path        string
	Query       []string
	FixedBody   map[string]any
	ReadOnly    bool
	Destructive bool
}

// request consumes only catalog-defined path/query fields; callers cannot override
// transport metadata, fixed operation selectors, or the authenticated identity.
func (t Tool) request(ctx context.Context, values map[string]any) (*http.Request, error) {
	parts := strings.Split(t.Path, "/")
	for i, part := range parts {
		if !strings.HasPrefix(part, "{") || !strings.HasSuffix(part, "}") {
			continue
		}
		key := part[1 : len(part)-1]
		v, ok := values[key].(string)
		if !ok || !safeSegment(v) {
			return nil, gerror.Newf("路径参数 %s 无效", key)
		}
		delete(values, key)
		parts[i] = url.PathEscape(v)
	}
	path := strings.Join(parts, "/")
	q := url.Values{}
	for _, key := range t.Query {
		if v, exists := values[key]; exists {
			if s, ok := v.(string); ok {
				q.Set(key, s)
			} else {
				b, err := json.Marshal(v)
				if err != nil {
					return nil, err
				}
				q.Set(key, string(b))
			}
			delete(values, key)
		}
	}
	if len(q) != 0 {
		path += "?" + q.Encode()
	}
	if t.Method == http.MethodGet || t.Method == http.MethodDelete {
		return http.NewRequestWithContext(ctx, t.Method, path, nil)
	}
	for k, v := range t.FixedBody {
		values[k] = v
	}
	body, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	r, err := http.NewRequestWithContext(ctx, t.Method, path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	return r, nil
}

// GF and the router may decode a path before matching it. Do not allow a
// placeholder to become a delimiter, traversal segment, or encoded delimiter.
func safeSegment(s string) bool {
	return s != "" && s != "." && s != ".." && len(s) <= 512 &&
		!strings.ContainsAny(s, "/\\%?#") && strings.IndexFunc(s, func(r rune) bool { return r < 32 || r == 127 }) < 0
}
