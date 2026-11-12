package handler

import (
	"context"
	"testing"

	"arkade/protocol/envelope"
)

// 旧 Runtime fake 不实现可选能力，也不必跟随监控功能扩充接口。
func TestMonitorUnsupportedOldRuntime(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg, &fakeRuntime{}, nil, nil)
	if reg.Has(envelope.MethodContainerMonitorSnapshot) {
		t.Fatal("unsupported monitor advertised")
	}
	_, err := newMonitorSnapshotHandler(&fakeRuntime{})(context.Background(), []byte(`{}`))
	if err == nil {
		t.Fatal("missing unsupported error")
	}
}
