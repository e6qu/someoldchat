// Package authorization: the operations whose refusal cannot be told apart from
// a missing object.
//
// An operation refuses a caller who may not act with store.ErrNotFound, because
// Slack declines to confirm that something a caller cannot see exists. It also
// refuses a caller who MAY act with store.ErrNotFound when the thing genuinely
// is not there. From one answer the two are the same, so a matrix that accepts
// "not found" as enforcement is accepting evidence that is equally consistent
// with no enforcement at all.
//
// That is not a hypothetical. The guard-mutation gate in tests/mutation strips
// every guard standing in front of an operation and asks whether any suite
// notices; ninety seam methods stayed green, and this is why. The matrix ran
// them, they ran on past the deleted guard to their own "not found", and the
// matrix counted that as a refusal about standing.
//
// The fix is a fixture rich enough for each operation to find its object, so
// that "not found" means "not for you" again. That is real work per operation —
// a canvas, a list, a reminder, a file, a call, a huddle, each with the
// membership that makes it visible — and it is what shrinks this list. Until an
// operation gets one, it is named here rather than counted as covered.
//
// The ceiling only shrinks.
//
// AcceptSharedInvite and DeclineSharedInvite are a structural residue rather
// than a fixture gap: they are the invited organization's action, refused by a
// caller who is not in the target workspace, and every member this fixture holds
// is in the host. Closing them needs a target-workspace member, which is the
// same second-organization the browser suite cannot arrange either. They are
// named here rather than driven.
package authorization

func refusalDoesNotDistinguishTheHolder() map[string]struct{} {
	return map[string]struct{}{
		"AcceptSharedInvite":                      {},
		"AddBookmark":                             {},
		"StartExternalAuthConnection":             {},
		"DeleteWorkspaceProfileField":             {},
		"SaveListAsTemplate":                      {},
		"CreateListFromTemplate":                  {},
		"DeleteListTemplate":                      {},
		"AddListColumn":                           {},
		"AddReaction":                             {},
		"AddUserGroupChannels":                    {},
		"AdminAddConversationAccessGroup":         {},
		"AdminAddUserGroupTeams":                  {},
		"AdminClearAppResolution":                 {},
		"AdminCreateIncomingWebhook":              {},
		"PublishView":                             {},
		"AdminCancelAppRequest":                   {},
		"AdminConvertConversationToPublic":        {},
		"AdminInviteConversationMembers":          {},
		"AdminListConversationAccessGroups":       {},
		"AdminRemoveConversationAccessGroup":      {},
		"AdminSetConversationArchived":            {},
		"AdminSetConversationTeams":               {},
		"AdminSetIncomingWebhookEnabled":          {},
		"AppHome":                                 {},
		"AssignListItem":                          {},
		"AssistantThread":                         {},
		"CloseView":                               {},
		"CommentOnCanvas":                         {},
		"CommentOnListItem":                       {},
		"CompleteFunction":                        {},
		"CompleteWorkflowButton":                  {},
		"ConversationCanvas":                      {},
		"ConvertGroupDirectToPrivate":             {},
		"CurrentModalView":                        {},
		"DeclineSharedInvite":                     {},
		"DeleteCanvas":                            {},
		"DeleteCanvasAccess":                      {},
		"DeleteCanvasComment":                     {},
		"DeleteActivitySavedView":                 {},
		"DeleteSidebarSection":                    {},
		"SetSidebarSectionCollapsed":              {},
		"DeleteListItemComment":                   {},
		"AttachFileToListItem":                    {},
		"DetachFileFromListItem":                  {},
		"DeleteListAccess":                        {},
		"DeleteListItems":                         {},
		"DeleteScheduledUserStatus":               {},
		"DeleteWorkflow":                          {},
		"DiscardWorkflowStagedChanges":            {},
		"DispatchAppShortcut":                     {},
		"DispatchSlashCommand":                    {},
		"Draft":                                   {},
		"EditCanvas":                              {},
		"GetDeveloperApp":                         {},
		"GetDeveloperAppDeliveryHealth":           {},
		"GetFunctionPermission":                   {},
		"GetListDownload":                         {},
		"GetListItem":                             {},
		"IssueDeveloperAppToken":                  {},
		"RevokeDeveloperAppTokens":                {},
		"ListDeveloperAppTokens":                  {},
		"RevokeDeveloperAppToken":                 {},
		"ListEphemeralMessages":                   {},
		"LookupCanvasSections":                    {},
		"OpenAppHome":                             {},
		"PostEphemeral":                           {},
		"PostEphemeralWithBlocks":                 {},
		"PostEphemeralWithBlocksAndAttachments":   {},
		"RemoveListColumn":                        {},
		"RemovePin":                               {},
		"RemoveReaction":                          {},
		"RemoveSavedItem":                         {},
		"RemoveStar":                              {},
		"RemoveUserGroupChannels":                 {},
		"RenameConversation":                      {},
		"RestoreCanvasRevision":                   {},
		"RunWorkflow":                             {},
		"SavedItemForMessage":                     {},
		"SaveDraft":                               {},
		"SaveDraftWithAttachments":                {},
		"ScheduleMessage":                         {},
		"ScheduleMessageAs":                       {},
		"ScheduleMessageWithBlocks":               {},
		"ScheduleMessageWithBlocksAndAttachments": {},
		"SetAppIcon":                              {},
		"SetCanvasAccess":                         {},
		"SetConversationArchived":                 {},
		"SetConversationNotificationPreferences":  {},
		"SetConversationRetention":                {},
		"SetFunctionPermission":                   {},
		"SetListAccess":                           {},
		"SetTriggerPermission":                    {},
		"SetUserRole":                             {},
		"SetWorkflowTrigger":                      {},
		"SubmitView":                              {},
		"SubmitWorkflowForm":                      {},
		"UpdateCall":                              {},
		"UpdateLaterReminder":                     {},
		"UpdateListCells":                         {},
		"UpdateListItem":                          {},
		"UpdateWorkflow":                          {},
		"UserByEmail":                             {},
		"WebhookTriggerURL":                       {},
	}
}

// indistinguishableRefusalCeiling is how many operations still answer a caller
// who holds the authority exactly as they answer one who does not.
// RevokeDeveloperAppTokens joins its sibling IssueDeveloperAppToken here for the
// same structural reason: the fixture's one app is owned by U-member, and the
// matrix's holder is U-owner, so the owner-only operation refuses the holder and
// a stranger alike with not-found. Driving it would mean re-owning the fixture
// app, which several other cases depend on. This is a new operation joining the
// set, not existing ground lost.
//
// CommentOnListItem and DeleteListItemComment join for the same reasons their
// canvas siblings did: commenting and deleting a comment answer a holder who
// cannot reach the fixture's list exactly as they answer a stranger, and the
// author-only delete rule lives in the store's write rather than the service.
// ListItemComments, like CanvasComments, is not here — a holder with list access
// reads an empty page where a stranger is refused, so its refusal does
// distinguish.
//
// AttachFileToListItem and DetachFileFromListItem join for the same structural
// reason: with no attachment on the fixture's item, both answer the holder and a
// stranger with the same not-found, since the missing object is reached before
// any standing check. ListItemFiles is not here — like ListItemComments a holder
// reads an empty list where a stranger is refused, so its refusal distinguishes.
//
// ListDeveloperAppTokens and RevokeDeveloperAppToken join their siblings
// IssueDeveloperAppToken and RevokeDeveloperAppTokens: the fixture's app is owned
// by U-member while the matrix holder is U-owner, so an owner-only developer-app
// operation refuses the holder and a stranger alike with not-found. Unlike the
// bulk sibling that lists distinguish here — a holder listing an app they do not
// own is refused, not shown an empty list — because the not-found is reached at
// the ownership check, before any token is read.
//
// DeleteActivitySavedView joins because the fixture has no saved view: deleting
// one answers a member who holds none exactly as it answers a stranger, both
// not-found, since the missing view is reached before any standing check.
// CreateActivitySavedView is not here — a member creates their own view where a
// stranger is refused, so its outcome distinguishes.
// DeleteSidebarSection and SetSidebarSectionCollapsed join for the same reason
// their saved-view sibling did: with no section on the fixture, both answer a
// member who holds none exactly as they answer a stranger, not-found, because
// the missing section is reached before any standing check. The other sidebar
// operations distinguish — a create/read/rename/reorder/assign reaches a
// validation or ownership branch that a holder and a stranger do not share.
const indistinguishableRefusalCeiling = 103
