package httpx

import (
	agentsapi "arkade/hub/api/agents/v1"
	authapi "arkade/hub/api/auth/v1"
	stacksapi "arkade/hub/api/stacks/v1"
	resourcescontroller "arkade/hub/internal/controller/resources"
	stackscontroller "arkade/hub/internal/controller/stacks"
	compose "arkade/hub/internal/service/compose"
)

// Existing black-box regressions keep their historical names while exercising
// the sole native implementation and public contract, not a duplicate handler.
const maxVolumeFileSize = resourcescontroller.MaxVolumeFileSize

var validVolumeRequest = resourcescontroller.ValidVolumeRequest
var validVolumePath = resourcescontroller.ValidVolumePath

const composeProjectStatusOffline = "offline"
const codeConflict = "conflict"
const composeHashHexLen = 16
const maxComposeOutputRunes = 4000

type userEnvelope = authapi.MeRes
type agentView = agentsapi.AgentView
type composeStackView = stacksapi.StackView

var validComposeName = compose.ValidName
var composeConfigHash = compose.ConfigHash
var composeProjectHash = compose.ProjectHash
var truncateComposeOutput = compose.TruncateOutput
var toStackView = compose.StackViewOf
var migrateTaskView = stackscontroller.MigrateTaskView
