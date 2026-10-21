package logview

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestReplacementHunks(t *testing.T) {
	tests := []struct {
		name     string
		old, new string
		want     []DiffLine
	}{
		{
			name: "shared lines become context",
			old:  "a\nb\nc",
			new:  "a\nB\nc",
			want: []DiffLine{{DiffContext, "a"}, {DiffRemove, "b"}, {DiffAdd, "B"}, {DiffContext, "c"}},
		},
		{
			name: "pure insertion",
			old:  "a",
			new:  "a\nb",
			want: []DiffLine{{DiffContext, "a"}, {DiffAdd, "b"}},
		},
		{
			name: "context is limited",
			old:  "1\n2\n3\n4\nx\n5\n6\n7\n8",
			new:  "1\n2\n3\n4\ny\n5\n6\n7\n8",
			want: []DiffLine{
				{DiffContext, "2"}, {DiffContext, "3"}, {DiffContext, "4"},
				{DiffRemove, "x"}, {DiffAdd, "y"},
				{DiffContext, "5"}, {DiffContext, "6"}, {DiffContext, "7"},
			},
		},
		{
			name: "deletion of everything",
			old:  "a\nb",
			new:  "",
			want: []DiffLine{{DiffRemove, "a"}, {DiffRemove, "b"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hunks := replacementHunks(tt.old, tt.new)
			if len(hunks) != 1 {
				t.Fatalf("hunks = %+v, want one hunk", hunks)
			}
			got := hunks[0]
			if got.OldStart != 0 || got.NewStart != 0 {
				t.Errorf("positions = %d,%d, want unknown", got.OldStart, got.NewStart)
			}
			if !reflect.DeepEqual(got.Lines, tt.want) {
				t.Errorf("lines = %v, want %v", got.Lines, tt.want)
			}
		})
	}
}

func TestReplacementHunksKeepsSharedMiddleLines(t *testing.T) {
	hunks := replacementHunks("first\nshared one\nshared two\nlast", "FIRST\nshared one\nshared two\nLAST")
	if len(hunks) != 1 {
		t.Fatalf("hunks = %+v, want one hunk", hunks)
	}
	want := []DiffLine{
		{DiffRemove, "first"}, {DiffAdd, "FIRST"},
		{DiffContext, "shared one"}, {DiffContext, "shared two"},
		{DiffRemove, "last"}, {DiffAdd, "LAST"},
	}
	if !reflect.DeepEqual(hunks[0].Lines, want) {
		t.Errorf("lines = %+v, want %+v", hunks[0].Lines, want)
	}
	if added, removed := diffStats(hunks); added != 2 || removed != 2 {
		t.Errorf("stats = %d additions, %d removals, want 2 each", added, removed)
	}
}

func TestReplacementHunksNoChange(t *testing.T) {
	if got := replacementHunks("same\nlines", "same\nlines"); got != nil {
		t.Errorf("identical text produced hunks: %+v", got)
	}
}

func TestReplacementHunksSeparateDistantEdits(t *testing.T) {
	oldText := "first\n1\n2\n3\n4\n5\n6\n7\n8\nlast"
	newText := "FIRST\n1\n2\n3\n4\n5\n6\n7\n8\nLAST"
	hunks := replacementHunks(oldText, newText)
	if len(hunks) != 2 {
		t.Fatalf("hunks = %+v, want two separated hunks", hunks)
	}
	if added, removed := diffStats(hunks); added != 2 || removed != 2 {
		t.Errorf("stats = %d additions, %d removals, want 2 each", added, removed)
	}
}

func TestParseStructuredPatch(t *testing.T) {
	raw := json.RawMessage(`[{"oldStart":3,"newStart":4,"lines":[" a","-b","+c",""]}]`)
	want := []Hunk{{
		OldStart: 3,
		NewStart: 4,
		Lines:    []DiffLine{{DiffContext, "a"}, {DiffRemove, "b"}, {DiffAdd, "c"}, {DiffContext, ""}},
	}}
	if got := parseStructuredPatch(raw); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := parseStructuredPatch(json.RawMessage(`"not a patch"`)); got != nil {
		t.Errorf("malformed patch = %+v, want nil", got)
	}
	if got := parseStructuredPatch(json.RawMessage(`null`)); got != nil {
		t.Errorf("null patch = %+v, want nil", got)
	}
	if got := parseStructuredPatch(json.RawMessage(`[]`)); got == nil || len(got) != 0 {
		t.Errorf("empty patch = %#v, want empty non-nil slice", got)
	}
}
