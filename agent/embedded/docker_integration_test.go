package embedded

// Opt in on Linux with ARKADE_TEST_DOCKER_ENDPOINT=unix:///var/run/docker.sock.
// Existing containers are only listed/inspected/sampled. Mutations target a newly
// created, uniquely labelled container by full ID; images are never pulled.
import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"arkade/protocol/envelope"
)

func TestDockerIntegration(t *testing.T) {
	endpoint := os.Getenv("ARKADE_TEST_DOCKER_ENDPOINT")
	if endpoint == "" {
		t.Skip("set ARKADE_TEST_DOCKER_ENDPOINT to opt into real Docker integration")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "unix" || u.Host != "" || u.Path == "" {
		t.Fatal("integration endpoint must be an explicit unix:///path Docker socket")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	e, err := New(ctx, Options{DataDir: t.TempDir(), Runtime: "docker", Endpoint: endpoint, AgentID: "embedded-docker-integration"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		if err := e.Close(closeCtx); err != nil {
			t.Errorf("close embedded executor: %v", err)
		}
	})
	info, err := e.Info(ctx)
	if err != nil || info.Kind != "docker" || info.Version == "" {
		t.Fatalf("explicit Docker endpoint must not degrade: kind=%s err=%v", info.Kind, err)
	}
	var list envelope.ContainerListResult
	dockerIntegrationCall(t, ctx, e, envelope.MethodContainerList, struct{}{}, &list)
	if list.AgentID != "embedded-docker-integration" {
		t.Fatal("container list lost embedded Agent identity")
	}
	var snapshot envelope.MonitorSnapshot
	dockerIntegrationCall(t, ctx, e, envelope.MethodContainerMonitorSnapshot, envelope.MonitorSnapshotPayload{}, &snapshot)
	if snapshot.At <= 0 || !snapshot.Complete || snapshot.Error != "" {
		t.Fatalf("incomplete real Docker monitor sample: complete=%v error=%s", snapshot.Complete, snapshot.Error)
	}
	for _, container := range list.Containers {
		if container.ID == "" || container.Name == "" {
			t.Fatal("real Docker list returned an empty identity")
		}
	}
	// Inspect an existing container read-only, when available. Never mutate it.
	if len(list.Containers) > 0 {
		var inspect envelope.ContainerInspectResult
		raw := dockerIntegrationCall(t, ctx, e, envelope.MethodContainerInspect, envelope.ContainerInspectPayload{ContainerID: list.Containers[0].ID}, &inspect)
		if inspect.ID == "" || inspect.Name == "" {
			t.Fatal("real Docker inspect returned an empty identity")
		}
		dockerIntegrationSafeView(t, raw)
	}

	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", u.Path)
	}}
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second}
	t.Cleanup(transport.CloseIdleConnections)
	t.Run("owned_cached_alpine_lifecycle", func(t *testing.T) {
		var images []struct {
			ID       string
			RepoTags []string
		}
		dockerIntegrationAPI(ctx, t, client, http.MethodGet, "/images/json", nil, &images, http.StatusOK)
		imageID := ""
		for _, image := range images {
			for _, tag := range image.RepoTags {
				if strings.HasPrefix(tag, "alpine:") || strings.HasPrefix(tag, "docker.io/library/alpine:") {
					imageID = image.ID
					break
				}
			}
			if imageID != "" {
				break
			}
		}
		if imageID == "" {
			t.Skip("no cached alpine image; read-only integration passed, no image will be pulled")
		}
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			t.Fatal(err)
		}
		owner := hex.EncodeToString(nonce[:])
		name := "arkade-embedded-test-" + owner
		const ownerLabel = "io.arkade.embedded-test.owner"
		var created struct{ ID string }
		body := map[string]any{
			"Image":      imageID,
			"Cmd":        []string{"sh", "-c", "for i in 1 2 3 4 5; do echo arkade-embedded-line-$i; done; exec sleep 60"},
			"Env":        []string{"ARKADE_EMBEDDED_TEST_SECRET=must-not-leak"},
			"Labels":     map[string]string{ownerLabel: owner},
			"HostConfig": map[string]any{"NetworkMode": "none", "ReadonlyRootfs": true, "CapDrop": []string{"ALL"}, "LogConfig": map[string]any{"Type": "json-file"}},
		}
		dockerIntegrationAPI(ctx, t, client, http.MethodPost, "/containers/create?name="+url.QueryEscape(name), body, &created, http.StatusCreated)
		if len(created.ID) != 64 {
			t.Fatal("Docker create did not return a full container ID")
		}
		// Register cleanup before any RPC can fail. Only delete this exact ID after
		// independently re-checking its unique ownership label and name.
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cleanupCancel()
			var inspect struct {
				Name   string
				Config struct{ Labels map[string]string }
			}
			status := dockerIntegrationAPI(cleanupCtx, t, client, http.MethodGet, "/containers/"+created.ID+"/json", nil, &inspect, http.StatusOK, http.StatusNotFound)
			if status == http.StatusNotFound {
				return
			}
			if inspect.Config.Labels[ownerLabel] != owner || inspect.Name != "/"+name {
				t.Error("refusing cleanup: owned container identity/label changed")
				return
			}
			dockerIntegrationAPI(cleanupCtx, t, client, http.MethodDelete, "/containers/"+created.ID+"?force=1&v=0", nil, nil, http.StatusNoContent)
		})
		var op envelope.ContainerOpResult
		dockerIntegrationCall(t, ctx, e, envelope.MethodContainerStart, envelope.ContainerOpPayload{ContainerID: created.ID}, &op)
		if op.State != "running" {
			t.Fatalf("start state=%s", op.State)
		}
		var inspect envelope.ContainerInspectResult
		raw := dockerIntegrationCall(t, ctx, e, envelope.MethodContainerInspect, envelope.ContainerInspectPayload{ContainerID: created.ID}, &inspect)
		if inspect.Name != name || inspect.State != "running" {
			t.Fatal("owned container inspect disagrees with start")
		}
		dockerIntegrationSafeView(t, raw)
		if bytes.Contains(raw, []byte("must-not-leak")) {
			t.Fatal("inspect leaked container environment secret")
		}
		// Wait for the deterministic finite output, not an arbitrary host log.
		logCtx, logCancel := context.WithTimeout(ctx, 10*time.Second)
		defer logCancel()
		for {
			var logs envelope.ContainerLogsResult
			dockerIntegrationCall(t, logCtx, e, envelope.MethodContainerLogs, envelope.ContainerLogsPayload{ContainerID: created.ID, TailLines: 2}, &logs)
			if len(logs.Lines) > 2 {
				t.Fatal("container.logs exceeded requested line bound")
			}
			if len(logs.Lines) == 2 && strings.Contains(strings.Join(logs.Lines, "\n"), "arkade-embedded-line-5") {
				break
			}
			select {
			case <-logCtx.Done():
				t.Fatal("owned container logs did not become available")
			case <-time.After(50 * time.Millisecond):
			}
		}
		dockerIntegrationCall(t, ctx, e, envelope.MethodContainerMonitorSnapshot, envelope.MonitorSnapshotPayload{}, &snapshot)
		found := false
		for _, metric := range snapshot.Containers {
			if metric.Name == name {
				found = true
				if metric.State != "running" || metric.ID == "" {
					t.Fatal("owned monitor sample has invalid state/identity")
				}
			}
		}
		if !snapshot.Complete || !found {
			t.Fatal("monitor failed to sample the owned running container")
		}
		dockerIntegrationCall(t, ctx, e, envelope.MethodContainerStop, envelope.ContainerOpPayload{ContainerID: created.ID, Force: true}, &op)
		// Stop acknowledgement may still report running: Docker kill is asynchronous.
		// Confirm the final state through the same embedded inspect handler before remove.
		stopCtx, stopCancel := context.WithTimeout(ctx, 10*time.Second)
		defer stopCancel()
		for {
			dockerIntegrationCall(t, stopCtx, e, envelope.MethodContainerInspect, envelope.ContainerInspectPayload{ContainerID: created.ID}, &inspect)
			if inspect.State == "exited" {
				break
			}
			select {
			case <-stopCtx.Done():
				t.Fatalf("owned container did not reach exited after stop: state=%s", inspect.State)
			case <-time.After(50 * time.Millisecond):
			}
		}
		dockerIntegrationCall(t, ctx, e, envelope.MethodContainerRemove, envelope.ContainerOpPayload{ContainerID: created.ID}, &op)
		// Cleanup still independently checks absence by exact ID.
	})
}

func dockerIntegrationSafeView(t *testing.T, raw []byte) {
	t.Helper()
	var view map[string]json.RawMessage
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}
	for key := range view {
		switch strings.ToLower(key) {
		case "env", "labels", "config", "hostconfig", "secrets":
			t.Fatalf("inspect exposed non-whitelisted top-level field %s", key)
		}
	}
}

func dockerIntegrationCall(t *testing.T, ctx context.Context, e *Executor, method string, payload, result any) []byte {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	raw, rpcErr := e.Call(ctx, method, body)
	if rpcErr != nil {
		t.Fatalf("embedded %s failed: %v", method, rpcErr)
	}
	if err := json.Unmarshal(raw, result); err != nil {
		t.Fatalf("decode %s: %v", method, err)
	}
	return raw
}

func dockerIntegrationAPI(ctx context.Context, t *testing.T, client *http.Client, method, path string, body, result any, statuses ...int) int {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Docker %s request failed: %v", method, err)
	}
	defer resp.Body.Close()
	for _, status := range statuses {
		if resp.StatusCode == status {
			if result != nil && status != http.StatusNotFound {
				if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(result); err != nil {
					t.Fatalf("decode Docker response: %v", err)
				}
			}
			return status
		}
	}
	t.Fatal(fmt.Sprintf("Docker %s %s returned HTTP %d", method, path, resp.StatusCode))
	return 0
}
