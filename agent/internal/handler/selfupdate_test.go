package handler

import (
	"context"
	"testing"

	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

type selfUpdateProbeFixture struct {
	runtime.Runtime
	probes int
	runs   int
}

func (r *selfUpdateProbeFixture) SelfContainerInfo(context.Context) (runtime.SelfContainerInfo, error) {
	r.probes++
	return runtime.SelfContainerInfo{Project: "arkade", Service: "hub"}, nil
}

func (r *selfUpdateProbeFixture) RunDetachedComposeUp(context.Context, runtime.DetachedComposeUp) (string, error) {
	r.runs++
	return "helper", nil
}

func TestSelfUpdateProbeNoSideEffects(t *testing.T) {
	rt := &selfUpdateProbeFixture{}
	h := newSelfUpdateHandler(rt, true)
	out, err := h(context.Background(), []byte(`{"probe":true}`))
	res, ok := out.(envelope.SelfUpdateResult)
	if err != nil || !ok || res.Project != "arkade" || res.Accepted || rt.probes != 1 || rt.runs != 0 {
		t.Fatalf("探测必须只读取身份：out=%+v err=%v reads=%d writes=%d", out, err, rt.probes, rt.runs)
	}
	if _, err := h(context.Background(), []byte(`{"probe":true,"request_id":"test"}`)); err == nil || rt.probes != 1 || rt.runs != 0 {
		t.Fatal("探测不能混用更新参数")
	}
}

func TestSelfUpdateRemoteCannotUseManagedConfig(t *testing.T) {
	rt := &selfUpdateProbeFixture{}
	h := newSelfUpdateHandler(rt, false)
	_, err := h(context.Background(), []byte(`{"request_id":"test","managed":{"project":"arkade","content":"services: {}"}}`))
	if err == nil || rt.probes != 0 || rt.runs != 0 {
		t.Fatal("独立执行器不能使用纳管快照或探测其归属")
	}
}

func TestSelfUpdateRejectsMismatchedLiveProject(t *testing.T) {
	rt := &selfUpdateProbeFixture{}
	h := newSelfUpdateHandler(rt, true)
	out, err := h(context.Background(), []byte(`{"request_id":"test","managed":{"project":"another","content":"services: {}"}}`))
	res, ok := out.(envelope.SelfUpdateResult)
	if err != nil || !ok || len(res.Blockers) != 1 || res.Accepted || rt.probes != 1 || rt.runs != 0 {
		t.Fatalf("更新必须重新比较真实项目，且不能产生写操作：out=%+v err=%v", out, err)
	}
}
