package resources

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	api "arkade/hub/api/resources/v1"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/httpwire"
	"arkade/hub/internal/user"
	"github.com/gogf/gf/v2/net/ghttp"
)

// MaxVolumeFileSize is the maximum decoded file content size.
const MaxVolumeFileSize = 4 << 20

const maxVolumeFileSize = MaxVolumeFileSize

// ValidVolumeRequest exposes the native validation contract without duplicating it.
func ValidVolumeRequest(p api.VolumeFilesInput) bool { return validVolumeRequest(p) }

func ValidVolumePath(p string, root bool) bool { return validVolumePath(p, root) }

func validVolumePath(p string, root bool) bool {
	if len(p) > 4096 || strings.ContainsAny(p, "\\\x00\r\n") || strings.HasPrefix(p, "/") {
		return false
	}
	if p == "" || p == "." {
		return root
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
func validVolumeName(s string) bool {
	if len(s) == 0 || len(s) > 255 {
		return false
	}
	for i, c := range []byte(s) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		if i > 0 && (c == '_' || c == '-' || c == '.') {
			continue
		}
		return false
	}
	return true
}
func validVolumeRequest(p api.VolumeFilesInput) bool {
	if !validVolumeName(p.Volume) || !validVolumePath(p.Path, p.Operation == "list") {
		return false
	}
	if p.Operation != "write" && (p.DataBase64 != "" || p.CreateOnly || p.ExpectedSHA256 != "") {
		return false
	}
	if p.Operation != "rename" && p.Destination != "" {
		return false
	}
	switch p.Operation {
	case "list", "read", "mkdir", "delete":
		return true
	case "rename":
		return validVolumePath(p.Destination, false) && p.Path != p.Destination
	case "write":
		if len(p.DataBase64) > base64.StdEncoding.EncodedLen(maxVolumeFileSize) {
			return false
		}
		b, err := base64.StdEncoding.Strict().DecodeString(p.DataBase64)
		if err != nil || len(b) > maxVolumeFileSize {
			return false
		}
		if p.CreateOnly {
			return p.ExpectedSHA256 == ""
		}
		hash, err := hex.DecodeString(p.ExpectedSHA256)
		return err == nil && len(hash) == 32
	}
	return false
}

func (c *Controller) VolumeFiles(ctx context.Context, req *api.VolumeFilesReq) (*api.VolumeFilesRes, error) {
	r := httpwire.RestoreRawBody(ctx)
	gf := ghttp.RequestFromCtx(ctx)
	if gf != nil {
		gf.Response.Header().Set("Cache-Control", "no-store")
	}
	if err := c.access.Require(ctx, req.AgentID, user.PermManage); err != nil {
		return nil, err
	}
	if r == nil || gf == nil {
		return nil, controller.Failure("bad_request", "文件操作请求无效或超过4MiB限制")
	}
	r.Body = http.MaxBytesReader(gf.Response.Writer, r.Body, int64(base64.StdEncoding.EncodedLen(maxVolumeFileSize)+16384))
	defer r.Body.Close()
	// Start below GF's writer: it does not expose Unwrap, while the native
	// audit/logger wrappers do, preserving the transport read deadline.
	rc := http.NewResponseController(gf.Response.Writer.ResponseWriter)
	_ = rc.SetReadDeadline(time.Now().Add(60 * time.Second))
	defer rc.SetReadDeadline(time.Time{})
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = rc.SetReadDeadline(time.Now()); _ = r.Body.Close() })
	defer stop()
	var p api.VolumeFilesInput
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, controller.Failure("bad_request", "文件操作请求无效或超过4MiB限制")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF || !validVolumeRequest(p) {
		return nil, controller.Failure("bad_request", "文件路径、操作或内容无效；覆盖已有文件须提供原始校验值")
	}
	out, err := c.deps.Ops.VolumeFiles(ctx, req.AgentID, p)
	if err != nil {
		return nil, controller.ConnectionError(err)
	}
	return &api.VolumeFilesRes{Result: out}, nil
}
