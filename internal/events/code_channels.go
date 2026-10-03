package events

// CodeChannelPropertiesSetTopic records an agent's
// agents.conversations.setProperties, so an open client re-renders the code
// channel's context bar. The topic table records that it is not a Slack event.
const CodeChannelPropertiesSetTopic = "code_channel.properties_set"
