package httpx

import (
	groupsapi "arkade/hub/api/groups/v1"
	usersapi "arkade/hub/api/users/v1"
)

// Historical contract tests decode the same public API views as native controllers.
type userAdminView = usersapi.UserView
type groupView = groupsapi.GroupView
type groupMemberView = groupsapi.MemberView
type groupGrantView = groupsapi.GrantView
