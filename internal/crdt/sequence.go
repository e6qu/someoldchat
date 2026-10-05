// Package crdt is the canvas's collaborative text: a replicated sequence of
// characters that every writer edits locally and that converges to the same
// text on every replica, whatever order the edits arrive in.
//
// It is an RGA (replicated growable array). Every character carries an ID, a
// writer and a Lamport clock, unique across the document. An insert names the
// character it goes after; inserts after the same character are ordered by ID,
// the greater first, which makes the order the same everywhere without any
// coordination. A deleted character stays as a tombstone, so an edit that names
// it can still find its place.
//
// The same algorithm runs in the browser (canvasTextScript in internal/web). The
// two are held to one behaviour by the conformance vectors in testdata, which
// both implementations replay: the Go tests here and the browser suite's
// conformance spec.
//
// Positions count Unicode code points, the unit both languages can agree on,
// and the clocks stay below 2^53 so the browser's numbers hold them exactly.
package crdt

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ID names one character. The zero ID is the start of the document, which an
// insert at the beginning goes after.
type ID struct {
	Replica string `json:"r"`
	Clock   uint64 `json:"c"`
}

// replicaName is what a writer may be called: short ASCII, so the byte order Go
// compares by and the UTF-16 order the browser compares by are the same order.
var replicaName = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

// IsZero reports whether the ID is the start of the document.
func (id ID) IsZero() bool { return id.Replica == "" && id.Clock == 0 }

// Less is the total order that settles concurrent inserts: the Lamport clock,
// then the writer.
func (id ID) Less(other ID) bool {
	if id.Clock != other.Clock {
		return id.Clock < other.Clock
	}
	return id.Replica < other.Replica
}

// Span is a run of one writer's consecutive clocks, Start to End inclusive: the
// characters one delete removes from one writer's text.
type Span struct {
	Replica string `json:"r"`
	Start   uint64 `json:"s"`
	End     uint64 `json:"e"`
}

// Op is one edit. An insert puts Text after After, its characters taking ID's
// clock and the ones following it, each after the one before. A delete removes
// the characters Delete names.
type Op struct {
	ID     ID     `json:"id,omitzero"`
	After  ID     `json:"after,omitzero"`
	Text   string `json:"text,omitempty"`
	Delete []Span `json:"delete,omitempty"`
}

// Valid reports whether the op is well formed: an insert or a delete, never
// both and never empty, with names the document could hold.
func (op Op) Valid() error {
	inserting, deleting := op.Text != "", len(op.Delete) > 0
	switch {
	case inserting == deleting:
		return errors.New("an op is one insert or one delete")
	case inserting:
		if !replicaName.MatchString(op.ID.Replica) || op.ID.Clock == 0 {
			return errors.New("an insert names its writer and clock")
		}
		if !utf8.ValidString(op.Text) {
			return errors.New("an insert is valid UTF-8")
		}
		if !op.After.IsZero() && (!replicaName.MatchString(op.After.Replica) || op.After.Clock == 0) {
			return errors.New("an insert goes after a character or the start")
		}
		if op.ID.Clock+uint64(utf8.RuneCountInString(op.Text)) > maxClock {
			return errors.New("an insert's clocks overflow")
		}
	default:
		for _, span := range op.Delete {
			if !replicaName.MatchString(span.Replica) || span.Start == 0 || span.End < span.Start || span.End > maxClock {
				return errors.New("a delete names whole runs of characters")
			}
		}
	}
	return nil
}

// maxClock is the largest clock an op may carry: the browser holds clocks as
// numbers, which are exact only up to 2^53.
const maxClock = 1<<53 - 1

// ErrMissingDependency is an op that names a character this replica has not
// seen yet. It is not an error in the document: the op is held until the edit
// it depends on arrives (Sequence.Apply does this).
var ErrMissingDependency = errors.New("the op depends on an edit not yet applied")

type node struct {
	id      ID
	value   rune
	deleted bool
	next    *node
}

// Sequence is one replica of the text.
type Sequence struct {
	head    node
	nodes   map[ID]*node
	clock   uint64
	visible int
	pending []Op
}

// New returns an empty document.
func New() *Sequence {
	return &Sequence{nodes: map[ID]*node{}}
}

// Text is the document as it reads now.
func (s *Sequence) Text() string {
	var out strings.Builder
	for current := s.head.next; current != nil; current = current.next {
		if !current.deleted {
			out.WriteRune(current.value)
		}
	}
	return out.String()
}

// Len is the number of characters the document reads as.
func (s *Sequence) Len() int { return s.visible }

// Clock is the highest Lamport clock this replica has seen.
func (s *Sequence) Clock() uint64 { return s.clock }

// Pending is how many received ops wait for an edit that has not arrived.
func (s *Sequence) Pending() int { return len(s.pending) }

// Apply integrates an op from any replica, its own included. An op that
// depends on an edit not yet seen is held and applied once that edit arrives;
// applying an op twice changes nothing. It reports whether the document
// changed.
func (s *Sequence) Apply(op Op) (bool, error) {
	if err := op.Valid(); err != nil {
		return false, err
	}
	changed, err := s.integrate(op)
	if errors.Is(err, ErrMissingDependency) {
		s.pending = append(s.pending, op)
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// An applied op may be what a held one was waiting for, and that one in
	// turn what another was waiting for.
	for progress := true; progress && len(s.pending) > 0; {
		progress = false
		waiting := s.pending
		s.pending = nil
		for _, held := range waiting {
			heldChanged, heldErr := s.integrate(held)
			switch {
			case errors.Is(heldErr, ErrMissingDependency):
				s.pending = append(s.pending, held)
			case heldErr != nil:
				return changed, heldErr
			default:
				progress = true
				changed = changed || heldChanged
			}
		}
	}
	return changed, nil
}

func (s *Sequence) integrate(op Op) (bool, error) {
	if op.Text != "" {
		return s.integrateInsert(op)
	}
	return s.integrateDelete(op)
}

func (s *Sequence) integrateInsert(op Op) (bool, error) {
	after := &s.head
	if !op.After.IsZero() {
		found, ok := s.nodes[op.After]
		if !ok {
			return false, ErrMissingDependency
		}
		after = found
	}
	// An insert has seen what it goes after, so its clock is greater: the
	// order below depends on that, and an op that breaks it would leave
	// replicas disagreeing.
	if op.ID.Clock <= after.id.Clock {
		return false, errors.New("an insert's clock is not after the character it follows")
	}
	changed := false
	clock := op.ID.Clock
	for _, value := range op.Text {
		id := ID{Replica: op.ID.Replica, Clock: clock}
		clock++
		if existing, seen := s.nodes[id]; seen {
			after = existing
			continue
		}
		// Skip the characters inserted after the same place with a greater ID,
		// and everything inserted after them: their clocks are greater still,
		// so one comparison covers the whole run.
		previous := after
		for previous.next != nil && id.Less(previous.next.id) {
			previous = previous.next
		}
		inserted := &node{id: id, value: value, next: previous.next}
		previous.next = inserted
		s.nodes[id] = inserted
		s.visible++
		s.clock = max(s.clock, id.Clock)
		after = inserted
		changed = true
	}
	return changed, nil
}

func (s *Sequence) integrateDelete(op Op) (bool, error) {
	// All or nothing: a delete naming a character not yet seen waits whole,
	// rather than removing part of what it names now and the rest later.
	for _, span := range op.Delete {
		for clock := span.Start; clock <= span.End; clock++ {
			if _, ok := s.nodes[ID{Replica: span.Replica, Clock: clock}]; !ok {
				return false, ErrMissingDependency
			}
		}
	}
	changed := false
	for _, span := range op.Delete {
		for clock := span.Start; clock <= span.End; clock++ {
			target := s.nodes[ID{Replica: span.Replica, Clock: clock}]
			if !target.deleted {
				target.deleted = true
				s.visible--
				changed = true
			}
			s.clock = max(s.clock, clock)
		}
	}
	return changed, nil
}

// Insert is a local edit by replica: text at position (in code points) of the
// document as it reads now. It returns the op, already applied, for the other
// replicas.
func (s *Sequence) Insert(replica string, position int, text string) (Op, error) {
	if !replicaName.MatchString(replica) {
		return Op{}, errors.New("an edit names its writer")
	}
	if position < 0 || position > s.visible {
		return Op{}, fmt.Errorf("position %d is outside a document of %d", position, s.visible)
	}
	if text == "" || !utf8.ValidString(text) {
		return Op{}, errors.New("an insert is non-empty valid UTF-8")
	}
	op := Op{ID: ID{Replica: replica, Clock: s.clock + 1}, After: s.visibleBefore(position), Text: text}
	if _, err := s.Apply(op); err != nil {
		return Op{}, err
	}
	return op, nil
}

// Remove is a local edit: length characters from position. It returns the op,
// already applied, for the other replicas.
func (s *Sequence) Remove(position, length int) (Op, error) {
	if position < 0 || length <= 0 || position+length > s.visible {
		return Op{}, fmt.Errorf("characters %d to %d are outside a document of %d", position, position+length, s.visible)
	}
	var spans []Span
	index := 0
	for current := s.head.next; current != nil && index < position+length; current = current.next {
		if current.deleted {
			continue
		}
		if index >= position {
			last := len(spans) - 1
			if last >= 0 && spans[last].Replica == current.id.Replica && spans[last].End+1 == current.id.Clock {
				spans[last].End = current.id.Clock
			} else {
				spans = append(spans, Span{Replica: current.id.Replica, Start: current.id.Clock, End: current.id.Clock})
			}
		}
		index++
	}
	op := Op{Delete: spans}
	if _, err := s.Apply(op); err != nil {
		return Op{}, err
	}
	return op, nil
}

// visibleBefore is the character an insert at position goes after: the
// visible character just before it, or the start.
func (s *Sequence) visibleBefore(position int) ID {
	if position == 0 {
		return ID{}
	}
	index := 0
	for current := s.head.next; current != nil; current = current.next {
		if current.deleted {
			continue
		}
		index++
		if index == position {
			return current.id
		}
	}
	return ID{}
}

// Replace makes the document read text by the smallest edit at its two ends:
// the common beginning and end are kept, and what lies between is removed and
// inserted. It is how a writer that does not speak in ops, such as the Slack
// API, edits the same document every editor is editing.
func (s *Sequence) Replace(replica string, text string) ([]Op, error) {
	if !utf8.ValidString(text) {
		return nil, errors.New("the text is not valid UTF-8")
	}
	current := []rune(s.Text())
	next := []rune(text)
	prefix := 0
	for prefix < len(current) && prefix < len(next) && current[prefix] == next[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(current)-prefix && suffix < len(next)-prefix && current[len(current)-1-suffix] == next[len(next)-1-suffix] {
		suffix++
	}
	var ops []Op
	if removed := len(current) - prefix - suffix; removed > 0 {
		op, err := s.Remove(prefix, removed)
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	if inserted := next[prefix : len(next)-suffix]; len(inserted) > 0 {
		op, err := s.Insert(replica, prefix, string(inserted))
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, nil
}

// Run is a stretch of the document as stored: consecutive characters of one
// writer with consecutive clocks, either the text they read as or, once
// deleted, only how many there were. A tombstone needs its place and its name
// for edits that refer to it, never its text.
type Run struct {
	Replica string `json:"r"`
	Clock   uint64 `json:"c"`
	Text    string `json:"t,omitempty"`
	Deleted int    `json:"n,omitempty"`
}

// Snapshot is the document as stored: every character in order, tombstones
// included, as runs. Load reads it back to the same replica state.
func (s *Sequence) Snapshot() []Run {
	runs := []Run{}
	var text strings.Builder
	flush := func() {
		if last := len(runs) - 1; last >= 0 && runs[last].Deleted == 0 {
			runs[last].Text = text.String()
		}
		text.Reset()
	}
	var previous *node
	for current := s.head.next; current != nil; current = current.next {
		continues := previous != nil && previous.id.Replica == current.id.Replica &&
			previous.id.Clock+1 == current.id.Clock && previous.deleted == current.deleted
		if !continues {
			flush()
			runs = append(runs, Run{Replica: current.id.Replica, Clock: current.id.Clock})
		}
		if current.deleted {
			runs[len(runs)-1].Deleted++
		} else {
			text.WriteRune(current.value)
		}
		previous = current
	}
	flush()
	return runs
}

// Load is the replica a snapshot describes. It refuses a snapshot that names
// a character twice or that no document could hold, rather than building a
// replica that would disagree with the others.
func Load(runs []Run) (*Sequence, error) {
	s := New()
	tail := &s.head
	for _, run := range runs {
		count := run.Deleted
		if run.Text != "" {
			if run.Deleted != 0 || !utf8.ValidString(run.Text) {
				return nil, errors.New("a stored run is either text or deleted characters")
			}
			count = utf8.RuneCountInString(run.Text)
		}
		if !replicaName.MatchString(run.Replica) || run.Clock == 0 || count <= 0 || run.Clock+uint64(count)-1 > maxClock {
			return nil, errors.New("a stored run names characters no document holds")
		}
		deleted, values := run.Text == "", []rune(run.Text)
		for index := range count {
			id := ID{Replica: run.Replica, Clock: run.Clock + uint64(index)}
			if _, repeated := s.nodes[id]; repeated {
				return nil, errors.New("a stored document names a character twice")
			}
			current := &node{id: id, deleted: deleted}
			if !deleted {
				current.value = values[index]
				s.visible++
			}
			tail.next = current
			tail = current
			s.nodes[id] = current
			s.clock = max(s.clock, id.Clock)
		}
	}
	return s, nil
}

// ValidReplica reports whether name may name a writer.
func ValidReplica(name string) bool { return replicaName.MatchString(name) }
