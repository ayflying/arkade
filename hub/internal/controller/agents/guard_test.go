package agents

import (
	api "arkade/hub/api/agents/v1"
	"arkade/hub/internal/agentlink"
	"testing"
	"time"
)

func TestRevocationsShareEnrollmentGuard(t *testing.T) {
	for _, operation := range []string{"delete", "disable", "enable", "reset"} {
		t.Run(operation, func(t *testing.T) {
			c, db, _ := setup(t)
			created, err := c.Create(t.Context(), &api.CreateReq{Name: "host"})
			if err != nil {
				t.Fatal(err)
			}
			id := created.Agent.ID
			c.d.Guard = agentlink.NewEnrollmentGuardWithTimeout(10 * time.Millisecond)
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			go func() {
				done <- c.d.Guard.RunEnrollment(t.Context(), func() error { close(entered); <-release; return nil })
			}()
			<-entered
			switch operation {
			case "delete":
				_, err = c.Delete(t.Context(), &api.DeleteReq{AgentID: id})
			case "disable":
				_, err = c.Disable(t.Context(), &api.DisableReq{AgentID: id})
			case "enable":
				_, err = c.Enable(t.Context(), &api.EnableReq{AgentID: id})
			case "reset":
				_, err = c.ResetToken(t.Context(), &api.ResetTokenReq{AgentID: id})
			}
			close(release)
			if guardErr := <-done; guardErr != nil {
				t.Fatal(guardErr)
			}
			requireError(t, err, 500, "系统繁忙，请稍后重试")
			rec, err := db.GetAgent(t.Context(), id)
			if err != nil || rec.Disabled {
				t.Fatalf("revocation escaped guard: %+v %v", rec, err)
			}
			oldID, err := db.LookupAgentByTokenHash(t.Context(), agentlink.HashEnrollmentToken(created.Enrollment.Token))
			if err != nil || oldID != id {
				t.Fatalf("token rotated despite busy guard %q %v", oldID, err)
			}
		})
	}
}
