package audit

// Identity actions intentionally describe operations only, never credentials or claims.
const (
	ActionUserRegister   = "user.register"
	ActionProfileUpdate  = "account.profile_update"
	ActionPasswordChange = "account.password_change"
	ActionSettingsUpdate = "settings.update"
	ActionProviderCreate = "oidc.provider_create"
	ActionProviderUpdate = "oidc.provider_update"
	ActionProviderDelete = "oidc.provider_delete"
	ActionProviderTest   = "oidc.provider_test"
	ActionIdentityBind   = "identity.bind"
	ActionIdentityDelete = "identity.delete"
)

func init() {
	for action, label := range map[string]string{
		ActionUserRegister: "注册账户", ActionProfileUpdate: "更新个人资料", ActionPasswordChange: "修改个人密码",
		ActionSettingsUpdate: "修改系统设置", ActionProviderCreate: "创建身份提供方", ActionProviderUpdate: "修改身份提供方",
		ActionProviderDelete: "删除身份提供方", ActionProviderTest: "测试身份提供方", ActionIdentityBind: "绑定登录身份", ActionIdentityDelete: "解除登录身份",
	} {
		labels[action] = label
	}
}
