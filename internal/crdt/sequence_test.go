package crdt

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/conformance.json from the simulation")

func mustInsert(t *testing.T, s *Sequence, replica string, position int, text string) Op {
	t.Helper()
	op, err := s.Insert(replica, position, text)
	if err != nil {
		t.Fatalf("insert %q at %d: %v", text, position, err)
	}
	return op
}

func mustApply(t *testing.T, s *Sequence, ops ...Op) {
	t.Helper()
	for _, op := range ops {
		if _, err := s.Apply(op); err != nil {
			t.Fatalf("apply %+v: %v", op, err)
		}
	}
}

func TestLocalEditsReadAsWritten(t *testing.T) {
	s := New()
	mustInsert(t, s, "a", 0, "héllo")
	mustInsert(t, s, "a", 5, " wörld")
	mustInsert(t, s, "a", 0, "😀 ")
	if got := s.Text(); got != "😀 héllo wörld" {
		t.Fatalf("text = %q", got)
	}
	if _, err := s.Remove(2, 6); err != nil {
		t.Fatal(err)
	}
	if got, want := s.Text(), "😀 wörld"; got != want || s.Len() != 7 {
		t.Fatalf("text = %q (%d), want %q", got, s.Len(), want)
	}
}

func TestConcurrentInsertsConvergeWithoutInterleaving(t *testing.T) {
	base := New()
	start := mustInsert(t, base, "a", 0, "ab")

	left, right := New(), New()
	mustApply(t, left, start)
	mustApply(t, right, start)
	fromLeft := mustInsert(t, left, "left", 1, "XYZ")
	fromRight := mustInsert(t, right, "right", 1, "123")
	mustApply(t, left, fromRight)
	mustApply(t, right, fromLeft)

	if left.Text() != right.Text() {
		t.Fatalf("replicas diverged: %q and %q", left.Text(), right.Text())
	}
	// Each writer's run stays whole: concurrent typing at one place reads as
	// one run after the other, never as interleaved characters.
	if got := left.Text(); got != "aXYZ123b" && got != "a123XYZb" {
		t.Fatalf("text = %q, want the two runs whole", got)
	}
}

func TestOpsWaitForWhatTheyDependOn(t *testing.T) {
	writer := New()
	first := mustInsert(t, writer, "w", 0, "one")
	second := mustInsert(t, writer, "w", 3, " two")
	removal, err := writer.Remove(0, 4)
	if err != nil {
		t.Fatal(err)
	}

	reader := New()
	mustApply(t, reader, removal, second)
	if reader.Text() != "" || reader.Pending() != 2 {
		t.Fatalf("before the first op: text %q, pending %d", reader.Text(), reader.Pending())
	}
	mustApply(t, reader, first)
	if reader.Text() != writer.Text() || reader.Pending() != 0 {
		t.Fatalf("after: text %q, pending %d, want %q", reader.Text(), reader.Pending(), writer.Text())
	}
}

func TestApplyingTwiceChangesNothing(t *testing.T) {
	s := New()
	op := mustInsert(t, s, "a", 0, "abc")
	removal, err := s.Remove(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, again := range []Op{op, removal} {
		changed, err := s.Apply(again)
		if err != nil || changed {
			t.Fatalf("reapplying %+v: changed %v, err %v", again, changed, err)
		}
	}
	if s.Text() != "ac" {
		t.Fatalf("text = %q", s.Text())
	}
}

func TestMalformedOpsAreRefused(t *testing.T) {
	s := New()
	mustInsert(t, s, "a", 0, "abc")
	for name, op := range map[string]Op{
		"empty":            {},
		"both":             {ID: ID{"b", 9}, Text: "x", Delete: []Span{{"a", 1, 1}}},
		"no writer":        {ID: ID{"", 9}, Text: "x"},
		"no clock":         {ID: ID{"b", 0}, Text: "x"},
		"half an after":    {ID: ID{"b", 9}, After: ID{"a", 0}, Text: "x"},
		"invalid text":     {ID: ID{"b", 9}, Text: "\xff"},
		"reversed span":    {Delete: []Span{{"a", 3, 1}}},
		"clock from past":  {ID: ID{"b", 2}, After: ID{"a", 3}, Text: "x"},
		"overflowing span": {ID: ID{"b", maxClock}, Text: "xy"},
		"unsafe writer":    {ID: ID{"b c", 9}, Text: "x"},
		"long delete":      {Delete: []Span{{"a", 1, maxClock + 1}}},
	} {
		if _, err := s.Apply(op); err == nil {
			t.Errorf("%s: applied", name)
		}
	}
	if s.Text() != "abc" || s.Pending() != 0 {
		t.Fatalf("a refused op changed the document: %q, pending %d", s.Text(), s.Pending())
	}
}

func TestReplaceEditsOnlyTheMiddle(t *testing.T) {
	s := New()
	mustInsert(t, s, "a", 0, "# Plan\n\nship it\n")
	ops, err := s.Replace("api", "# Plan\n\nship it today\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Text != " today" {
		t.Fatalf("ops = %+v, want one insert of the new words", ops)
	}
	if ops, err := s.Replace("api", s.Text()); err != nil || len(ops) != 0 {
		t.Fatalf("replacing with the same text: %+v, %v", ops, err)
	}
	if _, err := s.Replace("api", ""); err != nil || s.Text() != "" {
		t.Fatalf("clearing: %q, %v", s.Text(), err)
	}
}

// vectors is what both implementations replay. Cases are documents several
// writers edited concurrently: every order of delivering their ops must read as
// the same text. Edits pin how a local edit becomes an op, so an op the
// browser makes is the op the server would have made.
type vectors struct {
	Cases []vectorCase `json:"cases"`
	Edits []vectorEdit `json:"edits"`
}

type vectorCase struct {
	Name   string  `json:"name"`
	Ops    []Op    `json:"ops"`
	Orders [][]int `json:"orders"`
	Text   string  `json:"text"`
	// Snapshot is the converged replica as stored, which every order of
	// delivery reaches and which both implementations load and write alike.
	Snapshot []Run `json:"snapshot"`
}

type vectorEdit struct {
	Name     string `json:"name"`
	Ops      []Op   `json:"ops"`
	Replica  string `json:"replica"`
	Kind     string `json:"kind"`
	Position int    `json:"position"`
	Length   int    `json:"length,omitempty"`
	Text     string `json:"text,omitempty"`
	Produced []Op   `json:"produced"`
	Result   string `json:"result"`
}

var alphabet = []string{"a", "b", "c", " ", "\n", "#", "é", "ß", "中", "😀", "*"}

type simulatedReplica struct {
	name     string
	doc      *Sequence
	received []Op
	seen     map[int]bool
}

// simulate has writers edit at random while ops reach the others late and out
// of order, then delivers everything and checks every replica reads the same.
// It returns the ops in the order they were made, the text they converge on,
// and a sample of the local edits along the way.
func simulate(t *testing.T, seed uint64, writers, steps int) ([]Op, string, []vectorEdit) {
	t.Helper()
	random := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	replicas := make([]*simulatedReplica, writers)
	for i := range replicas {
		replicas[i] = &simulatedReplica{name: fmt.Sprintf("r%d", i), doc: New(), seen: map[int]bool{}}
	}
	var log []Op
	var edits []vectorEdit
	deliver := func(replica *simulatedReplica, index int) {
		if replica.seen[index] {
			return
		}
		replica.seen[index] = true
		replica.received = append(replica.received, log[index])
		mustApply(t, replica.doc, log[index])
	}
	for step := range steps {
		replica := replicas[random.IntN(writers)]
		if random.IntN(3) == 0 && len(log) > 0 {
			for range 1 + random.IntN(4) {
				deliver(replica, random.IntN(len(log)))
			}
			continue
		}
		edit := vectorEdit{
			Name:    fmt.Sprintf("seed %d step %d", seed, step),
			Ops:     append([]Op{}, replica.received...),
			Replica: replica.name,
		}
		var produced []Op
		var err error
		length := replica.doc.Len()
		switch choice := random.IntN(10); {
		case choice < 6 || length == 0:
			edit.Kind, edit.Position = "insert", random.IntN(length+1)
			for range 1 + random.IntN(3) {
				edit.Text += alphabet[random.IntN(len(alphabet))]
			}
			var op Op
			op, err = replica.doc.Insert(replica.name, edit.Position, edit.Text)
			produced = []Op{op}
		case choice < 9:
			edit.Kind, edit.Position = "remove", random.IntN(length)
			edit.Length = 1 + random.IntN(min(3, length-edit.Position))
			var op Op
			op, err = replica.doc.Remove(edit.Position, edit.Length)
			produced = []Op{op}
		default:
			runes := []rune(replica.doc.Text())
			from := random.IntN(len(runes) + 1)
			to := from + random.IntN(len(runes)-from+1)
			edit.Kind, edit.Text = "replace", string(runes[:from])+alphabet[random.IntN(len(alphabet))]+string(runes[to:])
			produced, err = replica.doc.Replace(replica.name, edit.Text)
		}
		if err != nil {
			t.Fatalf("%s: %v", edit.Name, err)
		}
		for _, op := range produced {
			replica.seen[len(log)] = true
			replica.received = append(replica.received, op)
			log = append(log, op)
		}
		edit.Produced, edit.Result = produced, replica.doc.Text()
		if step%17 == 0 {
			edits = append(edits, edit)
		}
	}
	for _, replica := range replicas {
		for index := range log {
			deliver(replica, index)
		}
	}
	text := replicas[0].doc.Text()
	for _, replica := range replicas {
		if replica.doc.Text() != text || replica.doc.Pending() != 0 {
			t.Fatalf("seed %d: %s reads %q (pending %d), %s reads %q", seed, replica.name, replica.doc.Text(), replica.doc.Pending(), replicas[0].name, text)
		}
	}
	return log, text, edits
}

func TestRandomConcurrentEditingConverges(t *testing.T) {
	for seed := range uint64(200) {
		log, text, _ := simulate(t, seed, 2+int(seed%3), 120)
		// A replica that receives every op in a random order, with no
		// causality at all, still reads the same.
		random := rand.New(rand.NewPCG(seed, 1))
		for range 3 {
			late := New()
			for _, index := range random.Perm(len(log)) {
				mustApply(t, late, log[index])
			}
			if late.Text() != text || late.Pending() != 0 {
				t.Fatalf("seed %d: shuffled delivery reads %q (pending %d), want %q", seed, late.Text(), late.Pending(), text)
			}
		}
	}
}

func buildVectors(t *testing.T) vectors {
	t.Helper()
	var out vectors
	for seed := range uint64(12) {
		log, text, edits := simulate(t, seed, 2+int(seed%3), 60)
		random := rand.New(rand.NewPCG(seed, 2))
		orders := [][]int{random.Perm(len(log)), random.Perm(len(log)), random.Perm(len(log))}
		converged := New()
		mustApply(t, converged, log...)
		out.Cases = append(out.Cases, vectorCase{Name: fmt.Sprintf("seed %d", seed), Ops: log, Orders: orders, Text: text, Snapshot: converged.Snapshot()})
		out.Edits = append(out.Edits, edits...)
	}
	return out
}

// TestConformanceVectors keeps the shared vectors what this implementation
// does; the browser suite replays the same file against the script the canvas
// page ships. Run with -update after an intended change.
func TestConformanceVectors(t *testing.T) {
	built, err := json.MarshalIndent(buildVectors(t), "", " ")
	if err != nil {
		t.Fatal(err)
	}
	built = append(built, '\n')
	path := filepath.Join("testdata", "conformance.json")
	if *update {
		if err := os.WriteFile(path, built, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, built) {
		t.Fatal("testdata/conformance.json is stale: run go test ./internal/crdt -update")
	}
	var replayed vectors
	if err := json.Unmarshal(stored, &replayed); err != nil {
		t.Fatal(err)
	}
	for _, c := range replayed.Cases {
		for _, order := range c.Orders {
			s := New()
			for _, index := range order {
				mustApply(t, s, c.Ops[index])
			}
			if s.Text() != c.Text {
				t.Fatalf("%s: %q, want %q", c.Name, s.Text(), c.Text)
			}
			if !reflect.DeepEqual(s.Snapshot(), c.Snapshot) {
				t.Fatalf("%s: a delivery order stores a different replica", c.Name)
			}
		}
	}
}

func TestSnapshotLoadsTheSameReplica(t *testing.T) {
	for seed := range uint64(50) {
		log, text, _ := simulate(t, seed, 3, 80)
		original := New()
		mustApply(t, original, log...)
		encoded, err := json.Marshal(original.Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		var runs []Run
		if err := json.Unmarshal(encoded, &runs); err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(runs)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		if loaded.Text() != text || loaded.Len() != original.Len() || loaded.Clock() != original.Clock() {
			t.Fatalf("seed %d: loaded %q (clock %d), want %q (clock %d)", seed, loaded.Text(), loaded.Clock(), text, original.Clock())
		}
		// The loaded replica goes on editing exactly as the original does.
		next, err := original.Insert("later", original.Len()/2, "ẞ")
		if err != nil {
			t.Fatal(err)
		}
		mustApply(t, loaded, next)
		if loaded.Text() != original.Text() {
			t.Fatalf("seed %d: after an edit, loaded reads %q, original %q", seed, loaded.Text(), original.Text())
		}
	}
}

func TestLoadRefusesSnapshotsNoDocumentHolds(t *testing.T) {
	for name, runs := range map[string][]Run{
		"text and deleted": {{Replica: "a", Clock: 1, Text: "x", Deleted: 1}},
		"empty run":        {{Replica: "a", Clock: 1}},
		"no clock":         {{Replica: "a", Text: "x"}},
		"bad writer":       {{Replica: "a b", Clock: 1, Text: "x"}},
		"repeated":         {{Replica: "a", Clock: 1, Text: "xy"}, {Replica: "a", Clock: 2, Deleted: 1}},
		"invalid text":     {{Replica: "a", Clock: 1, Text: "\xff"}},
		"past the clock":   {{Replica: "a", Clock: maxClock, Text: "xy"}},
	} {
		if _, err := Load(runs); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}
