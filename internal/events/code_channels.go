package events

// CodeChannelPropertiesSetTopic records an agent's
// agents.conversations.setProperties, so an open client re-renders the code
// channel's context bar. The topic table records that it is not a Slack event.
const CodeChannelPropertiesSetTopic = "code_channel.properties_set"

// CodeChannelViewSetTopic and CodeChannelViewRemovedTopic record an agent's
// agents.conversations.setView and removeView, so an open client re-renders
// the channel's tabs. The topic table records that neither is a Slack event.
const (
	CodeChannelViewSetTopic     = "code_channel.view_set"
	CodeChannelViewRemovedTopic = "code_channel.view_removed"
)
