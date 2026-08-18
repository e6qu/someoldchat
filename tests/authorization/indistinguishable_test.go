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
// The exhaustive audit took this list from 103 to 68. It closed every operation
// the fixture can reach as the holder: the list-item operations once the list
// gained a row; the message reactions once the holder had one of its own; the
// posting, scheduling, draft, role, retention and notification operations once
// the probe supplied the one string, level, role, duration or future instant
// each needed; the operations on the caller's own scheduled status, saved item,
// Activity view, sidebar section and draft once those were seeded owned by the
// holder; and the user-group channel and channel-canvas operations once given a
// channel to act on. Each is backed by the probe reaching its success where a
// caller below membership is refused for standing, so a deleted guard is caught.
//
// What remains is three kinds of residue, named rather than counted as covered:
//   - Structural: an operation owner-only on an object the fixture gives the
//     member (DeleteCanvas, SetListAccess), a developer-app operation the fixture
//     app's member owner shadows (the IssueDeveloperAppToken family), a
//     second-organization action (AcceptSharedInvite), an enterprise-grid team
//     relationship a single workspace cannot form (AdminAddUserGroupTeams), or a
//     per-(message,user) pin or star whose add and remove share the one probe
//     timestamp (RemovePin, RemoveStar). Closing these would distort the fixture
//     other operations depend on.
//   - Complex-state buildable: the workflow state machine (RunWorkflow,
//     DeleteWorkflow, WebhookTriggerURL with its sealed secret, the interactive
//     run and function operations) and the incoming-webhook, app-request and
//     app-resolution admin operations. Each needs a multi-step object graph in a
//     particular state; they are a further batch of the same audit, not a
//     different problem.
//   - Payload buildable: the view and interaction operations (PublishView,
//     SubmitView, DispatchSlashCommand) whose front door is a block-kit or
//     interaction payload the probe does not yet synthesize.
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
		"AssistantThread":                         {},
		"CloseView":                               {},
		"CompleteFunction":                        {},
		"CompleteWorkflowButton":                  {},
		"ConvertGroupDirectToPrivate":             {},
		"CurrentModalView":                        {},
		"DeclineSharedInvite":                     {},
		"DeleteCanvas":                            {},
		"DeleteCanvasAccess":                      {},
		"DeleteCanvasComment":                     {},
		"DeleteListItemComment":                   {},
		"DetachFileFromListItem":                  {},
		"DeleteListAccess":                        {},
		"DeleteWorkflow":                          {},
		"DiscardWorkflowStagedChanges":            {},
		"DispatchAppShortcut":                     {},
		"DispatchSlashCommand":                    {},
		"GetDeveloperApp":                         {},
		"GetDeveloperAppDeliveryHealth":           {},
		"GetFunctionPermission":                   {},
		"GetListDownload":                         {},
		"IssueDeveloperAppToken":                  {},
		"RevokeDeveloperAppTokens":                {},
		"ListDeveloperAppTokens":                  {},
		"RevokeDeveloperAppToken":                 {},
		"ListEphemeralMessages":                   {},
		"OpenAppHome":                             {},
		"PostEphemeralWithBlocksAndAttachments":   {},
		"RemoveListColumn":                        {},
		"RemovePin":                               {},
		"RemoveStar":                              {},
		"RestoreCanvasRevision":                   {},
		"RunWorkflow":                             {},
		"SaveDraftWithAttachments":                {},
		"ScheduleMessageAs":                       {},
		"ScheduleMessageWithBlocksAndAttachments": {},
		"SetAppIcon":                              {},
		"SetCanvasAccess":                         {},
		"SetFunctionPermission":                   {},
		"SetListAccess":                           {},
		"SetTriggerPermission":                    {},
		"SetWorkflowTrigger":                      {},
		"SubmitView":                              {},
		"SubmitWorkflowForm":                      {},
		"UpdateListCells":                         {},
		"UpdateWorkflow":                          {},
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
// DeleteListItemComment stays for the reason its canvas sibling does: its
// author-only delete rule lives in the store's write rather than the service, so
// with no comment by the holder on the fixture's item it answers the holder and a
// stranger alike with not-found. CommentOnListItem left the set once the list
// gained a row the holder could comment on. ListItemComments, like CanvasComments,
// was never here — a holder with list access reads an empty page where a stranger
// is refused, so its refusal distinguishes.
//
// DetachFileFromListItem stays for the same structural reason RemovePin does: an
// attach and a detach share the one file id the probe hands out, so seeding an
// attachment for the detach to find would make AttachFileToListItem — which left
// the set once the item existed — answer "already attached" for the same holder.
//
// The developer-app-token family (IssueDeveloperAppToken, RevokeDeveloperAppToken,
// RevokeDeveloperAppTokens, ListDeveloperAppTokens) stays because the fixture's
// one app is owned by U-member while the matrix holder is U-owner, so an owner-only
// developer-app operation refuses the holder and a stranger alike with not-found.
// Re-owning the fixture app would distort the cases that depend on a member-owned
// app.
const indistinguishableRefusalCeiling = 68
