package agents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	api "arkade/hub/api/agents/v1"
	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
)

type updateExecutorFunc func(context.Context, string, []byte) ([]byte, *envelope.Error)

func (f updateExecutorFunc) Call(ctx context.Context, method string, b []byte) ([]byte, *envelope.Error) {
	return f(ctx, method, b)
}

func TestSelfUpdateUsesSyncedLocalProject(t *testing.T) {
	const id, name = store.LocalAgentID, "arkade"
	for _, tc := range []struct {
		name, hash         string
		managed, wantError bool
	}{
		{"adopted", "synced", true, false},
		{"drift", "different", true, true},
		{"external", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, db, _ := setup(t)
			ctx := t.Context()
			if err := db.EnsureEmbeddedLocalAgent(ctx); err != nil {
				t.Fatal(err)
			}
			if tc.managed {
				now := time.Now()
				err := db.CreateComposeStack(ctx, store.ComposeStack{ID: "stack", AgentID: id, Name: name, Content: "services: {}", EnvContent: "TEST=fixture", ConfigHash: "synced", Version: 1, UpdatedAt: now, CreatedAt: now, SyncedAt: now})
				if err != nil {
					t.Fatal(err)
				}
			}
			var submitted envelope.SelfUpdatePayload
			err := c.d.Registry.RegisterLocal(id, updateExecutorFunc(func(_ context.Context, method string, b []byte) ([]byte, *envelope.Error) {
				switch method {
				case envelope.MethodSelfUpdate:
					var p envelope.SelfUpdatePayload
					if err := json.Unmarshal(b, &p); err != nil {
						t.Error(err)
					}
					if p.Probe {
						if p.Managed != nil || p.RequestID != "" {
							t.Error("probe should not mutate or send config")
						}
						return []byte(`{"project":"arkade"}`), nil
					}
					submitted = p
					return []byte(`{"accepted":true}`), nil
				case envelope.MethodComposeList:
					return []byte(`{"projects":[{"name":"arkade","config_hash":"` + tc.hash + `"}]}`), nil
				default:
					t.Errorf("unexpected method %s", method)
					return nil, envelope.NewError(envelope.CodeUnsupported, "unexpected")
				}
			}))
			if err != nil {
				t.Fatal(err)
			}
			result, err := c.SelfUpdate(ctx, &api.SelfUpdateReq{AgentID: id})
			if tc.wantError {
				if err == nil || submitted.RequestID != "" {
					t.Fatal("drift should prevent submitting an update")
				}
				return
			}
			if err != nil || !result.Accepted || !selfUpdateRequestPattern.MatchString(submitted.RequestID) {
				t.Fatalf("unexpected update result: %+v %v", result, err)
			}
			if tc.managed && (submitted.Managed == nil || submitted.Managed.Project != name || submitted.Managed.EnvContent != "TEST=fixture") {
				t.Fatal("did not dispatch synced config")
			}
			if !tc.managed && submitted.Managed != nil {
				t.Fatal("external project should not receive a snapshot")
			}
		})
	}
}

func TestSelfUpdateStatusClassifiesWithoutLeakingLogs(t *testing.T) {
	session := &sessionStub{ok: true, actor: user.User{Role: user.RoleAdmin}}
	c := New(Deps{Registry: agentlink.NewRegistry(), Session: session})
	const requestID = "0123456789abcdef0123456789abcdef"
	const id = store.LocalAgentID
	state, exit := "exited", 1
	err := c.d.Registry.RegisterLocal(id, updateExecutorFunc(func(_ context.Context, method string, b []byte) ([]byte, *envelope.Error) {
		switch method {
		case envelope.MethodContainerInspect:
			var in envelope.ContainerInspectPayload
			_ = json.Unmarshal(b, &in)
			if in.ContainerID != envelope.SelfUpdateHelperName(requestID) {
				t.Error("wrong helper inspected")
			}
			out, _ := json.Marshal(envelope.ContainerInspectResult{ID: "helper-id", Name: in.ContainerID, State: state, ExitCode: exit})
			return out, nil
		case envelope.MethodContainerLogs:
			return []byte(`{"lines":["open /private/.env: permission denied", "TEST=fixture"]}`), nil
		default:
			t.Errorf("unexpected method %s", method)
			return nil, envelope.NewError(envelope.CodeUnsupported, "unexpected")
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	req := &api.SelfUpdateStatusReq{AgentID: id, RequestID: requestID}
	out, err := c.SelfUpdateStatus(ctx, req)
	if err != nil || out.State != "failed" || !strings.Contains(out.Message, ".env") || strings.Contains(out.Message, "private") || strings.Contains(out.Message, "fixture") {
		t.Fatalf("unexpected status: %+v %v", out, err)
	}
	exit = 0
	out, err = c.SelfUpdateStatus(ctx, req)
	if err != nil || out.State != "succeeded" {
		t.Fatalf("unexpected status: %+v %v", out, err)
	}
	state = "running"
	out, err = c.SelfUpdateStatus(ctx, req)
	if err != nil || out.State != "running" {
		t.Fatalf("unexpected status: %+v %v", out, err)
	}
	session.actor.Role = user.RoleUser
	_, err = c.SelfUpdateStatus(ctx, req)
	requireError(t, err, 403, "该操作仅管理员可用")
}
