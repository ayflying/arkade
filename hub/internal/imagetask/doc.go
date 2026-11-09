// Package imagetask manages run-local image operations. The HTTP layer must check
// Task.OwnerID before Get, Cancel or Download; the manager performs no authorization.
// Backend implementations must honor context cancellation and accept an empty
// BeginLoad digest for uploads. CommitLoad receives the final SHA-256 digest.
//
// Exports remain on the source agent for at most 30 minutes from task creation.
// A complete, checksum-verified Download consumes and releases the archive once.
// Failed or partial downloads retain it for retry until expiry, cancellation,
// manager shutdown, or bounded-history eviction; concurrent downloads are rejected.
// Migration releases temporary archives on both agents but never deletes the source image. Cancellation
// stops further work; it cannot roll back an import that the agent already completed.
package imagetask
