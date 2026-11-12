package httpx

// Initial credentials are sufficient only to identify the user and replace the
// temporary password. No resource/settings/identity API is available meanwhile.
func passwordChangeAllowed(path string) bool {
	switch path {
	case "/api/auth/me", "/api/auth/logout", "/api/account", "/api/account/password":
		return true
	default:
		return false
	}
}
