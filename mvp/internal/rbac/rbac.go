// Package rbac implements the token-based authenticator and RBAC
// authorizer used by the mk-apiserver middleware.
//
// This mirrors Kubernetes RBAC:
//
//   Authorization: Bearer <token>   ──►  User (+ groups)
//   User/Group                      ──►  RoleBinding / ClusterRoleBinding
//                                   ──►  Role / ClusterRole
//                                   ──►  PolicyRule (verbs × resources × apiGroups)
//
// The matcher is intentionally simple: literal strings plus "*" wildcards.
package rbac

import (
	"net/http"
	"strings"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/store"
)

// Request is a single authorization decision input.
type Request struct {
	User      *api.User
	Verb      string // get|list|create|update|delete|watch
	APIGroup  string // "" (core) | "apps" | "rbac.mk.io" | <crd group>
	Resource  string // pods|services|deployments|...|<crd plural>
	Namespace string
}

// SystemUser is the identity used for in-process controllers and when
// auth is disabled — it bypasses authorization.
var SystemUser = &api.User{
	Metadata: api.Metadata{Name: "system:admin"},
	Groups:   []string{"system:masters"},
}

// Authenticator resolves a bearer token to a User via the store.
type Authenticator struct {
	Store *store.Store
	// AnonymousAllowed controls whether requests without a token are
	// permitted (for M4 demo backward-compat). When true, a missing
	// Authorization header returns SystemUser rather than 401.
	AnonymousAllowed bool
}

// Authenticate extracts the bearer token and returns the matching user,
// SystemUser when anonymous, or nil+false when the token is invalid.
func (a *Authenticator) Authenticate(r *http.Request) (*api.User, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		if a.AnonymousAllowed {
			return SystemUser, true
		}
		return nil, false
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return nil, false
	}
	tok := strings.TrimSpace(h[len(prefix):])
	if tok == "" {
		return nil, false
	}
	u, err := a.Store.GetUserByToken(tok)
	if err != nil {
		return nil, false
	}
	return u, true
}

// Authorizer runs RBAC checks against the store-backed roles/bindings.
type Authorizer struct {
	Store *store.Store
}

// Authorize returns nil if the request is allowed, or an error describing
// the denial. system:masters is unconditionally allowed (like kubernetes).
func (az *Authorizer) Authorize(req Request) error {
	if req.User == nil {
		return errDenied("no user")
	}
	// system:masters bypass.
	for _, g := range req.User.Groups {
		if g == "system:masters" {
			return nil
		}
	}

	// Collect rules from ClusterRoleBindings that match the user.
	if ok, err := az.checkBindings("ClusterRoleBinding", req); err != nil {
		return err
	} else if ok {
		return nil
	}
	// Then RoleBindings in the request's namespace.
	if ok, err := az.checkBindings("RoleBinding", req); err != nil {
		return err
	} else if ok {
		return nil
	}
	return errDenied("user=" + req.User.Metadata.Name + " cannot " + req.Verb + " " + req.Resource + " in apiGroup=" + nz(req.APIGroup, "core"))
}

func (az *Authorizer) checkBindings(scope string, req Request) (bool, error) {
	bindings, err := az.Store.ListRoleBindings(scope)
	if err != nil {
		return false, err
	}
	for _, b := range bindings {
		// namespace scoping for RoleBindings
		if scope == "RoleBinding" {
			bns := b.Metadata.Namespace
			if bns == "" {
				bns = "default"
			}
			rns := req.Namespace
			if rns == "" {
				rns = "default"
			}
			if bns != rns {
				continue
			}
		}
		if !subjectMatches(b.Subjects, req.User) {
			continue
		}
		roleScope := b.RoleRef.Kind // Role|ClusterRole
		role, err := az.Store.GetRole(roleScope, b.RoleRef.Name)
		if err != nil {
			continue
		}
		for _, rule := range role.Rules {
			if ruleMatches(rule, req) {
				return true, nil
			}
		}
	}
	return false, nil
}

func subjectMatches(subs []api.Subject, u *api.User) bool {
	for _, s := range subs {
		switch s.Kind {
		case "User":
			if s.Name == u.Metadata.Name {
				return true
			}
		case "Group":
			for _, g := range u.Groups {
				if g == s.Name {
					return true
				}
			}
		}
	}
	return false
}

func ruleMatches(r api.PolicyRule, req Request) bool {
	return listContains(r.Verbs, req.Verb) &&
		listContains(r.APIGroups, req.APIGroup) &&
		listContains(r.Resources, req.Resource)
}

func listContains(list []string, v string) bool {
	for _, x := range list {
		if x == "*" || x == v {
			return true
		}
	}
	return false
}

type deniedErr struct{ msg string }

func (e *deniedErr) Error() string { return e.msg }

func errDenied(m string) error { return &deniedErr{"forbidden: " + m} }

// IsDenied reports whether err was a denial (for 403 mapping).
func IsDenied(err error) bool {
	_, ok := err.(*deniedErr)
	return ok
}

func nz(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
