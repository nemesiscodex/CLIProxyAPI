package access

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// KeyCredentialScopes maps client API keys to the upstream credentials they
// may use. Keys without an entry are unrestricted.
type KeyCredentialScopes struct {
	byKey map[string]*coreauth.AuthIDScope
}

// NewKeyCredentialScopes compiles access.key-credentials. Entries for keys not
// listed in access.api-keys are ignored, and malformed patterns are dropped;
// both are logged without revealing the key.
func NewKeyCredentialScopes(cfg *config.SDKConfig) *KeyCredentialScopes {
	scopes := &KeyCredentialScopes{byKey: make(map[string]*coreauth.AuthIDScope)}
	if cfg == nil || len(cfg.KeyCredentials) == 0 {
		return scopes
	}
	known := make(map[string]struct{}, len(cfg.APIKeys))
	for _, key := range cfg.APIKeys {
		known[strings.TrimSpace(key)] = struct{}{}
	}
	patternsByKey := make(map[string][]string, len(cfg.KeyCredentials))
	for _, entry := range cfg.KeyCredentials {
		key := strings.TrimSpace(entry.Key)
		if _, ok := known[key]; !ok {
			log.Warnf("access.key-credentials: ignoring entry for key %s, which is not in access.api-keys", keyFingerprint(key))
			continue
		}
		patternsByKey[key] = append(patternsByKey[key], entry.Credentials...)
	}
	for key, patterns := range patternsByKey {
		scope, errs := coreauth.NewAuthIDScope(patterns)
		for _, errPattern := range errs {
			log.Warnf("access.key-credentials: key %s: skipping %v", keyFingerprint(key), errPattern)
		}
		scopes.byKey[key] = scope
	}
	return scopes
}

// ScopeFor returns the credential scope of a client key, or nil when the key
// may use every credential.
func (s *KeyCredentialScopes) ScopeFor(key string) *coreauth.AuthIDScope {
	if s == nil {
		return nil
	}
	return s.byKey[strings.TrimSpace(key)]
}

func keyFingerprint(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "sha256:" + hex.EncodeToString(sum[:4])
}
