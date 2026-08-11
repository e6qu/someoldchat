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
package authorization

func refusalDoesNotDistinguishTheHolder() map[string]struct{} {
	return map[string]struct{}{
		"AcceptSharedInvite":                      {},
		"AddBookmark":                             {},
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
		"ApproveSharedInvite":                     {},
		"AssignListItem":                          {},
		"AssistantThread":                         {},
		"CloseView":                               {},
		"CommentOnCanvas":                         {},
		"CompleteFunction":                        {},
		"CompleteWorkflowButton":                  {},
		"ConversationCanvas":                      {},
		"ConvertGroupDirectToPrivate":             {},
		"CurrentModalView":                        {},
		"DeclineSharedInvite":                     {},
		"DeleteCanvas":                            {},
		"DeleteCanvasAccess":                      {},
		"DeleteCanvasComment":                     {},
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
		"RevokeSharedInvite":                      {},
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
const indistinguishableRefusalCeiling = 90
