// Package policy holds the gate that asks, of every policy this product stores,
// who reads it back to decide something.
//
// Seven functional holes were found by hand in one session and every one had
// the same shape: an administrator sets a policy, a surface reports it back,
// and nothing consults it. Restricting an app changed nothing. An invitation
// could be approved a month after it lapsed. A guest account never deactivated.
// Each was a field the product wrote, displayed and carried across the seam,
// with no reader that enforced it.
//
// Finding those by hand does not scale and does not repeat. This gate makes the
// question structural: a policy-shaped store reader must declare where it is
// enforced, or be recorded as unenforced with a reason, and the unenforced set
// may only shrink.
package policy

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// enforcement is where a policy is read back to decide something. The file is
// checked to exist and to mention the reader, so a rename or a deletion breaks
// this gate instead of quietly turning a policy back into decoration.
type enforcement struct {
	// at is the file that consults the policy.
	at string
	// why says what the policy decides there, in the product's terms.
	why string
}

// enforced maps each policy-shaped store reader to the place that acts on it.
func enforced() map[string]enforcement {
	return map[string]enforcement{
		"GetAppApproval":                         {at: "internal/service/messages.go", why: "a decided request cannot be cancelled, and restriction uninstalls the app"},
		"GetAutomationPermission":                {at: "internal/service/workflows.go", why: "who may run a workflow, a trigger and a function"},
		"GetConversationRetention":               {at: "internal/scheduler/retention.go", why: "the sweep deletes against the conversation's own horizon"},
		"GetRetentionPolicy":                     {at: "internal/scheduler/retention.go", why: "the sweep deletes against the workspace horizon where no override governs"},
		"GetUserExpiration":                      {at: "internal/scheduler/user_expirations.go", why: "a lapsed account is deactivated, and its credentials are refused at lookup"},
		"GetWorkspaceNotificationPreferences":    {at: "internal/service/messages.go", why: "whether a message notifies, and the schedule it may notify within"},
		"GetConversationNotificationPreferences": {at: "internal/store/sqlstore/sqlstore.go", why: "the store joins on the member's level when it decides what to raise"},
		"ListBarriers":                           {at: "internal/service/messages.go", why: "barrierSeparates refuses to put two separated people together"},
		"ListConversationTeams":                  {at: "internal/service/connect.go", why: "which organizations are in a shared channel, and who may invite another"},
		"ListUsersByRole":                        {at: "internal/service/messages.go", why: "refuseLastOwnerChange will not leave a workspace ownerless"},
		"ListSessionSettings":                    {at: "internal/service/messages.go", why: "MemberSessionSettings gives both session-minting paths how long the session they are about to issue may live"},
	}
}

// display are readers whose value is shown and is not meant to decide anything:
// an administrative listing, or a member's own presentation choice. They are
// declared rather than skipped, so nobody has to guess whether a missing
// enforcement site is an omission or the point.
func display() map[string]string {
	return map[string]string{
		"GetActivityPreferences": "a member's Activity layout, which is theirs to choose and governs nobody",
		"ListAppApprovals":       "the administrative list of decisions; GetAppApproval is what acts on one",
		"ListExternalTeams":      "the organizations a workspace is connected to, reported for review",
		"ListRoleAssignments":    "who holds which role, reported for review; the roles themselves gate through membership",
	}
}

// unenforced are policies this product records and does not apply. Each says
// why it is not closed. The ceiling below only shrinks.
func unenforced() map[string]string {
	return map[string]string{
		"GetConversationPrefs": "who_can_post and can_thread are stored and reported and read by nothing: an administrator " +
			"restricting a channel to admins changes nothing and any member still posts. Enforcing it needs a vocabulary and " +
			"the pinned reference does not supply one — it types the field as an unconstrained array of string — so a wrong " +
			"guess denies legitimate posts, which is worse than the present permissiveness.",
		"ListAuthPolicyEntities": "admin.auth.policy.assignEntities records entities against a policy that no sign-in path " +
			"consults. Same undefined vocabulary as channel posting policy, and here a wrong guess denies sign-in.",
	}
}

const unenforcedCeiling = 2

// policyShaped is how a policy reader is recognised. It is a name test, which
// is a real limit: a policy whose reader is named outside this vocabulary is
// invisible here. The limit is stated rather than pretended away, and the
// pattern is widened when a policy arrives that it misses.
var policyShaped = regexp.MustCompile(`Pref|Polic|Expir|Approv|Retention|Barrier|Permission|Restrict|Setting|Teams|Role|Scope`)

func TestEveryPolicyReaderIsClassified(t *testing.T) {
	readers := policyReaders(t)
	if len(readers) == 0 {
		t.Fatal("no policy-shaped store readers found; the port moved or the pattern stopped matching")
	}
	for _, name := range readers {
		_, isEnforced := enforced()[name]
		_, isDisplay := display()[name]
		_, isUnenforced := unenforced()[name]
		switch {
		case isEnforced && (isDisplay || isUnenforced), isDisplay && isUnenforced:
			t.Errorf("%s is classified twice; a policy is enforced, shown, or unapplied, not two of those", name)
		case !isEnforced && !isDisplay && !isUnenforced:
			t.Errorf("store.%s reads a policy nobody has classified. Say where it is enforced, that it is only shown, or that it is recorded and never applied — an unclassified policy is how seven of them came to decide nothing.", name)
		}
	}
	known := make(map[string]struct{}, len(readers))
	for _, name := range readers {
		known[name] = struct{}{}
	}
	for _, set := range []map[string]string{display(), unenforced()} {
		for name := range set {
			if _, ok := known[name]; !ok {
				t.Errorf("%s is classified but the store no longer declares it", name)
			}
		}
	}
	for name := range enforced() {
		if _, ok := known[name]; !ok {
			t.Errorf("%s is classified but the store no longer declares it", name)
		}
	}
}

// TestEveryEnforcementSiteExistsAndReadsItsPolicy stops a declaration outliving
// the code it points at. A file that no longer mentions the reader is a policy
// whose enforcement was deleted or renamed, which is the state this gate exists
// to make loud.
func TestEveryEnforcementSiteExistsAndReadsItsPolicy(t *testing.T) {
	for name, site := range enforced() {
		body, err := os.ReadFile(filepath.Join("..", "..", site.at))
		if err != nil {
			t.Errorf("%s names %s, which cannot be read: %v", name, site.at, err)
			continue
		}
		if !strings.Contains(string(body), name+"(") {
			t.Errorf("%s names %s as its enforcement, and that file does not call it. Either the enforcement moved, or it is gone and the policy now decides nothing.", name, site.at)
		}
		if strings.TrimSpace(site.why) == "" {
			t.Errorf("%s declares an enforcement site with no statement of what it decides", name)
		}
	}
}

func TestTheUnenforcedSetOnlyShrinks(t *testing.T) {
	if len(unenforced()) > unenforcedCeiling {
		t.Fatalf("%d policies are recorded and never applied, above the ceiling of %d", len(unenforced()), unenforcedCeiling)
	}
	if len(unenforced()) < unenforcedCeiling {
		t.Fatalf("only %d policies are unapplied now: lower unenforcedCeiling to match, so the ground gained is kept", len(unenforced()))
	}
	for name, reason := range unenforced() {
		if len(strings.TrimSpace(reason)) < 40 {
			t.Errorf("%s is recorded as unapplied without saying why; a policy that decides nothing needs a reason somebody can act on", name)
		}
	}
}

// policyReaders reads the store port rather than a list, so a policy arriving
// with a reader nobody classified fails above instead of passing unseen.
func policyReaders(t *testing.T) []string {
	t.Helper()
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, filepath.Join("..", "..", "internal", "store", "store.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]struct{})
	ast.Inspect(parsed, func(node ast.Node) bool {
		iface, ok := node.(*ast.InterfaceType)
		if !ok {
			return true
		}
		for _, method := range iface.Methods.List {
			for _, name := range method.Names {
				if !strings.HasPrefix(name.Name, "Get") && !strings.HasPrefix(name.Name, "List") && !strings.HasPrefix(name.Name, "Lookup") {
					continue
				}
				if policyShaped.MatchString(name.Name) {
					seen[name.Name] = struct{}{}
				}
			}
		}
		return true
	})
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
