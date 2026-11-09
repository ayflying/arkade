package httpx

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
)

const maxVolumeFileSize = 4 << 20

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
func validVolumeRequest(p envelope.VolumeFilesPayload) bool {
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

func handleVolumeFiles(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		agentID := agentIDFromPath(r)
		// 卷内容可能包含配置秘密，即使只读也必须manage，不沿用卷列表view权限。
		if !requirePermission(w, d, r, agentID, user.PermManage) {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, int64(base64.StdEncoding.EncodedLen(maxVolumeFileSize)+16384))
		defer r.Body.Close()
		// 对慢速上传设置读期限，避免body解码永久占用连接。
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Now().Add(60 * time.Second))
		defer rc.SetReadDeadline(time.Time{})
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		stop := context.AfterFunc(ctx, func() { _ = rc.SetReadDeadline(time.Now()); _ = r.Body.Close() })
		defer stop()
		r = r.WithContext(ctx)
		var p envelope.VolumeFilesPayload
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			badRequest(w, d, "文件操作请求无效或超过4MiB限制")
			return
		}
		var extra any
		if dec.Decode(&extra) != io.EOF || !validVolumeRequest(p) {
			badRequest(w, d, "文件路径、操作或内容无效；覆盖已有文件须提供原始校验值")
			return
		}
		out, err := d.Ops.VolumeFiles(r.Context(), agentID, p)
		if err != nil {
			respondConnError(w, d, err)
			return
		}
		respondJSON(w, d.Log, http.StatusOK, map[string]any{"result": out})
	}
}
