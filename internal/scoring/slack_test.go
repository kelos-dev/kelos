package scoring

import (
	"testing"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestVerdictForSlackReaction(t *testing.T) {
	cfg := &kelos.SlackScoring{
		Reactions: []kelos.SlackReactionScore{
			{Name: "+1", Verdict: kelos.ScoreVerdictPositive},
			{Name: "white_check_mark", Verdict: kelos.ScoreVerdictPositive},
			{Name: "-1", Verdict: kelos.ScoreVerdictNegative},
			{Name: "x", Verdict: kelos.ScoreVerdictNegative},
		},
	}

	tests := []struct {
		name        string
		cfg         *kelos.SlackScoring
		reaction    string
		wantVerdict kelos.ScoreVerdict
		wantOK      bool
	}{
		{
			name:        "configured positive reaction",
			cfg:         cfg,
			reaction:    "+1",
			wantVerdict: kelos.ScoreVerdictPositive,
			wantOK:      true,
		},
		{
			name:        "configured negative reaction",
			cfg:         cfg,
			reaction:    "x",
			wantVerdict: kelos.ScoreVerdictNegative,
			wantOK:      true,
		},
		{
			name:     "unconfigured reaction is ignored rather than recorded",
			cfg:      cfg,
			reaction: "eyes",
			wantOK:   false,
		},
		{
			name:        "skin tone variant on the event matches the base emoji",
			cfg:         cfg,
			reaction:    "+1::skin-tone-4",
			wantVerdict: kelos.ScoreVerdictPositive,
			wantOK:      true,
		},
		{
			// The API rejects a modifier in configuration; if one is present
			// anyway it must not alias the base name, or two entries differing
			// only by modifier could score the same reaction either way.
			name: "skin tone variant in the configuration does not alias the base emoji",
			cfg: &kelos.SlackScoring{
				Reactions: []kelos.SlackReactionScore{
					{Name: "+1::skin-tone-2", Verdict: kelos.ScoreVerdictPositive},
				},
			},
			reaction: "+1",
			wantOK:   false,
		},
		{
			name:     "nil scoring config",
			cfg:      nil,
			reaction: "+1",
			wantOK:   false,
		},
		{
			name:     "empty reaction list disables scoring",
			cfg:      &kelos.SlackScoring{Reactions: []kelos.SlackReactionScore{}},
			reaction: "+1",
			wantOK:   false,
		},
		{
			name:     "colon-wrapped configuration name never matches",
			cfg:      &kelos.SlackScoring{Reactions: []kelos.SlackReactionScore{{Name: ":+1:", Verdict: kelos.ScoreVerdictPositive}}},
			reaction: "+1",
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verdict, ok := VerdictForSlackReaction(tt.cfg, tt.reaction)
			if ok != tt.wantOK {
				t.Fatalf("VerdictForSlackReaction(%q) ok = %v, want %v", tt.reaction, ok, tt.wantOK)
			}
			if verdict != tt.wantVerdict {
				t.Errorf("VerdictForSlackReaction(%q) verdict = %q, want %q", tt.reaction, verdict, tt.wantVerdict)
			}
		})
	}
}

func TestSlackReactionEnabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  *kelos.SlackScoring
		want bool
	}{
		{name: "nil config", cfg: nil, want: false},
		{name: "nil reactions", cfg: &kelos.SlackScoring{}, want: false},
		{
			name: "empty list",
			cfg:  &kelos.SlackScoring{Reactions: []kelos.SlackReactionScore{}},
			want: false,
		},
		{
			name: "one mapping",
			cfg:  &kelos.SlackScoring{Reactions: []kelos.SlackReactionScore{{Name: "+1", Verdict: kelos.ScoreVerdictPositive}}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SlackReactionEnabled(tt.cfg); got != tt.want {
				t.Errorf("SlackReactionEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNormalizeReaction(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "+1", want: "+1"},
		{in: "+1::skin-tone-2", want: "+1"},
		{in: "white_check_mark", want: "white_check_mark"},
		{in: "", want: ""},
	}

	for _, tt := range tests {
		if got := NormalizeReaction(tt.in); got != tt.want {
			t.Errorf("NormalizeReaction(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
