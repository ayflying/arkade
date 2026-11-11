package groups

import (
	"context"
	"testing"

	api "arkade/hub/api/groups/v1"
	"arkade/hub/internal/httpwire"
	"arkade/hub/internal/user"
)

func TestStrictDecodedBodiesOverrideGFFieldsAndPreservePath(t *testing.T) {
	c, db, _ := setup(t)
	ctx := context.Background()
	createdBody := &api.CreateReq{Name: "strict-created", Description: "strict description"}
	created, err := c.Create(httpwire.WithDecodedBody(ctx, createdBody), &api.CreateReq{Name: "wrong", Description: "wrong"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Group.Name != createdBody.Name || created.Group.Description != createdBody.Description {
		t.Fatalf("create did not honor strict body: %+v", created.Group)
	}
	g := created.Group
	if err := db.CreateUser(ctx, user.User{ID: "strict-user", Username: "strict", Role: user.RoleUser}); err != nil {
		t.Fatal(err)
	}
	memberBody := &api.SetMembersReq{GroupID: "untrusted", UserIDs: []string{"strict-user"}}
	members, err := c.SetMembers(httpwire.WithDecodedBody(ctx, memberBody), &api.SetMembersReq{GroupID: g.ID, UserIDs: []string{"wrong"}})
	if err != nil {
		t.Fatal(err)
	}
	if members.GroupID != g.ID || len(members.Members) != 1 || members.Members[0].ID != "strict-user" {
		t.Fatalf("members lost strict values or framework identity: %+v", members)
	}
	if memberBody.GroupID != "untrusted" || memberBody.UserIDs[0] != "strict-user" {
		t.Fatal("mutated strict member body")
	}
	grantBody := &api.SetGrantsReq{GroupID: "untrusted", Grants: []api.GrantInput{{AgentID: "strict-agent", Permission: "manage"}}}
	grants, err := c.SetGrants(httpwire.WithDecodedBody(ctx, grantBody), &api.SetGrantsReq{GroupID: g.ID, Grants: []api.GrantInput{{AgentID: "wrong", Permission: "none"}}})
	if err != nil {
		t.Fatal(err)
	}
	if grants.GroupID != g.ID || len(grants.Grants) != 1 || grants.Grants[0].AgentID != "strict-agent" || grants.Grants[0].Permission != "manage" {
		t.Fatalf("grants lost strict values or framework identity: %+v", grants)
	}
	if grantBody.GroupID != "untrusted" || grantBody.Grants[0].AgentID != "strict-agent" {
		t.Fatal("mutated strict grant body")
	}
	updateBody := patch(t, "untrusted", `{"description":null}`)
	updated, err := c.Update(httpwire.WithDecodedBody(ctx, updateBody), patch(t, g.ID, `{"description":"wrong"}`))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Group.ID != g.ID || updated.Group.Description != "" {
		t.Fatalf("update strict override: %+v", updated.Group)
	}
	if updateBody.GroupID != "untrusted" {
		t.Fatal("mutated strict update body")
	}
}
