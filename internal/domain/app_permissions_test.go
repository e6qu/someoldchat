package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestAppPermissionCheckHoldsTheAccessControlInvariant(t *testing.T) {
	valid := AppPermission{AppID: "A1", WorkspaceID: "T1", PermissionType: AppPermissionEveryone}
	for _, testCase := range []struct {
		name   string
		change func(*AppPermission)
		want   error
	}{
		{"everyone", func(*AppPermission) {}, nil},
		{"named list", func(value *AppPermission) {
			value.PermissionType, value.UserGroupIDs = AppPermissionNamedEntities, []UserGroupID{"S1"}
		}, nil},
		{"allowlist", func(value *AppPermission) {
			value.ChannelRestrictionMode, value.ChannelIDs = ChannelRestrictionSpecificChannels, []ConversationID{"C1"}
		}, nil},
		{"no app", func(value *AppPermission) { value.AppID = "" }, ErrInvalidAppPermission},
		{"unknown type", func(value *AppPermission) { value.PermissionType = "whoever" }, ErrAppPermissionType},
		{"entities without named_entities", func(value *AppPermission) { value.UserIDs = []UserID{"U1"} }, ErrAppPermissionType},
		{"named_entities naming nobody", func(value *AppPermission) { value.PermissionType = AppPermissionNamedEntities }, ErrNamedEntitiesEmpty},
		{"unknown mode", func(value *AppPermission) { value.ChannelRestrictionMode = "nowhere" }, ErrChannelRestrictionMode},
		{"channels under all_channels", func(value *AppPermission) {
			value.ChannelRestrictionMode, value.ChannelIDs = ChannelRestrictionAllChannels, []ConversationID{"C1"}
		}, ErrChannelRestrictionMode},
		{"restriction on no_one", func(value *AppPermission) {
			value.PermissionType, value.ChannelRestrictionMode = AppPermissionNoOne, ChannelRestrictionAllChannels
		}, ErrChannelRestrictionRequiresAppAccess},
		{"too many users", func(value *AppPermission) {
			value.PermissionType = AppPermissionNamedEntities
			for index := 0; index <= MaxAppPermissionSetUsers; index++ {
				value.UserIDs = append(value.UserIDs, UserID("U"+strings.Repeat("1", index+1)))
			}
		}, ErrTooManyNamedEntities},
	} {
		value := valid
		testCase.change(&value)
		if err := value.Check(); !errors.Is(err, testCase.want) || (testCase.want == nil && err != nil) {
			t.Errorf("%s: Check()=%v, want %v", testCase.name, err, testCase.want)
		}
	}
}

func TestAnMCPServerRuleMayOnlyNarrowItsApp(t *testing.T) {
	for _, testCase := range []struct {
		server MCPServerPermissionType
		app    AppPermissionType
		want   bool
	}{
		{MCPServerPermissionEveryone, AppPermissionEveryone, false},
		{MCPServerPermissionNamedEntitiesExclude, AppPermissionEveryone, false},
		{MCPServerPermissionEveryone, AppPermissionNamedEntities, true},
		{MCPServerPermissionNamedEntitiesExclude, AppPermissionNamedEntities, true},
		{MCPServerPermissionNamedEntities, AppPermissionNamedEntities, false},
		{MCPServerPermissionNoOne, AppPermissionNamedEntities, false},
		{MCPServerPermissionNamedEntities, AppPermissionNoOne, true},
		{MCPServerPermissionNoOne, AppPermissionNoOne, false},
	} {
		if got := testCase.server.BroaderThan(testCase.app); got != testCase.want {
			t.Errorf("%s.BroaderThan(%s)=%t, want %t", testCase.server, testCase.app, got, testCase.want)
		}
	}
}

// An MCP server's identifier is what its permission is stored against, so it
// must be the same wherever and whenever it is derived, and differ between
// servers and between apps.
func TestMCPServerIDIsStableAndDistinct(t *testing.T) {
	first := NewMCPServerID("A1", "search")
	if first != NewMCPServerID("A1", "search") {
		t.Fatal("the identifier is not stable")
	}
	if !strings.HasPrefix(string(first), "Amcp") || strings.ToUpper(string(first[4:])) != string(first[4:]) {
		t.Fatalf("identifier %q is not an upper-case Amcp identifier", first)
	}
	if first == NewMCPServerID("A1", "tickets") || first == NewMCPServerID("A2", "search") {
		t.Fatal("two servers share an identifier")
	}
}
