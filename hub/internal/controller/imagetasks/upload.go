package imagetasks

import (
	"context"
	"net/http"
	"strconv"
	"time"

	api "arkade/hub/api/imagetasks/v1"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/httpwire"
	"arkade/hub/internal/imagetask"
	"arkade/hub/internal/user"
	"github.com/gogf/gf/v2/net/ghttp"
)

func archiveSize(r *http.Request) (int64, error) {
	size := r.ContentLength
	if header := r.Header.Get("X-Archive-Size"); header != "" {
		declared, err := strconv.ParseInt(header, 10, 64)
		if err != nil || declared <= 0 || (size > 0 && declared != size) {
			return 0, badRequest("归档大小声明与请求长度不符")
		}
		if size <= 0 {
			size = declared
		}
	}
	if size <= 0 {
		return 0, failure(http.StatusLengthRequired, "length_required", "请提供归档大小")
	}
	if size > imagetask.MaxArchiveSize {
		return 0, badRequest("归档必须提供确切大小，且在1字节到2GiB之间")
	}
	return size, nil
}

func (c *Controller) Import(ctx context.Context, req *api.ImportReq) (*api.ImportRes, error) {
	if err := c.access.Require(ctx, req.AgentID, user.PermManage); err != nil {
		return nil, err
	}
	r := httpwire.RestoreRawBody(ctx)
	gf := ghttp.RequestFromCtx(ctx)
	if r == nil || gf == nil {
		return nil, controller.ErrInternal
	}
	size, err := archiveSize(r)
	if err != nil {
		// Preserve legacy 411's empty response, not a synthetic JSON business error.
		if size == 0 {
			if ce, ok := err.(*controller.Error); ok && ce.Status == http.StatusLengthRequired {
				gf.Response.Writer.WriteHeader(http.StatusLengthRequired)
				// GF Flush injects StatusText for any non-200 empty buffer even
				// after the header was sent. The transport has already committed
				// 411; mark only its pending buffer as OK to keep the body empty.
				gf.Response.Status = http.StatusOK
				return nil, controller.ErrResponseCommitted
			}
		}
		return nil, err
	}
	body := http.MaxBytesReader(gf.Response.RawWriter(), r.Body, size+1)
	defer body.Close()
	transfer, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	rc := http.NewResponseController(gf.Response.Writer.ResponseWriter)
	_ = rc.SetReadDeadline(time.Now().Add(30 * time.Minute))
	defer rc.SetReadDeadline(time.Time{})
	stop := context.AfterFunc(transfer, func() { _ = rc.SetReadDeadline(time.Now()); _ = body.Close() })
	defer stop()
	u, err := c.current(ctx)
	if err != nil {
		return nil, err
	}
	t, err := c.deps.Tasks.Upload(transfer, req.AgentID, u.ID, body, size)
	if err != nil {
		return nil, badRequest("上传失败：请检查文件大小、服务器连接或传输配额")
	}
	accepted(ctx)
	return &api.ImportRes{Task: api.View(t)}, nil
}
