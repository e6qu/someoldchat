package huddlesfu

import "strings"

// A forwarded track is identified by the participant that published it and that
// track's own id. The two are joined by a NUL, which no participant id or track
// id contains, so the owner can be recovered unambiguously — the room needs it
// to avoid sending a participant its own track back.
const forwardDelimiter = "\x00"

func forwardKey(owner, trackID string) string {
	return owner + forwardDelimiter + trackID
}

func forwardOwner(key string) string {
	if index := strings.Index(key, forwardDelimiter); index >= 0 {
		return key[:index]
	}
	return key
}

// forwardStreamID is the outgoing media-stream id for a participant's forwarded
// tracks: their id and nothing else. It must be a valid SDP msid token, so it
// carries no delimiter. Using the owner alone also groups all of a participant's
// tracks into one media stream on the subscriber, which is what the browser
// attaches to their tile.
func forwardStreamID(owner, _ string) string {
	return owner
}

// ForwardedOwner recovers the participant a forwarded stream id belongs to. The
// browser reads it from the incoming stream id to place the track on the right
// tile.
func ForwardedOwner(streamID string) string {
	return streamID
}
