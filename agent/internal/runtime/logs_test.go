package runtime

// logs_test.go 覆盖容器日志的多路复用解帧。
//
// 为什么值得单独测：这个格式错误在端到端联调里才暴露——日志接口返回
// 带控制字符的乱码（帧头字节），而所有"假服务端返回纯文本"的测试都发现不了。
// 这里直接构造两种真实格式的字节流，锁死解帧行为。

import (
	"encoding/binary"
	"strings"
	"testing"
)

// frame 构造一个多路复用帧：1 字节 stream + 3 字节保留 + 4 字节大端长度 + 正文。
func frame(stream byte, text string) []byte {
	b := make([]byte, logFrameHeaderLen+len(text))
	b[0] = stream
	binary.BigEndian.PutUint32(b[4:8], uint32(len(text)))
	copy(b[logFrameHeaderLen:], text)
	return b
}

// TestDecodeLogPayloadFramed 验证非 TTY 容器的多路复用响应能被正确解帧。
func TestDecodeLogPayloadFramed(t *testing.T) {
	var raw []byte
	raw = append(raw, frame(logStreamStdOut, "第一行\n")...)
	raw = append(raw, frame(logStreamStdErr, "错误输出\n")...)
	raw = append(raw, frame(logStreamStdOut, "第三行\n")...)

	got := decodeLogPayload(raw)
	want := "第一行\n错误输出\n第三行\n"
	if got != want {
		t.Fatalf("解帧结果错误:\n期望 %q\n实际 %q", want, got)
	}
	// 关键回归点：结果里绝不能残留帧头控制字符。
	if strings.ContainsAny(got, "\x00\x01\x02") {
		t.Fatalf("解帧后仍残留控制字符: %q", got)
	}
}

// TestDecodeLogPayloadPlain 验证 TTY 容器的纯文本响应原样保留。
func TestDecodeLogPayloadPlain(t *testing.T) {
	plain := "hello-arkade\nsecond line\n"
	if got := decodeLogPayload([]byte(plain)); got != plain {
		t.Fatalf("纯文本不应被改写:\n期望 %q\n实际 %q", plain, got)
	}
}

// TestDecodeLogPayloadEmpty 验证空响应不 panic。
func TestDecodeLogPayloadEmpty(t *testing.T) {
	if got := decodeLogPayload(nil); got != "" {
		t.Fatalf("空响应应返回空串，实际 %q", got)
	}
}

// TestDecodeLogPayloadTruncatedFrame 验证帧不完整时退化为文本，不丢日志。
func TestDecodeLogPayloadTruncatedFrame(t *testing.T) {
	var raw []byte
	raw = append(raw, frame(logStreamStdOut, "完整行\n")...)
	// 人为制造一个"声称 100 字节但只有 5 字节"的坏帧。
	bad := make([]byte, logFrameHeaderLen+5)
	bad[0] = logStreamStdOut
	binary.BigEndian.PutUint32(bad[4:8], 100)
	copy(bad[logFrameHeaderLen:], "tail")
	raw = append(raw, bad...)

	got := decodeLogPayload(raw)
	if !strings.Contains(got, "完整行") {
		t.Fatalf("应保留能解析的帧内容，实际 %q", got)
	}
}
