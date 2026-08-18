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

// forwardStreamID carries the owning participant in the outgoing stream id so a
// subscriber's browser can attribute each forwarded track to the participant it
// came from and lay their tiles out accordingly.
func forwardStreamID(owner, streamID string) string {
	return owner + forwardDelimiter + streamID
}

// ForwardedOwner recovers the participant a forwarded stream id belongs to. The
// browser reads it from the incoming stream id to place the track on the right
// tile.
func ForwardedOwner(streamID string) string {
	return forwardOwner(streamID)
}
