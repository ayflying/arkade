package stackmigrate

import "context"

// Backend is the migration boundary. Source operations must remain read-only.
// ImportCompose MUST atomically reject a target project that appeared after the
// last StackExists check. No manager-side check can eliminate that TOCTOU race.
// Mutating calls must return ResidualError if they fail after a known side effect.
// Archive cleanup belongs to the backend and may delete only its own temporary
// transfer files, never source data or destination user volumes/images/projects.
type Backend interface {
	CanManage(ctx context.Context, agentID, stackName string) (bool, error)
	AgentOnline(agentID string) bool
	StackExists(ctx context.Context, agentID, stackName string) (bool, error)
	StackBusy(ctx context.Context, agentID, stackName string) (bool, error)
	ReadCompose(ctx context.Context, agentID, stackName string) (config, env, hash string, err error)
	ImagesOf(ctx context.Context, agentID, stackName string) ([]string, error)
	CopyImage(ctx context.Context, source, target, reference string, progress func(done, total int64)) error
	VolumesOf(ctx context.Context, agentID, stackName string) (VolumePlan, error)
	CopyVolume(ctx context.Context, source, target, volume string, progress func(done, total int64)) error
	ImportCompose(ctx context.Context, agentID, stackName, config, env, hash string) error
	StartStack(ctx context.Context, agentID, stackName string) error
}

type VolumePlan struct {
	Volumes   []string
	BindDirs  int
	Anonymous int
	External  []string
}

type Options struct {
	StackName      string
	Source         string
	Target         string
	IncludeVolumes bool
	StartAfter     bool
	RequestID      string
	OwnerID        string
}

type ownerContextKey struct{}

func WithOwner(ctx context.Context, owner string) context.Context {
	return context.WithValue(ctx, ownerContextKey{}, owner)
}

// OwnerIDFromContext exposes the stable submitter identity to backend permission
// checks without preserving cancellation/deadlines from a short HTTP request.
func OwnerIDFromContext(ctx context.Context) string {
	owner, _ := ctx.Value(ownerContextKey{}).(string)
	return owner
}

// ResidualError reports effects confirmed by a failed backend call. Counts are
// increments for that one call, not the total task count. Unknown/partial effects
// should be described in Err; never claim they were rolled back.
type ResidualError struct {
	Err      error
	Residual Residual
}

func (e *ResidualError) Error() string {
	if e.Err == nil {
		return "迁移操作失败但目标端已有改动"
	}
	return e.Err.Error()
}
func (e *ResidualError) Unwrap() error { return e.Err }
