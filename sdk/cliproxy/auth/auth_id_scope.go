package auth

import (
	"context"
	"fmt"
	"path"
)

// AuthIDScope is the set of credentials a request may use, matched against
// Auth.ID with path.Match patterns. A nil scope allows every credential; a
// non-nil scope without patterns allows none.
type AuthIDScope struct {
	patterns []string
}

// NewAuthIDScope builds a scope from path.Match patterns. Malformed patterns
// are skipped and reported, so they can only narrow the scope.
func NewAuthIDScope(patterns []string) (*AuthIDScope, []error) {
	scope := &AuthIDScope{patterns: make([]string, 0, len(patterns))}
	var errs []error
	for _, pattern := range patterns {
		if _, errMatch := path.Match(pattern, ""); errMatch != nil {
			errs = append(errs, fmt.Errorf("credential pattern %q: %w", pattern, errMatch))
			continue
		}
		scope.patterns = append(scope.patterns, pattern)
	}
	return scope, errs
}

// Allows reports whether the credential with authID is inside the scope.
func (s *AuthIDScope) Allows(authID string) bool {
	if s == nil {
		return true
	}
	for _, pattern := range s.patterns {
		if matched, _ := path.Match(pattern, authID); matched {
			return true
		}
	}
	return false
}

type authIDScopeContextKey struct{}

// WithAuthIDScope limits credential selection for requests using ctx to scope.
func WithAuthIDScope(ctx context.Context, scope *AuthIDScope) context.Context {
	if scope == nil {
		return ctx
	}
	return context.WithValue(ctx, authIDScopeContextKey{}, scope)
}

// AuthIDScopeFromContext returns the scope attached by WithAuthIDScope, or nil
// when the request may use every credential.
func AuthIDScopeFromContext(ctx context.Context) *AuthIDScope {
	if ctx == nil {
		return nil
	}
	scope, _ := ctx.Value(authIDScopeContextKey{}).(*AuthIDScope)
	return scope
}
