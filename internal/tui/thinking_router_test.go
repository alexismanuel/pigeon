package tui

import (
	"strings"
	"testing"
)

// collect runs all tokens through the router and returns (regularTokens, thinkingTokens).
func collect(tokens []string) (string, string) {
	var reg, think strings.Builder
	r := newXMLThinkingRouter(
		func(t string) { reg.WriteString(t) },
		func(t string) { think.WriteString(t) },
	)
	for _, tok := range tokens {
		r.feed(tok)
	}
	return reg.String(), think.String()
}

func TestNoThinkingTags(t *testing.T) {
	reg, think := collect([]string{"Hello", " world"})
	if reg != "Hello world" {
		t.Errorf("regular = %q, want %q", reg, "Hello world")
	}
	if think != "" {
		t.Errorf("thinking = %q, want empty", think)
	}
}

func TestThinkingBlockInSingleToken(t *testing.T) {
	reg, think := collect([]string{"<thinking>deep thought</thinking>answer"})
	if think != "deep thought" {
		t.Errorf("thinking = %q, want %q", think, "deep thought")
	}
	if reg != "answer" {
		t.Errorf("regular = %q, want %q", reg, "answer")
	}
}

func TestThinkingBlockAcrossTokens(t *testing.T) {
	tokens := []string{"before", "<", "think", "ing>", "inside", "</thinking>", "after"}
	reg, think := collect(tokens)
	if think != "inside" {
		t.Errorf("thinking = %q, want %q", think, "inside")
	}
	if reg != "beforeafter" {
		t.Errorf("regular = %q, want %q", reg, "beforeafter")
	}
}

func TestThinkingTagSplitAcrossTokens(t *testing.T) {
	// Tag split character by character.
	tokens := []string{"<", "t", "h", "i", "n", "k", "i", "n", "g", ">", "thought", "<", "/", "t", "h", "i", "n", "k", "i", "n", "g", ">", "rest"}
	reg, think := collect(tokens)
	if think != "thought" {
		t.Errorf("thinking = %q, want %q", think, "thought")
	}
	if reg != "rest" {
		t.Errorf("regular = %q, want %q", reg, "rest")
	}
}

func TestTextBeforeThinkingBlock(t *testing.T) {
	tokens := []string{"preamble <thinking>reason</thinking> conclusion"}
	reg, think := collect(tokens)
	if think != "reason" {
		t.Errorf("thinking = %q, want %q", think, "reason")
	}
	if reg != "preamble  conclusion" {
		t.Errorf("regular = %q, want %q", reg, "preamble  conclusion")
	}
}

func TestMultipleThinkingBlocks(t *testing.T) {
	tokens := []string{"<thinking>first</thinking>mid<thinking>second</thinking>end"}
	reg, think := collect(tokens)
	if think != "firstsecond" {
		t.Errorf("thinking = %q, want %q", think, "firstsecond")
	}
	if reg != "midend" {
		t.Errorf("regular = %q, want %q", reg, "midend")
	}
}

func TestPartialOpenTagHeldBack(t *testing.T) {
	// The router should not emit the partial "<think" until it knows it's not
	// the opening tag. Feed "<think" then a character that makes it not a tag.
	var reg, think strings.Builder
	r := newXMLThinkingRouter(
		func(t string) { reg.WriteString(t) },
		func(t string) { think.WriteString(t) },
	)
	r.feed("<think") // could be start of <thinking>
	if reg.String() != "" {
		t.Errorf("should hold back partial tag, got regular=%q", reg.String())
	}
	r.feed("er>") // completes to <thinker> — not a thinking tag
	if reg.String() != "<thinker>" {
		t.Errorf("regular = %q, want %q", reg.String(), "<thinker>")
	}
	if think.String() != "" {
		t.Errorf("thinking should be empty, got %q", think.String())
	}
}

func TestXMLPartialSuffix(t *testing.T) {
	cases := []struct {
		s, tag string
		want   int
	}{
		{"hello <thi", "<thinking>", 4},
		{"hello <thinking", "<thinking>", 9},
		{"hello", "<thinking>", 0},
		{"<", "<thinking>", 1},
		{"x</thinkin", "</thinking>", 9},
	}
	for _, tc := range cases {
		got := xmlPartialSuffix(tc.s, tc.tag)
		if got != tc.want {
			t.Errorf("xmlPartialSuffix(%q, %q) = %d, want %d", tc.s, tc.tag, got, tc.want)
		}
	}
}
