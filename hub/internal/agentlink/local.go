package agentlink

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"arkade/protocol/envelope"
)

// LocalExecutor 复用 Agent 方法协议，生命周期由注册者负责；实现应遵守 ctx。
type LocalExecutor interface {
	Call(ctx context.Context, method string, payload []byte) ([]byte, *envelope.Error)
}
type localEntry struct {
	executor LocalExecutor
	ctx      context.Context
	cancel   context.CancelFunc
}

// ReserveLocalID 永久保留本机身份，防止关闭本机执行器后远程冒充。
// 已存在远程连接时拒绝抢占，调用方应在开放远程接入前完成保留。
func (r *Registry) ReserveLocalID(agentID string) error {
	if strings.TrimSpace(agentID) == "" {
		return errors.New("本机 Agent ID 不能为空")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.conns[agentID]; ok {
		return errors.New("本机 Agent ID 已被远程连接占用")
	}
	if r.reservedLocal == nil {
		r.reservedLocal = make(map[string]struct{})
	}
	r.reservedLocal[agentID] = struct{}{}
	return nil
}

// RegisterLocal 注册本机执行器；重复注册拒绝，避免生命周期所有者被悄悄替换。
func (r *Registry) RegisterLocal(agentID string, executor LocalExecutor) error {
	if strings.TrimSpace(agentID) == "" {
		return errors.New("本机 Agent ID 不能为空")
	}
	if executor == nil {
		return errors.New("本机执行器不能为空")
	}
	value := reflect.ValueOf(executor)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return errors.New("本机执行器不能为空")
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.conns[agentID]; ok {
		return errors.New("本机 Agent ID 已被远程连接占用")
	}
	if r.locals[agentID] != nil {
		return errors.New("本机执行器已注册")
	}
	if r.locals == nil {
		r.locals = make(map[string]*localEntry)
	}
	if r.reservedLocal == nil {
		r.reservedLocal = make(map[string]struct{})
	}
	if r.localSlots == nil {
		r.localSlots = make(chan struct{}, 64)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.locals[agentID] = &localEntry{executor: executor, ctx: ctx, cancel: cancel}
	r.reservedLocal[agentID] = struct{}{}
	return nil
}

// RemoveLocal 只注销执行器并取消在途调用，不释放身份保留，不关闭 owner 的资源。
func (r *Registry) RemoveLocal(agentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if local := r.locals[agentID]; local != nil {
		delete(r.locals, agentID)
		local.cancel()
	}
}
func (r *Registry) IsLocal(agentID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.locals[agentID] != nil
}

// IsReservedLocal 用于远程握手写数据库前拒绝本机身份；注销后仍然返回 true。
func (r *Registry) IsReservedLocal(agentID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.reservedLocal[agentID]
	return ok
}

func (r *Registry) callLocal(ctx context.Context, agentID, method string, payload any, local *localEntry) ([]byte, *ConnError) {
	callCtx, cancel := timeoutContext(ctx, requestTimeoutFor(method))
	defer cancel()
	stop := context.AfterFunc(local.ctx, cancel)
	defer stop()
	if local.ctx.Err() != nil {
		return nil, newOfflineError(agentID)
	}
	if callCtx.Err() != nil {
		return nil, localTimeoutError(callCtx, agentID, method)
	}
	env, err := envelope.NewRequest(newRequestID(), method, "agent:"+agentID, payload)
	if err != nil {
		return nil, &ConnError{Code: envelope.CodeBadRequest, Message: "构造请求失败", Wrapped: err}
	}
	// 即使错误实现忽略取消，最多保留64个执行 goroutine；后续请求按自身超时等待槽位。
	select {
	case r.localSlots <- struct{}{}:
	case <-local.ctx.Done():
		return nil, newOfflineError(agentID)
	case <-callCtx.Done():
		return nil, localTimeoutError(callCtx, agentID, method)
	}
	results := make(chan result, 1)
	go func() {
		defer func() { <-r.localSlots }()
		res := result{}
		defer func() {
			if recover() != nil {
				res = result{err: &ConnError{Code: "internal_error", Message: "本机执行器内部错误"}}
			}
			results <- res
		}()
		raw, protocolErr := local.executor.Call(callCtx, method, env.Payload)
		if protocolErr != nil {
			res.err = &ConnError{Code: protocolErr.Code, Message: protocolErr.Message}
			return
		}
		res.payload = raw
	}()
	select {
	case <-local.ctx.Done():
		return nil, newOfflineError(agentID)
	case <-callCtx.Done():
		if local.ctx.Err() != nil {
			return nil, newOfflineError(agentID)
		}
		return nil, localTimeoutError(callCtx, agentID, method)
	case res := <-results:
		if local.ctx.Err() != nil {
			return nil, newOfflineError(agentID)
		}
		if callCtx.Err() != nil {
			return nil, localTimeoutError(callCtx, agentID, method)
		}
		return res.payload, res.err
	}
}
func localTimeoutError(ctx context.Context, agentID, method string) *ConnError {
	err := newTimeoutError(agentID, method)
	err.Wrapped = ctx.Err()
	return err
}
