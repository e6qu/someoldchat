package events

// SlackbotResponseCreatedTopic and SlackbotResponseDeletedTopic record a
// member's change to the workspace's Slackbot custom responses. The topic
// table records that neither is a Slack event.
const (
	SlackbotResponseCreatedTopic = "slackbot_response.created"
	SlackbotResponseDeletedTopic = "slackbot_response.deleted"
)
