package grantflow

// Env var names every app using grantflow reads, so the UI and MCP halves
// of an app are configured identically.
const (
	EnvClientID      = "GRANT_CLIENT_ID"
	EnvClientSecret  = "GRANT_CLIENT_SECRET"
	EnvRedirectURI   = "GRANT_REDIRECT_URI"
	EnvEncryptionKey = "GRANT_ENCRYPTION_KEY"
)

// ConfigFromEnv reads Config from the Env* variables via getenv (os.Getenv).
// ok is false when none are set, so an app can keep running without it.
func ConfigFromEnv(getenv func(string) string, issuer string) (cfg Config, ok bool) {
	cfg = Config{
		Issuer:              issuer,
		ClientID:            getenv(EnvClientID),
		ClientSecret:        getenv(EnvClientSecret),
		RedirectURI:         getenv(EnvRedirectURI),
		EncryptionKeySecret: getenv(EnvEncryptionKey),
	}
	return cfg, cfg.ClientID != "" || cfg.ClientSecret != "" || cfg.RedirectURI != "" || cfg.EncryptionKeySecret != ""
}
