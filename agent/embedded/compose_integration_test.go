package embedded

// Linux/WSL opt-in only:
// ARKADE_TEST_COMPOSE=1 ARKADE_TEST_DOCKER_ENDPOINT=unix:///var/run/docker.sock \
//   go test ./embedded -run '^TestComposeIntegration$' -v
// Requires docker compose and an already cached alpine:3.20 (override with
// ARKADE_TEST_COMPOSE_IMAGE=alpine:<cached-tag>). Never pulls images, creates
// volumes, or touches existing projects. Cleanup removes exact owned IDs only.
import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"arkade/protocol/envelope"
)

func TestComposeIntegration(t *testing.T) {
	if os.Getenv("ARKADE_TEST_COMPOSE") != "1" {
		t.Skip("set ARKADE_TEST_COMPOSE=1 to opt into owned real Compose lifecycle")
	}
	if runtime.GOOS != "linux" {
		t.Skip("run this integration test natively on Linux or inside WSL")
	}
	endpoint := os.Getenv("ARKADE_TEST_DOCKER_ENDPOINT")
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "unix" || u.Host != "" || u.Path == "" {
		t.Fatal("ARKADE_TEST_DOCKER_ENDPOINT must be an explicit unix:///path socket")
	}
	binary, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal("opt-in Compose integration requires the docker CLI")
	}
	// Docker gives DOCKER_CONTEXT priority over DOCKER_HOST. Keep the embedded
	// compose subprocess and the explicitly targeted verification CLI identical.
	t.Setenv("DOCKER_CONTEXT", "")
	t.Setenv("DOCKER_HOST", endpoint)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cli := func(ctx context.Context, args ...string) ([]byte, error) {
		// Explicit --host takes precedence over inherited contexts/DOCKER_HOST.
		cmd := exec.CommandContext(ctx, binary, append([]string{"--host", endpoint}, args...)...)
		return cmd.CombinedOutput()
	}
	if out, err := cli(ctx, "compose", "version"); err != nil {
		t.Fatalf("docker compose unavailable: %v: %s", err, out)
	}
	image := os.Getenv("ARKADE_TEST_COMPOSE_IMAGE")
	if image == "" {
		image = "alpine:3.20"
	}
	if !strings.HasPrefix(image, "alpine:") && !strings.HasPrefix(image, "docker.io/library/alpine:") {
		t.Fatal("integration image must be a cached alpine tag")
	}
	out, err := cli(ctx, "image", "inspect", "--format", "{{.Id}}", image)
	if err != nil {
		t.Fatalf("required cached image unavailable; no pull attempted: %v: %s", err, out)
	}
	imageID := strings.TrimSpace(string(out))
	if !strings.HasPrefix(imageID, "sha256:") || len(imageID) != 71 {
		t.Fatal("image inspect did not return a full content ID")
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	owner := hex.EncodeToString(nonce[:])
	project := "arkade-embedded-compose-" + owner
	const ownerLabel = "io.arkade.embedded-compose-test.owner"
	// No networks or volumes: the only daemon resource is the owned container.
	config := fmt.Sprintf("services:\n  sleeper:\n    image: %s\n    pull_policy: never\n    command: [sleep, '120']\n    network_mode: none\n    read_only: true\n    cap_drop: [ALL]\n    labels:\n      %s: '%s'\n", imageID, ownerLabel, owner)
	dataDir := t.TempDir()
	e, err := New(ctx, Options{DataDir: dataDir, Runtime: "docker", Endpoint: endpoint, ComposeBin: binary, AgentID: project})
	if err != nil {
		t.Fatal(err)
	}
	// Registered after TempDir cleanup: close/join commands first, then independently
	// re-check both labels before deleting any exact ID. Never use compose down here.
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := e.Close(cleanupCtx); err != nil {
			t.Errorf("executor still active; refusing concurrent cleanup: %v", err)
			return
		}
		ids, err := cli(cleanupCtx, "ps", "-aq", "--no-trunc", "--filter", "label=com.docker.compose.project="+project)
		if err != nil {
			t.Errorf("list owned cleanup candidates: %v: %s", err, ids)
			return
		}
		for _, id := range strings.Fields(string(ids)) {
			if !composeIntegrationOwned(t, cleanupCtx, cli, id, project, ownerLabel, owner) {
				continue
			}
			if out, err := cli(cleanupCtx, "rm", "-f", id); err != nil {
				t.Errorf("remove exact owned container %s: %v: %s", id, err, out)
			}
		}
	})
	info, err := e.Info(ctx)
	if err != nil || info.Kind != "docker" || info.Endpoint != endpoint {
		t.Fatalf("explicit Docker endpoint degraded or changed: %+v: %v", info, err)
	}
	// Random names alone are not ownership evidence: insist on an empty project
	// before up, and on both labels + exact ID before each subsequent mutation.
	ids, err := cli(ctx, "ps", "-aq", "--no-trunc", "--filter", "label=com.docker.compose.project="+project)
	if err != nil || len(strings.Fields(string(ids))) != 0 {
		t.Fatalf("project collision or inability to prove absence: %v: %s", err, ids)
	}
	composeIntegrationOperation(t, ctx, e, project, owner, "up", config)
	workspace := filepath.Join(dataDir, "stacks", project, "compose.yml")
	if content, err := os.ReadFile(workspace); err != nil || string(content) != config {
		t.Fatalf("compose did not persist the exact isolated config: %v", err)
	}
	ids, err = cli(ctx, "ps", "-aq", "--no-trunc", "--filter", "label=com.docker.compose.project="+project)
	if err != nil || len(strings.Fields(string(ids))) != 1 {
		t.Fatalf("expected one owned project container: %v: %s", err, ids)
	}
	id := strings.TrimSpace(string(ids))
	if !composeIntegrationOwned(t, ctx, cli, id, project, ownerLabel, owner) {
		t.Fatal("up produced an unowned container; refusing further mutation")
	}
	composeIntegrationOperation(t, ctx, e, project, owner, "stop", "")
	state, err := cli(ctx, "inspect", "--format", "{{.State.Running}}", id)
	if err != nil || strings.TrimSpace(string(state)) != "false" {
		t.Fatalf("owned container still running after stop: %v: %s", err, state)
	}
	if !composeIntegrationOwned(t, ctx, cli, id, project, ownerLabel, owner) {
		t.Fatal("ownership changed before remove")
	}
	composeIntegrationOperation(t, ctx, e, project, owner, "remove", "")
	ids, err = cli(ctx, "ps", "-aq", "--no-trunc", "--filter", "label=com.docker.compose.project="+project)
	if err != nil || len(strings.Fields(string(ids))) != 0 {
		t.Fatalf("remove left project containers: %v: %s", err, ids)
	}
	if _, err := os.Stat(filepath.Dir(workspace)); !os.IsNotExist(err) {
		t.Fatalf("remove left project workspace (or stat failed): %v", err)
	}
}

func composeIntegrationOwned(t *testing.T, ctx context.Context, cli func(context.Context, ...string) ([]byte, error), id, project, label, owner string) bool {
	t.Helper()
	if len(id) != 64 {
		t.Error("refusing mutation: container ID is not a full ID")
		return false
	}
	out, err := cli(ctx, "inspect", "--format", "{{json .Config.Labels}}", id)
	var labels map[string]string
	if err != nil || json.Unmarshal(out, &labels) != nil || labels[label] != owner || labels["com.docker.compose.project"] != project || labels["com.docker.compose.service"] != "sleeper" {
		t.Errorf("refusing mutation: exact container ownership could not be proven: %v", err)
		return false
	}
	return true
}

func composeIntegrationOperation(t *testing.T, ctx context.Context, e *Executor, project, owner, action, config string) {
	t.Helper()
	var task envelope.ComposeOperationResult
	dockerIntegrationCall(t, ctx, e, envelope.MethodComposeOperationStart, envelope.ComposeOperationStartPayload{
		Name: project, RequestID: owner + "-" + action, Action: action, Config: config, RemoveVolumes: false,
	}, &task)
	if task.TaskID == "" || task.Name != project || task.Action != action {
		t.Fatalf("invalid %s task identity: %+v", action, task)
	}
	for {
		switch task.Status {
		case "succeeded":
			if task.Error != "" {
				t.Fatalf("%s succeeded with error: %s", action, task.Error)
			}
			return
		case "failed", "canceled":
			t.Fatalf("%s failed: error=%s events=%+v", action, task.Error, task.Output)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s did not finish: %v", action, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
		dockerIntegrationCall(t, ctx, e, envelope.MethodComposeOperationGet, envelope.ComposeOperationGetPayload{TaskID: task.TaskID}, &task)
	}
}
