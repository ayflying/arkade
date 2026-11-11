package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
)

func TestResourcesOnlineContainerRPCMappings(t *testing.T) {
	e := newTestEnv(t)
	a := startImportAgent(t, e, func(req envelope.Envelope, _ int) any {
		switch req.Method {
		case envelope.MethodContainerInspect:
			var p envelope.ContainerInspectPayload
			if err := envelope.DecodePayload(&req, &p); err != nil || p.ContainerID != "real-container" {
				t.Errorf("inspect %+v %v", p, err)
			}
			return envelope.ContainerInspectResult{ID: p.ContainerID, Name: "fixture", Image: "alpine"}
		case envelope.MethodContainerLogs:
			var p envelope.ContainerLogsPayload
			if err := envelope.DecodePayload(&req, &p); err != nil || p.ContainerID != "real-container" || p.TailLines != 17 {
				t.Errorf("logs %+v %v", p, err)
			}
			return envelope.ContainerLogsResult{Lines: []string{"online-container-log"}}
		case envelope.MethodContainerStart, envelope.MethodContainerStop, envelope.MethodContainerRestart, envelope.MethodContainerRemove:
			var p envelope.ContainerOpPayload
			if err := envelope.DecodePayload(&req, &p); err != nil || p.ContainerID != "real-container" {
				t.Errorf("operation %+v %v", p, err)
			}
			if req.Method == envelope.MethodContainerRemove {
				if !p.Force || !p.RemoveVolumes {
					t.Errorf("remove flags %+v", p)
				}
			} else if p.Force || p.RemoveVolumes {
				t.Errorf("unexpected destructive flags %+v", p)
			}
			return envelope.ContainerOpResult{ContainerID: p.ContainerID, State: "mapped-state"}
		}
		t.Errorf("unexpected RPC %s", req.Method)
		return nil
	})
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	base := a.url + "/api/agents/" + a.id + "/containers/real-container"
	alias := "container_id=wrong&CONTAINER-ID=wrong&agent_id=missing"
	for _, tc := range []struct{ method, path, body, contains string }{
		{http.MethodGet, "?" + alias, "", `"id":"real-container"`},
		{http.MethodGet, "/logs?tail=17&" + alias, "", "online-container-log"},
		{http.MethodPost, "/start?" + alias, `{"container_id":"body-wrong"}`, `"container_id":"real-container"`},
		{http.MethodPost, "/stop?" + alias, "", `"state":"mapped-state"`},
		{http.MethodPost, "/restart?" + alias, "", `"state":"mapped-state"`},
		{http.MethodDelete, "?force=true&remove_volumes=true&" + alias, "", `"state":"mapped-state"`},
	} {
		resp, raw := doHTTP(t, tc.method, base+tc.path, tc.body, cookies)
		if resp.StatusCode != 200 || !strings.Contains(string(raw), tc.contains) {
			t.Fatalf("%s %s status=%d %s", tc.method, tc.path, resp.StatusCode, raw)
		}
	}
	a.assertMethods(t, envelope.MethodContainerInspect, envelope.MethodContainerLogs, envelope.MethodContainerStart, envelope.MethodContainerStop, envelope.MethodContainerRestart, envelope.MethodContainerRemove)
}

func TestResourcesOnlineStackRPCMappings(t *testing.T) {
	e := newTestEnv(t)
	const name = "online-stack"
	hash := composeConfigHash(sampleCompose)
	a := startImportAgent(t, e, func(req envelope.Envelope, _ int) any {
		switch req.Method {
		case envelope.MethodComposeUp:
			var p envelope.ComposeSpecPayload
			if err := envelope.DecodePayload(&req, &p); err != nil || p.Name != name || p.Content != sampleCompose || p.ContentHash != hash {
				t.Errorf("up %+v %v", p, err)
			}
		case envelope.MethodComposeLogs:
			var p envelope.ComposeLogsPayload
			if err := envelope.DecodePayload(&req, &p); err != nil || p.Name != name || p.Service != "web" || p.TailLines != 19 {
				t.Errorf("logs %+v %v", p, err)
			}
			return envelope.ComposeLogsResult{Lines: []string{"online-stack-log"}, Truncated: true}
		case envelope.MethodComposeBackup:
			var p envelope.ComposeBackupPayload
			if err := envelope.DecodePayload(&req, &p); err != nil || p.Name != name || !p.IncludeVolumes {
				t.Errorf("backup %+v %v", p, err)
			}
			return envelope.ComposeBackupResult{Name: p.Name, Path: "/fixture/backup", SizeBytes: 123, Files: []string{"compose.yml"}, CreatedAt: 42}
		case envelope.MethodComposePS, envelope.MethodComposeDown, envelope.MethodComposeStop, envelope.MethodComposeRestart, envelope.MethodComposePull, envelope.MethodComposeRebuild, envelope.MethodComposeRemove:
			var p envelope.ComposeOpPayload
			if err := envelope.DecodePayload(&req, &p); err != nil || p.Name != name {
				t.Errorf("operation %+v %v", p, err)
			}
			if req.Method == envelope.MethodComposePS {
				return envelope.ContainerListResult{Containers: []envelope.ContainerSummary{{ID: "stack-child", StackID: name}}}
			}
			if len(p.Services) != 1 || p.Services[0] != "web" || !p.RemoveVolumes || p.Force != (req.Method == envelope.MethodComposeRebuild) {
				t.Errorf("action flags %s %+v", req.Method, p)
			}
		default:
			t.Errorf("unexpected RPC %s", req.Method)
		}
		return envelope.ComposeOpResult{Name: name, Status: "running", ConfigHash: hash, Output: "mapped-operation"}
	})
	e.seedStack(t, a.id, name, sampleCompose)
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	base := a.url + "/api/agents/" + a.id + "/stacks/" + name
	for _, tc := range []struct{ path, want string }{{"/ps?name=wrong&agent_id=missing", "stack-child"}, {"/logs?name=wrong&service=web&tail=19", "online-stack-log"}} {
		resp, raw := doHTTP(t, http.MethodGet, base+tc.path, "", cookies)
		if resp.StatusCode != 200 || !strings.Contains(string(raw), tc.want) {
			t.Fatalf("%s %d %s", tc.path, resp.StatusCode, raw)
		}
	}
	for _, action := range []string{"up", "down", "stop", "restart", "pull", "rebuild", "backup", "remove"} {
		input := map[string]any{"action": action, "services": []string{"web"}, "remove_volumes": true, "include_volumes": true}
		body, _ := json.Marshal(input)
		resp, raw := doHTTP(t, http.MethodPost, base+"/actions?name=query-wrong&agent_id=missing", string(body), cookies)
		want := "mapped-operation"
		if action == "backup" {
			want = "/fixture/backup"
		}
		if resp.StatusCode != 200 || !strings.Contains(string(raw), want) {
			t.Fatalf("action %s %d %s", action, resp.StatusCode, raw)
		}
		if action != "remove" {
			if _, err := e.db.GetComposeStack(t.Context(), a.id, name); err != nil {
				t.Fatalf("action %s removed DB record: %v", action, err)
			}
		}
	}
	if _, err := e.db.GetComposeStack(t.Context(), a.id, name); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("remove retained stack %v", err)
	}
	a.assertMethods(t, envelope.MethodComposePS, envelope.MethodComposeLogs, envelope.MethodComposeUp, envelope.MethodComposeDown, envelope.MethodComposeStop, envelope.MethodComposeRestart, envelope.MethodComposePull, envelope.MethodComposeRebuild, envelope.MethodComposeBackup, envelope.MethodComposeRemove)
}
