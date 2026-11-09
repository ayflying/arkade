package imagetransfer

// limitwriter.go 实现"写入前限额"的写端包装。
//
// ============================ 为什么不能事后检查 ============================
//
// 最自然的写法是 io.Copy(f, rc) 然后比较 f 的大小：
//
//	written, _ := io.Copy(f, rc)
//	if written > limit { return error }   // ← 错
//
// 这是**事后**检查：磁盘此时已经被写满（或写了很多 GB 才停），
// 破坏已经造成。一个异常或恶意的引擎只要持续输出，
// 就能在报错之前把宿主机磁盘写穿，连带拖死这台机器上的其它服务。
//
// 正确做法是**在每次写入之前**判断"再写这么多会不会超"，
// 超了就不写、直接返回错误。这样归档落盘量**永远不超过上限**，
// 而不是"超过之后报告一个错误"。
//
// limitWriter 就是这个闸门。它同时承担三件事：
//  1. 单归档硬上限；
//  2. 每块之前的外部检查（全局总量预算、ctx 取消）；
//  3. 顺带计算 SHA256 与上报进度（避免多趟读盘）。

import (
	"io"
)

// saveCopyBufSize 是导出时的固定拷贝缓冲大小（1 MiB）。
//
// 固定大小、与归档无关：不能用"按归档大小分配缓冲"的做法，
// 那本身就是一种内存放大。
const saveCopyBufSize = 1 << 20

// limitWriter 是带写入前限额的文件写端。
//
// 刻意**不实现** io.ReaderFrom：io.Copy 会优先使用目标端的 ReadFrom
// （或源端的 WriteTo），一旦命中就会绕过我们的 Write，
// 使限额检查形同虚设。少实现一个方法，比"记得不要用 io.Copy"可靠。
type limitWriter struct {
	dst  io.Writer
	hash io.Writer

	// limit 是单归档允许写入的最大字节数。
	limit int64
	// written 是已成功写入的字节数。
	written int64

	// beforeWrite 在每次真正写入之前调用；返回非 nil 则不写入并中止。
	// 参数是本次准备写入的字节数。
	beforeWrite func(n int64) error
	// onProgress 在每次写入之后调用（可为 nil）。
	onProgress func(written int64)
}

// Write 实现 io.Writer。
//
// 关键顺序：**先判断，再写入**。
func (w *limitWriter) Write(p []byte) (int, error) {
	n := int64(len(p))

	// 闸门 1：单归档硬上限。超了连一个字节都不写。
	if w.written+n > w.limit {
		return 0, ErrArchiveTooLarge
	}
	// 闸门 2：外部检查（全局预算 / ctx）。
	if w.beforeWrite != nil {
		if err := w.beforeWrite(n); err != nil {
			return 0, err
		}
	}

	written, err := w.dst.Write(p)
	w.written += int64(written)
	if w.hash != nil && written > 0 {
		_, _ = w.hash.Write(p[:written])
	}
	if w.onProgress != nil {
		w.onProgress(w.written)
	}
	return written, err
}

// copyWithLimit 用固定缓冲把 src 复制到 dst（dst 是 limitWriter）。
//
// 不用 io.CopyBuffer 的直接原因：它会检查 src 是否实现 WriterTo
// 或 dst 是否实现 ReaderFrom，命中就走捷径、绕过 limitWriter.Write。
// 这里手写循环，确保每一块都经过限额判断——这正是本函数存在的意义。
//
// 另外每块之间没有额外的 ctx 检查：ctx 的检查由 limitWriter.beforeWrite
// 负责（它每一块都会被调用一次），避免两处判断标准不一致。
func copyWithLimit(dst *limitWriter, src io.Reader, buf []byte) error {
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			written, werr := dst.Write(buf[:n])
			if werr != nil {
				return werr
			}
			if written != n {
				// 短写（正常文件不会发生）：按 io.Writer 的约定视为错误，
				// 否则会导致数据静默丢失。
				return io.ErrShortWrite
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return nil
			}
			return rerr
		}
	}
}
