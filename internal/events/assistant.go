package events

// AssistantThreadUpdatedTopic records an assistant app's write to a thread's
// assistant state (assistant.threads.setTitle, setStatus and
// setSuggestedPrompts). It is not a Slack event — the methods answer the app
// itself — and exists so an open client re-renders the thread's state.
const AssistantThreadUpdatedTopic = "assistant.thread_updated"
