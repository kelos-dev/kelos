package logview

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestReplacementHunk(t *testing.T) {
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
			got := replacementHunk(tt.old, tt.new)
			if got.OldStart != 0 || got.NewStart != 0 {
				t.Errorf("positions = %d,%d, want unknown", got.OldStart, got.NewStart)
			}
			if !reflect.DeepEqual(got.Lines, tt.want) {
				t.Errorf("lines = %v, want %v", got.Lines, tt.want)
			}
		})
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
}
