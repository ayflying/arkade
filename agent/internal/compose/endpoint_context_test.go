package compose

import (
	"strings"
	"testing"
)

func TestExplicitDaemonDiscardsInheritedContextOnly(t *testing.T) {
	t.Setenv("DOCKER_CONTEXT", "wrong-context")
	t.Setenv("DOCKER_TLS_VERIFY", "1")
	t.Setenv("DOCKER_CERT_PATH", "/existing-certs")
	for _, host := range []string{"", "unix:///var/run/docker.sock", "tcp://remote:2375"} {
		r := &Runner{DockerHost: host}
		env := r.environ()
		if hasEnvKey(env, "DOCKER_CONTEXT") != (host == "") {
			t.Fatalf("host=%q context inheritance mismatch", host)
		}
		for _, want := range []string{"DOCKER_TLS_VERIFY=1", "DOCKER_CERT_PATH=/existing-certs"} {
			found := false
			for _, entry := range env {
				if strings.EqualFold(entry, want) {
					found = true
				}
			}
			if !found {
				t.Fatalf("TLS setting unexpectedly removed: %s", want)
			}
		}
	}
}
