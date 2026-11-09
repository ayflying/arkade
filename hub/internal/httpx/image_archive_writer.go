package httpx

import (
	"net/http"
	"strconv"
)

// 首块到达才承诺tar响应：重复下载或过期等准入失败仍可返回明确JSON错误。
// 已发送后不能混入JSON，Content-Length使浏览器能识别中途截断。
type imageArchiveWriter struct {
	w       http.ResponseWriter
	size    int64
	started bool
}

func (s *imageArchiveWriter) Write(b []byte) (int, error) {
	if !s.started {
		s.w.Header().Set("Cache-Control", "no-store")
		s.w.Header().Set("Content-Type", "application/x-tar")
		s.w.Header().Set("Content-Disposition", `attachment; filename="arkade-image.tar"`)
		if s.size > 0 {
			s.w.Header().Set("Content-Length", strconv.FormatInt(s.size, 10))
		}
		s.started = true
	}
	return s.w.Write(b)
}
