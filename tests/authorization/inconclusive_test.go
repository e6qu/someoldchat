package authorization

// inconclusiveStanding are the operations the zero-argument probe cannot decide,
// because each checks a required argument before it checks the caller. The
// authority check exists and does run for a caller who supplies valid
// arguments; what the probe cannot do is reach it.
//
// This is a blind spot in the test, recorded as one. It is not a list of
// operations that skip authorization: TestEveryInconclusiveMethodReallyIsInconclusive
// proves each entry answers an owner exactly as it answers a stranger, so an
// unguarded operation cannot hide here.
func inconclusiveStanding() map[string]struct{} {
	return map[string]struct{}{
		"AddPeopleToDirectConversation":      {},
		"AdminCreateIncomingWebhook":         {},
		"AdminSetTriggerTypePermission":      {},
		"AdminTriggerTypePermission":         {},
		"AppendMessageStream":                {},
		"AssistantSearchContext":             {},
		"DeleteExternalAuthToken":            {},
		"DispatchViewBlockAction":            {},
		"GetWorkflowPermission":              {},
		"ListEventsAfter":                    {},
		"ListFeaturedWorkflows":              {},
		"ListUserEventsAfter":                {},
		"LoadAppOptions":                     {},
		"OpenUserPhoto":                      {},
		"Post":                               {},
		"PostMessageAs":                      {},
		"PostWithBlocks":                     {},
		"PostWithBlocksAndAttachments":       {},
		"PublishView":                        {},
		"RequestWorkflowStepResponsesExport": {},
		"SetAssistantThreadSuggestedPrompts": {},
		"SetAssistantThreadTitle":            {},
		"SetWorkflowPermission":              {},
		"ShareFile":                          {},
		"StartMessageStream":                 {},
		"StopMessageStream":                  {},
		"SynchronizeExternalUserRole":        {},
		"UpdateMessage":                      {},
		"UpdateScheduledMessage":             {},
		"WorkflowStepCompleted":              {},
		"WorkflowStepFailed":                 {},
		"WorkflowStepResponses":              {},
		"WorkflowUpdateStep":                 {},
	}
}

// inconclusiveStandingCeiling is how blind the probe is allowed to be.
const inconclusiveStandingCeiling = 33
