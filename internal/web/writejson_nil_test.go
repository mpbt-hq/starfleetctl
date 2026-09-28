package web

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// TestWriteJSONNilSliceEncodesAsEmptyArray is a regression test for the
// "Cannot read properties of null (reading 'length')" / "rows is null" crash
// in the fleet console.
//
// A handler that has nothing to return hands writeJSON a nil slice, and a nil
// slice encodes as JSON `null`. The frontend then stores that as
// `taskState.rows` and evaluates `rows.length`, which throws — so an empty
// result looked like a broken application rather than an empty list, and the
// real error was hidden behind a JavaScript TypeError.
func TestWriteJSONNilSliceEncodesAsEmptyArray(t *testing.T) {
	cases := []struct {
		name string
		in   any
	}{
		{"nil slice of struct", []map[string]any(nil)},
		{"nil string slice", []string(nil)},
		{"nil slice of pointers", []*int(nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeJSON(rec, tc.in)

			var got any
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("response is not valid JSON: %v (body %q)", err, rec.Body.String())
			}
			arr, ok := got.([]any)
			if !ok {
				t.Fatalf("encoded as %T, want a JSON array so the frontend can call .length on it (body %q)",
					got, rec.Body.String())
			}
			if len(arr) != 0 {
				t.Errorf("expected an empty array, got %d elements", len(arr))
			}
		})
	}
}

// TestWriteJSONKeepsNonEmptyAndNonSliceValues guards against the nil-slice
// normalisation being too eager: real data, maps and nil pointers must pass
// through unchanged.
func TestWriteJSONKeepsNonEmptyAndNonSliceValues(t *testing.T) {
	t.Run("non-empty slice survives", func(t *testing.T) {
		rec := httptest.NewRecorder()
		writeJSON(rec, []string{"a", "b"})
		var got []string
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(got) != 2 || got[0] != "a" || got[1] != "b" {
			t.Errorf("got %v, want [a b]", got)
		}
	})

	t.Run("map is untouched", func(t *testing.T) {
		rec := httptest.NewRecorder()
		writeJSON(rec, map[string]any{"ok": true})
		if got := rec.Body.String(); !json.Valid([]byte(got)) || got == "null\n" {
			t.Errorf("map was altered: %q", got)
		}
	})

	t.Run("nil byte slice stays a base64 string", func(t *testing.T) {
		// encoding/json renders []byte as base64, so it is data, not a list.
		// Normalising it would change null into "".
		rec := httptest.NewRecorder()
		writeJSON(rec, []byte(nil))
		if got := rec.Body.String(); got != "null\n" {
			t.Errorf("nil []byte encoded as %q, want %q", got, "null\n")
		}
	})

	t.Run("plain nil stays null", func(t *testing.T) {
		// A bare nil interface is not a slice; it must not be turned into
		// anything, so the API keeps its documented behaviour for it.
		rec := httptest.NewRecorder()
		writeJSON(rec, nil)
		if got := rec.Body.String(); got != "null\n" {
			t.Errorf("bare nil encoded as %q, want %q", got, "null\n")
		}
	})
}
