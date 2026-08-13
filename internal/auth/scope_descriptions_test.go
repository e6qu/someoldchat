package auth

import "testing"

// TestEveryGrantableScopeIsExplained is the gate that keeps the install-consent
// screen from ever showing a bare token: every scope the deployment can grant
// must carry a human description. A new scope added to allScopes without one
// fails here rather than reaching a person authorizing an app.
func TestEveryGrantableScopeIsExplained(t *testing.T) {
	for _, scope := range allScopes {
		if scope.Description() == "" {
			t.Errorf("scope %q has no consent-screen description", scope)
		}
	}
}

// TestScopeDescriptionsAreScopedToGrantableScopes keeps the table honest in the
// other direction: a description for a scope the deployment cannot grant is dead
// weight that outlives the scope it described, the same way admin.emoji:write
// once outlived its caller in allScopes.
func TestScopeDescriptionsAreScopedToGrantableScopes(t *testing.T) {
	grantable := make(map[Scope]struct{}, len(allScopes))
	for _, scope := range allScopes {
		grantable[scope] = struct{}{}
	}
	for scope := range scopeDescriptions {
		if _, ok := grantable[scope]; !ok {
			t.Errorf("description for %q, which is not a grantable scope", scope)
		}
	}
}

func TestScopeDescriptionIsEmptyForAnUnknownScope(t *testing.T) {
	if got := Scope("not:a-real-scope").Description(); got != "" {
		t.Fatalf("unknown scope described as %q, want empty so the caller shows the token", got)
	}
}
