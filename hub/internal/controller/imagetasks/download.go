package imagetasks

import (
	"context"
	"net/http"
	"strconv"
	"time"

	api "arkade/hub/api/imagetasks/v1"
	"arkade/hub/internal/controller"
	"github.com/gogf/gf/v2/net/ghttp"
)

// archiveWriter commits only on the first chunk: admission errors remain JSON.
// It writes through the native GF transport, never its buffered JSON response.
type archiveWriter struct {
	w       http.ResponseWriter
	size    int64
	started bool
}

func (s *archiveWriter) Write(b []byte) (int, error) {
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

func (c *Controller) Download(ctx context.Context, req *api.DownloadReq) (*api.DownloadRes, error) {
	t, err := c.task(ctx, req.TaskID)
	if err != nil {
		return nil, err
	}
	if !t.DownloadReady {
		return nil, badRequest("导出未完成或下载已过期")
	}
	gf := ghttp.RequestFromCtx(ctx)
	if gf == nil {
		return nil, controller.ErrInternal
	}
	writer := gf.Response.RawWriter()
	stream := &archiveWriter{w: writer, size: t.ArchiveSize}
	rc := http.NewResponseController(gf.Response.Writer.ResponseWriter)
	_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Minute))
	defer rc.SetWriteDeadline(time.Time{})
	stop := context.AfterFunc(ctx, func() { _ = rc.SetWriteDeadline(time.Now()) })
	defer stop()
	if err := c.deps.Tasks.Download(ctx, t.ID, stream); err != nil {
		if !stream.started {
			return nil, badRequest("归档暂不可下载、已有下载进行中或已过期")
		}
		c.deps.Log.Warn("镜像归档下载未完成", "task", t.ID)
	}
	return nil, controller.ErrResponseCommitted
}
