package httpx

import (
 "context"
 "log/slog"
 "testing"

 "arkade/hub/internal/auth"
 "arkade/hub/internal/config"
 "arkade/hub/internal/store"
)
func testIdentityServices(t *testing.T,db *store.DB,svc *auth.Service,path string,log *slog.Logger)*identityServices {
 t.Helper()
 cfg:=config.Default()
 cfg.Database.Path=path
 cfg.Server.BaseURL="http://127.0.0.1:8080"
 identity,err:=newIdentityServices(context.Background(),db,svc,cfg,log)
 if err!=nil{t.Fatal(err)}
 return identity
}
