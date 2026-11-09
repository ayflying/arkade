package runtime

// logs.go 负责把运行时返回的容器日志整理成可读的行。
//
// 为什么单独成文件：Docker/Podman 的 logs 端点有两种响应格式——
//   - 非 TTY 容器走「多路复用帧」：每帧 8 字节头（stream 类型 + 3 字节保留 + 4 字节大端长度）
//     后跟正文，stdout 与 stderr 交错；
//   - TTY 容器走纯文本，没有帧头。
//
// 直接把响应当字符串处理会把帧头字节混进输出，前端会看到一堆控制字符。
// 这里统一解帧：能识别出合法帧头就逐帧拆开，否则按纯文本处理。

import (
	"encoding/binary"
	"strings"
)

// 多路复用帧头长度与 stream 类型。
const (
	logFrameHeaderLen = 8
	// streamStdIn / streamStdOut / streamStdErr 是帧头首字节的取值（0/1/2）。
	logStreamStdIn  = 0
	logStreamStdOut = 1
	logStreamStdErr = 2
)

// decodeLogPayload 把 logs 端点响应整理成文本行。
//
// 返回的字符串已去掉帧头；行切分交给上层，保证"解帧"与"取尾行"两件事分开，
// 各自可测。
func decodeLogPayload(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	if !looksLikeFramedLogs(raw) {
		// TTY 容器或已被代理层还原成纯文本：原样返回。
		return string(raw)
	}

	var b strings.Builder
	b.Grow(len(raw))
	for off := 0; off+logFrameHeaderLen <= len(raw); {
		stream := raw[off]
		size := int(binary.BigEndian.Uint32(raw[off+4 : off+8]))
		start := off + logFrameHeaderLen
		// 长度越界说明后面不是帧（可能混了尾部纯文本），把剩余内容当文本处理，
		// 避免因为一处不规整就丢掉整段日志。
		if size < 0 || start+size > len(raw) {
			b.Write(raw[off:])
			break
		}
		if stream == logStreamStdOut || stream == logStreamStdErr || stream == logStreamStdIn {
			b.Write(raw[start : start+size])
		} else {
			// 未知 stream：宁可保留内容也不要丢日志。
			b.Write(raw[start : start+size])
		}
		off = start + size
	}
	return b.String()
}

// looksLikeFramedLogs 判断响应是否是合法的多路复用帧序列。
//
// 判定方式：逐帧走一遍，要求首字节是 0/1/2 且长度能对齐。
// 只要有一帧不合法就认为不是帧格式——纯文本日志的首字节几乎不可能
// 恰好构成合法帧序列（尤其是长度字段），因此误判概率极低。
func looksLikeFramedLogs(raw []byte) bool {
	if len(raw) < logFrameHeaderLen {
		return false
	}
	off := 0
	frames := 0
	for off+logFrameHeaderLen <= len(raw) {
		stream := raw[off]
		if stream > logStreamStdErr {
			return false
		}
		size := int(binary.BigEndian.Uint32(raw[off+4 : off+8]))
		// 帧长上限保护：单帧超过 32 MiB 基本不可能是真帧。
		if size < 0 || size > 32<<20 {
			return false
		}
		start := off + logFrameHeaderLen
		if start+size > len(raw) {
			return false
		}
		off = start + size
		frames++
	}
	// 必须至少解析出一帧，且恰好消耗完（允许尾部极短残留，如单个换行）。
	return frames > 0 && off >= len(raw)-1
}
