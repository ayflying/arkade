package migrate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

// Open 确认归档仍可用。
func (e *VolumeExporter) Open(ctx context.Context, exportID string) (VolumeExportInfo, error) {
	a, err := e.lookup(exportID)
	if err != nil {
		return VolumeExportInfo{}, err
	}
	defer e.endRead(a)

	path, err := e.archivePath(exportID)
	if err != nil {
		return VolumeExportInfo{}, err
	}
	fi, serr := os.Lstat(path)
	if serr != nil {
		// 登记还在但文件没了：报"不存在"，调用方的正确反应是重新导出。
		return VolumeExportInfo{}, ErrVolumeExportNotFound
	}
	// 大小必须与登记一致：被截断的归档若被当成完整归档传给目标端，
	// 会在解包时失败——而那时已经花掉了整个传输时间。
	if fi.Size() != a.size {
		return VolumeExportInfo{}, fmt.Errorf("卷归档大小与登记不一致（登记 %d，实际 %d）", a.size, fi.Size())
	}
	return a.info(), nil
}

// GetChunk 读取一块字节。
func (e *VolumeExporter) GetChunk(ctx context.Context, exportID string, offset, maxBytes int64) ([]byte, bool, error) {
	if offset < 0 {
		return nil, false, ErrBadOffset
	}
	a, err := e.lookup(exportID)
	if err != nil {
		return nil, false, err
	}
	defer e.endRead(a)

	if maxBytes <= 0 || maxBytes > ChunkSize {
		maxBytes = ChunkSize
	}
	path, err := e.archivePath(exportID)
	if err != nil {
		return nil, false, err
	}
	f, oerr := os.Open(path)
	if oerr != nil {
		return nil, false, ErrVolumeExportNotFound
	}
	defer func() { _ = f.Close() }()

	if offset >= a.size {
		return nil, true, nil
	}
	buf := make([]byte, maxBytes)
	n, rerr := f.ReadAt(buf, offset)
	if rerr != nil && !errors.Is(rerr, io.EOF) {
		return nil, false, rerr
	}
	eof := offset+int64(n) >= a.size
	return buf[:n], eof, nil
}
