package config

import (
 "os"
 "path/filepath"
 "testing"
)

func TestIdentityConfigurationEnvironmentAndTOML(t *testing.T) {
 file:=filepath.Join(t.TempDir(),"hub.toml")
 if err:=os.WriteFile(file,[]byte("[auth]\nsettings_key_file = 'configured.key'\nallow_insecure_oidc_loopback = true\n"),0600);err!=nil{t.Fatal(err)}
 t.Setenv("ARKADE_SETTINGS_KEY_FILE","environment.key")
 t.Setenv("ARKADE_ALLOW_INSECURE_OIDC_LOOPBACK","false")
 cfg,err:=Load(file)
 if err!=nil{t.Fatal(err)}
 if cfg.Auth.SettingsKeyFile!="environment.key"||cfg.Auth.AllowInsecureOIDCLoopback{t.Fatalf("environment did not override identity config: key=%s insecure=%v",cfg.Auth.SettingsKeyFile,cfg.Auth.AllowInsecureOIDCLoopback)}
 t.Setenv("ARKADE_SETTINGS_KEY_FILE","")
 t.Setenv("ARKADE_ALLOW_INSECURE_OIDC_LOOPBACK","")
 cfg,err=Load(file)
 if err!=nil||cfg.Auth.SettingsKeyFile!="configured.key"||!cfg.Auth.AllowInsecureOIDCLoopback{t.Fatalf("TOML identity settings not recognized: %v",err)}
}
