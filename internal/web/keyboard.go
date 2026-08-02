package web

import (
	"html/template"
	"strings"
)

// The client's keyboard layer, declared once.
//
// It used to be declared three times: in the script that handles the key, in
// the `aria-keyshortcuts` attribute that announces it, and in whatever prose
// claimed it existed. Nothing held those together, so a shortcut could be
// announced to a screen reader and do nothing, or work and be undiscoverable.
// NAV-02 lists `F6` section movement as a requirement and no code has ever
// implemented it, which is exactly that failure in its most durable form.
//
// This file is the declaration. keyboardSections renders the help dialog that
// Command/Control+/ opens, ariaKeyshortcuts renders the attributes, and
// TestEveryAdvertisedShortcutIsDocumented asserts the page announces nothing
// this list does not describe.

// keyboardShortcut is one binding: what it does, and which chord invokes it on
// each platform.
//
// Apple and Other are written in the DOM `aria-keyshortcuts` grammar
// ("Meta+Shift+K"), not in display form, because the attribute is the
// machine-readable half and prose is generated from it rather than the other
// way round. Display form is derived by keyboardLabel.
type keyboardShortcut struct {
	Action string
	Apple  string
	Other  string
	// Note records a difference from Slack's desktop client. Slack's own help
	// article documents several of these; where the browser cannot take a
	// chord the operating system or the browser has already claimed, saying so
	// is the honest surface, not silently binding something else.
	Note string
}

// keyboardSection groups shortcuts the way Slack's own shortcut help groups
// them, so a member who knows where to look in Slack looks in the same place
// here.
type keyboardSection struct {
	Title     string
	Shortcuts []keyboardShortcut
}

// keyboardSections is the whole layer. Everything the client binds appears
// here; nothing appears here that the client does not bind.
func keyboardSections() []keyboardSection {
	return []keyboardSection{
		{Title: "Navigation", Shortcuts: []keyboardShortcut{
			{Action: "Search the workspace", Apple: "Meta+G", Other: "Control+G"},
			{Action: "Search this conversation", Apple: "Meta+F", Other: "Control+F"},
			{Action: "Jump to a conversation", Apple: "Meta+K", Other: "Control+K"},
			{Action: "Previous conversation", Apple: "Alt+ArrowUp", Other: "Alt+ArrowUp"},
			{Action: "Next conversation", Apple: "Alt+ArrowDown", Other: "Alt+ArrowDown"},
			{Action: "Previous unread conversation", Apple: "Alt+Shift+ArrowUp", Other: "Alt+Shift+ArrowUp"},
			{Action: "Next unread conversation", Apple: "Alt+Shift+ArrowDown", Other: "Alt+Shift+ArrowDown"},
			{Action: "Move to the next section", Apple: "Meta+F6", Other: "Control+F6",
				Note: "Slack's desktop client uses F6 alone; a browser reserves it, so the primary modifier is added."},
			{Action: "Move to the previous section", Apple: "Meta+Shift+F6", Other: "Control+Shift+F6"},
			{Action: "Activity", Apple: "Control+3", Other: "Control+Shift+3",
				Note: "Slack's Command/Control+Shift+M is desktop-only; the web client uses the numbered navigation-tab shortcut."},
			{Action: "Unreads", Apple: "Meta+Shift+A", Other: "Control+Shift+A"},
			{Action: "Threads", Apple: "Meta+Shift+T", Other: "Control+Shift+T"},
			{Action: "Direct messages", Apple: "Meta+Shift+K", Other: "Control+Shift+K"},
			{Action: "Later", Apple: "Meta+Shift+S", Other: "Control+Shift+S"},
			{Action: "Conversation details", Apple: "Meta+Shift+I", Other: "Control+Shift+I"},
			{Action: "Keyboard shortcuts", Apple: "Meta+/", Other: "Control+/"},
		}},
		{Title: "Reading", Shortcuts: []keyboardShortcut{
			{Action: "Mark this conversation read", Apple: "Escape", Other: "Escape",
				Note: "Outside a text field, so Escape still dismisses the composer's suggestions, a dialog, or the navigation drawer."},
			{Action: "Mark every conversation read", Apple: "Shift+Escape", Other: "Shift+Escape",
				Note: "Works from the composer too: Shift+Escape means nothing else in a text field."},
			{Action: "Move between messages", Apple: "ArrowUp ArrowDown Home End", Other: "ArrowUp ArrowDown Home End"},
			{Action: "Open the thread on the focused message", Apple: "T ArrowRight", Other: "T ArrowRight"},
			{Action: "Return to the conversation from a thread", Apple: "ArrowLeft", Other: "ArrowLeft"},
			{Action: "Mark unread from the focused message", Apple: "U", Other: "U"},
		}},
		{Title: "Message actions", Shortcuts: []keyboardShortcut{
			{Action: "React to the focused message", Apple: "R", Other: "R"},
			{Action: "Edit the focused message", Apple: "E", Other: "E"},
			{Action: "Save the focused message for later", Apple: "A", Other: "A"},
			{Action: "Remind me about the focused message", Apple: "M", Other: "M"},
			{Action: "Forward the focused message", Apple: "F", Other: "F"},
			{Action: "Pin or unpin the focused message", Apple: "P", Other: "P"},
			{Action: "Delete the focused message", Apple: "Delete", Other: "Delete"},
		}},
		{Title: "Composing", Shortcuts: []keyboardShortcut{
			{Action: "Send the message", Apple: "Enter", Other: "Enter"},
			{Action: "New line without sending", Apple: "Shift+Enter", Other: "Shift+Enter"},
			{Action: "Edit your last message", Apple: "ArrowUp", Other: "ArrowUp",
				Note: "Only when the composer is empty."},
			{Action: "Bold", Apple: "Meta+B", Other: "Control+B"},
			{Action: "Italic", Apple: "Meta+I", Other: "Control+I"},
			{Action: "Strikethrough", Apple: "Meta+Shift+X", Other: "Control+Shift+X"},
			{Action: "Attach a file", Apple: "Meta+U", Other: "Control+U"},
		}},
	}
}

// keyboardView is one row of the rendered help dialog.
type keyboardView struct {
	Action string
	Apple  string
	Other  string
	Note   string
	// Search is everything a member might type to find this row, so the
	// dialog's filter matches on the action, on either chord, and on the
	// display form of either chord.
	Search string
}

type keyboardSectionView struct {
	Title     string
	Shortcuts []keyboardView
}

func keyboardHelp() []keyboardSectionView {
	sections := keyboardSections()
	views := make([]keyboardSectionView, 0, len(sections))
	for _, section := range sections {
		rows := make([]keyboardView, 0, len(section.Shortcuts))
		for _, shortcut := range section.Shortcuts {
			apple := keyboardLabel(shortcut.Apple, true)
			other := keyboardLabel(shortcut.Other, false)
			rows = append(rows, keyboardView{
				Action: shortcut.Action,
				Apple:  apple,
				Other:  other,
				Note:   shortcut.Note,
				Search: strings.ToLower(strings.Join([]string{shortcut.Action, shortcut.Apple, shortcut.Other, apple, other, section.Title}, " ")),
			})
		}
		views = append(views, keyboardSectionView{Title: section.Title, Shortcuts: rows})
	}
	return views
}

// keyboardLabel turns the `aria-keyshortcuts` grammar into what a member reads
// on their own platform: ⌘⇧K on Apple hardware, Ctrl+Shift+K elsewhere.
func keyboardLabel(chord string, apple bool) string {
	parts := strings.Fields(chord)
	labels := make([]string, 0, len(parts))
	for _, part := range parts {
		labels = append(labels, keyboardChordLabel(part, apple))
	}
	return strings.Join(labels, " ")
}

func keyboardChordLabel(chord string, apple bool) string {
	keys := strings.Split(chord, "+")
	labels := make([]string, 0, len(keys))
	for _, key := range keys {
		labels = append(labels, keyboardKeyLabel(key, apple))
	}
	if apple {
		// Apple platforms concatenate modifier symbols without a separator,
		// which is what the platform's own menus do.
		return strings.Join(labels, "")
	}
	return strings.Join(labels, "+")
}

func keyboardKeyLabel(key string, apple bool) string {
	if apple {
		switch key {
		case "Meta":
			return "⌘"
		case "Shift":
			return "⇧"
		case "Alt":
			return "⌥"
		case "Control":
			return "⌃"
		}
	}
	switch key {
	case "Meta":
		return "Cmd"
	case "Control":
		return "Ctrl"
	case "ArrowUp":
		return "↑"
	case "ArrowDown":
		return "↓"
	case "Escape":
		return "Esc"
	}
	return key
}

// ariaKeyshortcuts renders the whole attribute for one action, naming both
// platforms' chords because the attribute has no way to express "whichever
// applies here" and assistive technology matches on the literal chord.
//
// It looks the action up rather than taking a chord, so an element cannot
// announce a binding that keyboardSections does not declare. It returns the
// attribute rather than its value for two reasons: the attribute name cannot
// then be misspelled at a call site, and the chord separator is `+`, which
// html/template escapes to `&#43;` inside a quoted value. Browsers decode that
// correctly, but the rendered page stops saying what it means, and every test
// and selector that reads the markup has to know about the escape. The whole
// attribute comes from the table in this file and no request ever reaches it.
func ariaKeyshortcuts(action string) template.HTMLAttr {
	for _, section := range keyboardSections() {
		for _, shortcut := range section.Shortcuts {
			if shortcut.Action != action {
				continue
			}
			chord := shortcut.Apple
			if shortcut.Apple != shortcut.Other {
				chord = shortcut.Apple + " " + shortcut.Other
			}
			return template.HTMLAttr(`aria-keyshortcuts="` + chord + `"`)
		}
	}
	// An unknown action is a programming error, and returning nothing makes it
	// silent. Naming it makes the rendered page obviously wrong, which is how
	// it gets noticed — and TestEveryAdvertisedShortcutIsDocumented fails on it.
	return template.HTMLAttr(`data-unknown-shortcut="` + template.HTMLEscapeString(action) + `"`)
}
