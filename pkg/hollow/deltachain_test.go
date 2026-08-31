package hollow

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

// TestGoldenDeltaChain applies a real chain of delta blobs on top of a real
// snapshot and checks the result against an independently-parsed snapshot
// taken at the same version later in the same delta chain (the fakedata
// generator periodically writes a full snapshot alongside the ongoing delta
// chain, at NUM_STATES_BETWEEN_SNAPSHOTS intervals). This exercises the
// entire delta-application path (all four schema kinds, cross-references,
// var-length data, multi-shard types) against ground truth, not just
// self-consistency. Skipped when fakehollowdata/ isn't populated locally.
func TestGoldenDeltaChain(t *testing.T) {
	snapshotPaths, err := filepath.Glob("../../fakehollowdata/snapshot-*")
	if err != nil {
		t.Fatalf("globbing for snapshot fixtures: %v", err)
	}
	if len(snapshotPaths) < 2 {
		t.Skip("need at least 2 fakehollowdata/snapshot-* fixtures to chain deltas between them; see fakehollowdata/README.md")
	}
	sort.Strings(snapshotPaths)

	deltaPaths, err := filepath.Glob("../../fakehollowdata/delta-*")
	if err != nil {
		t.Fatalf("globbing for delta fixtures: %v", err)
	}
	deltaByFromVersion := make(map[string]string, len(deltaPaths))
	deltaRe := regexp.MustCompile(`^delta-(\d+)-(\d+)$`)
	for _, p := range deltaPaths {
		m := deltaRe.FindStringSubmatch(filepath.Base(p))
		if m == nil {
			t.Fatalf("unrecognized delta fixture filename: %s", p)
		}
		deltaByFromVersion[m[1]] = p
	}

	blob := readSnapshotFixture(t, snapshotPaths[0])
	version := versionOf(t, snapshotPaths[0])
	totalDeltasApplied := 0

	// Walk the whole chain across every snapshot checkpoint found, not just
	// the first two — the fakedata generator writes a full snapshot every
	// NUM_STATES_BETWEEN_SNAPSHOTS cycles, so this exercises every delta in
	// the fixture set against ground truth, continuing from the same
	// (already delta-merged) Blob rather than re-reading at each checkpoint.
	for _, checkpointPath := range snapshotPaths[1:] {
		endVersion := versionOf(t, checkpointPath)
		deltasThisLeg := 0
		for version != endVersion {
			deltaPath, ok := deltaByFromVersion[version]
			if !ok {
				t.Fatalf("no delta fixture found starting from version %s (chain broken after %d deltas)", version, totalDeltasApplied)
			}

			deltaBytes, err := os.ReadFile(deltaPath)
			if err != nil {
				t.Fatalf("reading %s: %v", deltaPath, err)
			}
			blob, err = blob.ApplyDelta(bytes.NewReader(deltaBytes))
			if err != nil {
				t.Fatalf("applying %s (delta #%d): %v", deltaPath, totalDeltasApplied+1, err)
			}
			totalDeltasApplied++
			deltasThisLeg++
			version = deltaRe.FindStringSubmatch(filepath.Base(deltaPath))[2]
		}

		want := readSnapshotFixture(t, checkpointPath)
		assertBlobsEqual(t, blob, want)
		t.Logf("verified against %s after %d deltas (%d total)", filepath.Base(checkpointPath), deltasThisLeg, totalDeltasApplied)
	}
}

var versionRe = regexp.MustCompile(`-(\d+)$`)

func versionOf(t *testing.T, path string) string {
	t.Helper()
	m := versionRe.FindStringSubmatch(filepath.Base(path))
	if m == nil {
		t.Fatalf("could not extract version from fixture filename: %s", path)
	}
	return m[1]
}

func readSnapshotFixture(t *testing.T, path string) *Blob {
	t.Helper()
	fileBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	blob, err := readSnapshotBlob(bytes.NewReader(fileBytes))
	if err != nil {
		t.Fatalf("readSnapshotBlob(%s): %v", path, err)
	}
	return blob
}

// assertBlobsEqual checks that got and want have identical populated
// ordinals and identical decoded field/element/entry values for every
// type, in every schema kind.
func assertBlobsEqual(t *testing.T, got, want *Blob) {
	t.Helper()

	wantByName := make(map[string]*TypeState, len(want.Types))
	for _, ts := range want.Types {
		wantByName[ts.Schema.Name()] = ts
	}

	if len(got.Types) != len(want.Types) {
		t.Fatalf("len(Types) = %d, want %d", len(got.Types), len(want.Types))
	}

	for _, g := range got.Types {
		w, ok := wantByName[g.Schema.Name()]
		if !ok {
			t.Fatalf("type %s present in got but not in want", g.Schema.Name())
		}
		if g.MaxOrdinal != w.MaxOrdinal {
			t.Fatalf("%s: MaxOrdinal = %d, want %d", g.Schema.Name(), g.MaxOrdinal, w.MaxOrdinal)
		}
		for ordinal := int32(0); ordinal <= w.MaxOrdinal; ordinal++ {
			if g.Populated.IsPopulated(ordinal) != w.Populated.IsPopulated(ordinal) {
				t.Fatalf("%s[%d]: populated = %v, want %v", g.Schema.Name(), ordinal, g.Populated.IsPopulated(ordinal), w.Populated.IsPopulated(ordinal))
			}
			if !w.Populated.IsPopulated(ordinal) {
				continue
			}

			switch {
			case w.Object != nil:
				assertObjectRecordEqual(t, g.Schema.Name(), ordinal, g.Object, w.Object)
			case w.List != nil:
				ge, we := g.List.Elements(ordinal), w.List.Elements(ordinal)
				if !int32SliceEqual(ge, we) {
					t.Fatalf("%s[%d]: List elements = %v, want %v", g.Schema.Name(), ordinal, ge, we)
				}
			case w.Set != nil:
				ge, we := g.Set.Elements(ordinal), w.Set.Elements(ordinal)
				if !int32SetEqual(ge, we) {
					t.Fatalf("%s[%d]: Set elements = %v, want %v", g.Schema.Name(), ordinal, ge, we)
				}
			case w.Map != nil:
				ge, we := g.Map.Entries(ordinal), w.Map.Entries(ordinal)
				if !mapEntrySetEqual(ge, we) {
					t.Fatalf("%s[%d]: Map entries = %v, want %v", g.Schema.Name(), ordinal, ge, we)
				}
			}
		}
	}
}

func assertObjectRecordEqual(t *testing.T, typeName string, ordinal int32, got, want *ObjectTypeData) {
	t.Helper()
	for i, field := range want.Schema.Fields {
		switch field.Type {
		case FieldTypeInt:
			gv, gok := got.GetInt(ordinal, i)
			wv, wok := want.GetInt(ordinal, i)
			if gok != wok || gv != wv {
				t.Fatalf("%s[%d].%s = (%d,%v), want (%d,%v)", typeName, ordinal, field.Name, gv, gok, wv, wok)
			}
		case FieldTypeLong:
			gv, gok := got.GetLong(ordinal, i)
			wv, wok := want.GetLong(ordinal, i)
			if gok != wok || gv != wv {
				t.Fatalf("%s[%d].%s = (%d,%v), want (%d,%v)", typeName, ordinal, field.Name, gv, gok, wv, wok)
			}
		case FieldTypeFloat:
			gv, gok := got.GetFloat(ordinal, i)
			wv, wok := want.GetFloat(ordinal, i)
			if gok != wok || gv != wv {
				t.Fatalf("%s[%d].%s = (%v,%v), want (%v,%v)", typeName, ordinal, field.Name, gv, gok, wv, wok)
			}
		case FieldTypeDouble:
			gv, gok := got.GetDouble(ordinal, i)
			wv, wok := want.GetDouble(ordinal, i)
			if gok != wok || gv != wv {
				t.Fatalf("%s[%d].%s = (%v,%v), want (%v,%v)", typeName, ordinal, field.Name, gv, gok, wv, wok)
			}
		case FieldTypeBoolean:
			gv, gok := got.GetBoolean(ordinal, i)
			wv, wok := want.GetBoolean(ordinal, i)
			if gok != wok || gv != wv {
				t.Fatalf("%s[%d].%s = (%v,%v), want (%v,%v)", typeName, ordinal, field.Name, gv, gok, wv, wok)
			}
		case FieldTypeString:
			gv, gok := got.GetString(ordinal, i)
			wv, wok := want.GetString(ordinal, i)
			if gok != wok || gv != wv {
				t.Fatalf("%s[%d].%s = (%q,%v), want (%q,%v)", typeName, ordinal, field.Name, gv, gok, wv, wok)
			}
		case FieldTypeBytes:
			gv, gok := got.GetBytes(ordinal, i)
			wv, wok := want.GetBytes(ordinal, i)
			if gok != wok || !bytes.Equal(gv, wv) {
				t.Fatalf("%s[%d].%s = (%x,%v), want (%x,%v)", typeName, ordinal, field.Name, gv, gok, wv, wok)
			}
		case FieldTypeReference:
			gv, gok := got.GetReference(ordinal, i)
			wv, wok := want.GetReference(ordinal, i)
			if gok != wok || gv != wv {
				t.Fatalf("%s[%d].%s = (%d,%v), want (%d,%v)", typeName, ordinal, field.Name, gv, gok, wv, wok)
			}
		}
	}
}

func int32SliceEqual(a, b []int32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func int32SetEqual(a, b []int32) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[int32]int, len(a))
	for _, v := range a {
		counts[v]++
	}
	for _, v := range b {
		counts[v]--
	}
	for _, c := range counts {
		if c != 0 {
			return false
		}
	}
	return true
}

func mapEntrySetEqual(a, b []MapEntry) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[MapEntry]int, len(a))
	for _, v := range a {
		counts[v]++
	}
	for _, v := range b {
		counts[v]--
	}
	for _, c := range counts {
		if c != 0 {
			return false
		}
	}
	return true
}
