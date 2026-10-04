package domain

// MaxStreamMarkdownRunes is the longest markdown_text one chat.appendStream
// or chat.startStream call may carry.
const MaxStreamMarkdownRunes = 12_000

const (
	// MaxMessageTextRunes is the single text ceiling for every way a message can
	// enter or change in the product. It is measured in Unicode code points, as
	// Slack's documented 40,000-character contract is; byte length would reject
	// non-ASCII text earlier than an equally long ASCII message.
	MaxMessageTextRunes = 40000

	// MaxMessageBodyBytes bounds the combined normalized Block Kit and legacy
	// attachment document. The Slack HTTP adapter enforced this first, but the
	// shared service did not, so gRPC and incoming-webhook callers could persist
	// a structured message large enough to amplify every later history read.
	MaxMessageBodyBytes = 256 << 10
)

// MaxUserPhotoBytes is the largest profile photo users.setPhoto accepts.
const MaxUserPhotoBytes = 10 << 20

// MaxCustomEmojiBytes is Slack's limit on an uploaded custom emoji image.
const MaxCustomEmojiBytes = 128 << 10

// CustomEmojiSide is the largest side an uploaded custom emoji is stored at.
// Slack resizes an upload that is larger, and shows an emoji no larger than
// this even at its jumbo size, so the bytes past it are never displayed.
const CustomEmojiSide = 128

// MaxConversationTextLength is the longest topic or purpose Slack accepts, in
// characters. It was compared with len(), which counts bytes, so a topic in any
// non-Latin script was refused at a third of the length Slack allows.
const MaxConversationTextLength = 250

// MaxAccessLogPages is the deepest page team.accessLogs serves; Slack answers
// over_pagination_limit past it.
const MaxAccessLogPages = 100
