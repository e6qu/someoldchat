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

// screenStreamSuffix marks a forwarded media stream as a participant's screen
// share rather than their camera. A participant id is a prefixed hex string
// (domain.PublicID) with no dot, so the suffix cannot collide with an owner and
// the two lanes stay distinguishable. It is a valid SDP msid token, so it
// survives renegotiation unchanged. The browser mirrors this exact convention
// to route a screen stream to its presenter surface.
const screenStreamSuffix = ".screen"

// forwardStreamID is the outgoing media-stream id for a participant's forwarded
// track. Their camera and microphone share the owner's id alone, so the browser
// groups them onto one tile; their screen carries the owner plus the screen
// suffix, so it lands on a distinct presenter surface while the camera keeps
// playing. It must be a valid SDP msid token, so it carries no delimiter.
func forwardStreamID(owner string, screen bool) string {
	if screen {
		return owner + screenStreamSuffix
	}
	return owner
}

// ForwardedOwner recovers the participant a forwarded stream id belongs to,
// whether it is a camera or a screen stream. The browser reads it from the
// incoming stream id to place the track on the right tile.
func ForwardedOwner(streamID string) string {
	return strings.TrimSuffix(streamID, screenStreamSuffix)
}

// ForwardedIsScreen reports whether a forwarded stream id names a participant's
// screen share rather than their camera, so a subscriber can promote it to a
// presenter view instead of replacing the owner's camera tile.
func ForwardedIsScreen(streamID string) bool {
	return strings.HasSuffix(streamID, screenStreamSuffix)
}
