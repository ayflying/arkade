package httpx

import (
	"net/http"

	groupsapi "arkade/hub/api/groups/v1"
	usersapi "arkade/hub/api/users/v1"
	auditcontroller "arkade/hub/internal/controller/audit"
	groupscontroller "arkade/hub/internal/controller/groups"
	userscontroller "arkade/hub/internal/controller/users"
	"github.com/gogf/gf/v2/net/ghttp"
)

func bindManagementRoutes(s *ghttp.Server, d Deps) {
	session := newSessionAccessor()
	bindPrivateModule(s, d, userscontroller.New(d.DB, session, d.Log), func(r *ghttp.Request) any {
		switch r.Method {
		case http.MethodPost:
			return &usersapi.CreateReq{}
		case http.MethodPatch:
			return &usersapi.UpdateReq{}
		case http.MethodPut:
			return &usersapi.ResetPasswordReq{}
		}
		return nil
	})
	bindPrivateModule(s, d, groupscontroller.New(d.DB, session, d.Log), func(r *ghttp.Request) any {
		switch r.Method {
		case http.MethodPost:
			return &groupsapi.CreateReq{}
		case http.MethodPatch:
			return &groupsapi.UpdateReq{}
		case http.MethodPut:
			if r.Router.Uri == "/api/groups/{groupID}/members" {
				return &groupsapi.SetMembersReq{}
			}
			return &groupsapi.SetGrantsReq{}
		}
		return nil
	})
	bindPrivateModule(s, d, auditcontroller.New(d.DB, session, d.Log), nil)
}
