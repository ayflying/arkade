package imagetask

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"time"
)

func (m *Manager) Start(kind, agent, target, reference string, ownerID string) (Task, error) {
	if agent == "" || reference == "" || (kind != "pull" && kind != "export" && kind != "migrate") || (kind == "migrate" && (target == "" || target == agent)) {
		return Task{}, ErrInvalid
	}
	j, err := m.create(kind, agent, target, ownerID)
	if err != nil {
		return Task{}, err
	}
	initial, _ := m.Get(j.task.ID)
	go func() { m.finish(j, m.run(j, kind, agent, target, reference)) }()
	return initial, nil
}
func (m *Manager) wait(j *job, agent string, r RemoteTask) (RemoteTask, error) {
	for {
		if j.ctx.Err() != nil {
			if j.ctx.Err() == context.DeadlineExceeded {
				return r, ErrTimeout
			}
			return r, ErrCanceled
		}
		switch r.Status {
		case "succeeded", "completed", "success":
			return r, nil
		case "failed", "error", "canceled", "cancelled":
			return r, ErrBackend
		}
		// Remote text is untrusted and may contain registry credentials.
		m.update(j, "等待远程处理", r.BytesDone, r.BytesTotal)
		timer := time.NewTimer(m.pollInterval)
		select {
		case <-j.ctx.Done():
			timer.Stop()
			continue
		case <-timer.C:
		}
		next, err := m.backend.Status(j.ctx, agent, r.ID)
		if err != nil {
			return r, ErrBackend
		}
		if next.ID == "" {
			next.ID = r.ID
		}
		if next.ID != r.ID {
			return r, ErrBackend
		}
		r = next
	}
}
func validArchive(r RemoteTask) bool {
	digest, err := hex.DecodeString(r.SHA256)
	return r.SizeBytes > 0 && r.SizeBytes <= MaxArchiveSize && err == nil && len(digest) == sha256.Size
}
func (m *Manager) run(j *job, kind, agent, target, reference string) error {
	var r RemoteTask
	var err error
	if kind == "pull" {
		r, err = m.backend.StartPull(j.ctx, agent, reference)
	} else {
		r, err = m.backend.StartSave(j.ctx, agent, reference)
	}
	// Even an error response may identify a remotely created resource.
	if r.ID != "" {
		if e := m.register(j, agent, r); e != nil {
			return e
		}
	}
	if err != nil || r.ID == "" {
		return ErrBackend
	}
	r, err = m.wait(j, agent, r)
	if err != nil {
		return err
	}
	if kind == "pull" {
		return nil
	}
	if !validArchive(r) {
		return ErrIntegrity
	}
	if kind == "export" {
		m.mu.Lock()
		j.archive = r
		j.task.ArchiveSize = r.SizeBytes
		m.mu.Unlock()
		return nil
	}
	load, err := m.backend.BeginLoad(j.ctx, target, r.SizeBytes, r.SHA256)
	if load.ID != "" {
		if e := m.register(j, target, load); e != nil {
			return e
		}
	}
	if err != nil || load.ID == "" {
		return ErrBackend
	}
	err = m.copyArchive(j.ctx, j, agent, r, func(offset int64, b []byte) error { return m.backend.PutChunk(j.ctx, target, load.ID, offset, b) })
	if err != nil {
		return err
	}
	committed, err := m.backend.CommitLoad(j.ctx, target, load.ID, r.SHA256)
	if err != nil {
		return ErrBackend
	}
	if committed.ID == "" {
		committed.ID = load.ID
	}
	if committed.ID != load.ID {
		return ErrBackend
	}
	_, err = m.wait(j, target, committed)
	return err
}
func (m *Manager) copyArchive(ctx context.Context, j *job, agent string, r RemoteTask, put func(int64, []byte) error) error {
	h := sha256.New()
	var offset int64
	for {
		if err := ctx.Err(); err != nil {
			return ErrCanceled
		}
		b, eof, err := m.backend.GetChunk(ctx, agent, r.ID, offset, ChunkSize)
		if err != nil {
			return ErrBackend
		}
		if len(b) > ChunkSize || int64(len(b)) > r.SizeBytes-offset || (!eof && len(b) == 0) {
			return ErrIntegrity
		}
		if len(b) > 0 {
			if err := put(offset, b); err != nil {
				return ErrBackend
			}
			_, _ = h.Write(b)
			offset += int64(len(b))
			m.update(j, "传输归档", offset, &r.SizeBytes)
		}
		if eof {
			break
		}
	}
	if offset != r.SizeBytes || !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), r.SHA256) {
		return ErrIntegrity
	}
	return nil
}
func (m *Manager) Download(ctx context.Context, id string, writer io.Writer) error {
	m.mu.Lock()
	j := m.jobs[id]
	if j == nil {
		m.mu.Unlock()
		return ErrNotFound
	}
	if !j.task.DownloadReady || j.downloading {
		m.mu.Unlock()
		return ErrNotReady
	}
	j.downloading = true
	r := j.archive
	agent := j.task.AgentID
	m.mu.Unlock()
	defer func() { m.mu.Lock(); j.downloading = false; m.mu.Unlock() }()
	transfer, stop := context.WithCancel(j.ctx)
	defer stop()
	detach := context.AfterFunc(ctx, stop)
	defer detach()
	err := m.copyArchive(transfer, j, agent, r, func(_ int64, b []byte) error {
		n, e := writer.Write(b)
		if e == nil && n != len(b) {
			return io.ErrShortWrite
		}
		return e
	})
	// Only a complete verified transfer consumes the archive. A failed client
	// write or transient remote error remains retryable until the job deadline.
	if err == nil {
		m.dispose(j, false)
		j.cancel()
	}
	return err
}

func (m *Manager) Upload(ctx context.Context, agent string, ownerID string, reader io.Reader, size int64) (Task, error) {
	if agent == "" || reader == nil || size <= 0 || size > MaxArchiveSize {
		return Task{}, ErrInvalid
	}
	j, err := m.create("import", agent, "", ownerID)
	if err != nil {
		return Task{}, err
	}
	upload, stop := context.WithCancel(j.ctx)
	detach := context.AfterFunc(ctx, stop)
	fail := func(e error) (Task, error) {
		detach()
		stop()
		if ctx.Err() != nil || j.ctx.Err() != nil {
			e = ErrCanceled
		}
		m.finish(j, e)
		t, _ := m.Get(j.task.ID)
		return t, e
	}
	r, err := m.backend.BeginLoad(upload, agent, size, "")
	if r.ID != "" {
		if e := m.register(j, agent, r); e != nil {
			return fail(e)
		}
	}
	if err != nil || r.ID == "" {
		return fail(ErrBackend)
	}
	h := sha256.New()
	buffer := make([]byte, ChunkSize)
	var offset int64
	for offset < size {
		if upload.Err() != nil || ctx.Err() != nil {
			return fail(ErrCanceled)
		}
		want := int64(len(buffer))
		if size-offset < want {
			want = size - offset
		}
		n, e := io.ReadFull(reader, buffer[:int(want)])
		if e != nil {
			return fail(ErrIntegrity)
		}
		if e = m.backend.PutChunk(upload, agent, r.ID, offset, buffer[:n]); e != nil {
			return fail(ErrBackend)
		}
		_, _ = h.Write(buffer[:n])
		offset += int64(n)
		m.update(j, "上传归档", offset, &size)
	}
	// Enforce declared length rather than silently accepting trailing bytes.
	var extra [1]byte
	n, e := io.ReadFull(reader, extra[:])
	if n != 0 || e != io.EOF {
		return fail(ErrIntegrity)
	}
	if upload.Err() != nil || ctx.Err() != nil {
		return fail(ErrCanceled)
	}
	digest := hex.EncodeToString(h.Sum(nil))
	// Detach from the request only after the synchronous upload is complete.
	detach()
	stop()
	t, _ := m.Get(j.task.ID)
	go func() {
		committed, e := m.backend.CommitLoad(j.ctx, agent, r.ID, digest)
		if e != nil {
			m.finish(j, ErrBackend)
			return
		}
		if committed.ID == "" {
			committed.ID = r.ID
		}
		if committed.ID != r.ID {
			m.finish(j, ErrBackend)
			return
		}
		_, e = m.wait(j, agent, committed)
		m.finish(j, e)
	}()
	return t, nil
}
