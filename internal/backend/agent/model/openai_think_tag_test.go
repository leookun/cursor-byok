package modeladapter

import (
	"reflect"
	"strings"
	"testing"
)

// partsKindText flattens a slice of openAIContentPart into (Kind, Text) pairs.
func partsKindText(parts []openAIContentPart) []struct {
	Kind openAIContentPartKind
	Text string
} {
	out := make([]struct {
		Kind openAIContentPartKind
		Text string
	}, 0, len(parts))
	for _, p := range parts {
		out = append(out, struct {
			Kind openAIContentPartKind
			Text string
		}{Kind: p.Kind, Text: p.Text})
	}
	return out
}

// thoughtTagOpen / thoughtTagClose spell out the angle-bracket tags via byte
// escapes so this test file stays robust against editors that mangle literal
// angle brackets in Go source.
const (
	thoughtTagOpen  = "\x3cthought\x3e"
	thoughtTagClose = "\x3c/thought\x3e"
)

// wrapThink builds openAIThinkOpenTag + inner + openAIThinkCloseTag.
func wrapThink(inner string) string {
	return openAIThinkOpenTag + inner + openAIThinkCloseTag
}

// wrapThought builds "<thought>" + inner + "</thought>".
func wrapThought(inner string) string {
	return thoughtTagOpen + inner + thoughtTagClose
}

func TestOpenAIThinkTagParserDefaultThinkTag(t *testing.T) {
	parser := &openAIThinkTagParser{}
	parts := parser.Consume(wrapThink("reasoning here"))
	want := []struct {
		Kind openAIContentPartKind
		Text string
	}{
		{openAIContentPartReasoning, "reasoning here"},
		{openAIContentPartThinkingCompleted, ""},
	}
	got := partsKindText(parts)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("think tag: got %+v, want %+v", got, want)
	}
}

func TestOpenAIThinkTagParserThoughtTag(t *testing.T) {
	parser := &openAIThinkTagParser{}
	parts := parser.Consume(wrapThought("reasoning here"))
	want := []struct {
		Kind openAIContentPartKind
		Text string
	}{
		{openAIContentPartReasoning, "reasoning here"},
		{openAIContentPartThinkingCompleted, ""},
	}
	got := partsKindText(parts)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("thought tag: got %+v, want %+v", got, want)
	}
}

func TestOpenAIThinkTagParserNoTags(t *testing.T) {
	parser := &openAIThinkTagParser{}
	parts := parser.Consume("plain text")
	want := []struct {
		Kind openAIContentPartKind
		Text string
	}{
		{openAIContentPartText, "plain text"},
	}
	got := partsKindText(parts)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("no tags: got %+v, want %+v", got, want)
	}
}

func TestOpenAIThinkTagParserEmptyReasoning(t *testing.T) {
	parser := &openAIThinkTagParser{}
	parts := parser.Consume(wrapThink(""))
	want := []struct {
		Kind openAIContentPartKind
		Text string
	}{
		{openAIContentPartThinkingCompleted, ""},
	}
	got := partsKindText(parts)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("empty reasoning: got %+v, want %+v", got, want)
	}
}

func TestOpenAIThinkTagParserMixedTags(t *testing.T) {
	parser := &openAIThinkTagParser{}
	input := "before " + wrapThink(" think inside ") + " after " + wrapThought(" more thought ") + " tail"
	parts := parser.Consume(input)
	want := []struct {
		Kind openAIContentPartKind
		Text string
	}{
		{openAIContentPartText, "before "},
		{openAIContentPartReasoning, " think inside "},
		{openAIContentPartThinkingCompleted, ""},
		{openAIContentPartText, " after "},
		{openAIContentPartReasoning, " more thought "},
		{openAIContentPartThinkingCompleted, ""},
		{openAIContentPartText, " tail"},
	}
	got := partsKindText(parts)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mixed: got %+v, want %+v", got, want)
	}
}

func TestOpenAIThinkTagParserStreamingSplit(t *testing.T) {
	cases := []struct {
		name   string
		chunks []string
		want   []struct {
			Kind openAIContentPartKind
			Text string
		}
	}{
		{
			name: "think tag split across 3 deltas",
			chunks: []string{
				"before " + openAIThinkOpenTag[:len(openAIThinkOpenTag)-2],
				openAIThinkOpenTag[len(openAIThinkOpenTag)-2:] + " reason",
				openAIThinkCloseTag + " tail",
			},
			want: []struct {
				Kind openAIContentPartKind
				Text string
			}{
				{openAIContentPartText, "before "},
				{openAIContentPartReasoning, " reason"},
				{openAIContentPartThinkingCompleted, ""},
				{openAIContentPartText, " tail"},
			},
		},
		{
			name: "thought tag split across deltas",
			chunks: []string{
				"a" + thoughtTagOpen[:3],
				thoughtTagOpen[3:] + "reason",
				thoughtTagClose + "b",
			},
			want: []struct {
				Kind openAIContentPartKind
				Text string
			}{
				{openAIContentPartText, "a"},
				{openAIContentPartReasoning, "reason"},
				{openAIContentPartThinkingCompleted, ""},
				{openAIContentPartText, "b"},
			},
		},
		{
			name: "close tag split across deltas",
			chunks: []string{
				openAIThinkOpenTag + " reason " + openAIThinkCloseTag[:len(openAIThinkCloseTag)-2],
				openAIThinkCloseTag[len(openAIThinkCloseTag)-2:] + " tail",
			},
			want: []struct {
				Kind openAIContentPartKind
				Text string
			}{
				{openAIContentPartReasoning, " reason "},
				{openAIContentPartThinkingCompleted, ""},
				{openAIContentPartText, " tail"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parser := &openAIThinkTagParser{}
			var got []struct {
				Kind openAIContentPartKind
				Text string
			}
			for _, chunk := range tc.chunks {
				got = append(got, partsKindText(parser.Consume(chunk))...)
			}
			got = append(got, partsKindText(parser.Flush())...)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestOpenAIThinkTagParserFlushRemainder(t *testing.T) {
	parser := &openAIThinkTagParser{}
	parts := parser.Consume(openAIThinkOpenTag + " still thinking")
	flushed := parser.Flush()
	got := partsKindText(append(parts, flushed...))
	want := []struct {
		Kind openAIContentPartKind
		Text string
	}{
		{openAIContentPartReasoning, " still thinking"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("flush remainder: got %+v, want %+v", got, want)
	}
}

func TestOpenAIThinkTagParserCustomTags(t *testing.T) {
	parser := &openAIThinkTagParser{
		Tags: []openAIThinkTagPair{
			{Open: "\x3creason\x3e", Close: "\x3c/reason\x3e"},
		},
	}
	parts := parser.Consume("x\x3creason\x3eyyy\x3c/reason\x3ez")
	want := []struct {
		Kind openAIContentPartKind
		Text string
	}{
		{openAIContentPartText, "x"},
		{openAIContentPartReasoning, "yyy"},
		{openAIContentPartThinkingCompleted, ""},
		{openAIContentPartText, "z"},
	}
	got := partsKindText(parts)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("custom tags: got %+v, want %+v", got, want)
	}
}

func TestOpenAIThinkTagParserNilSafe(t *testing.T) {
	var parser *openAIThinkTagParser
	if parts := parser.Consume("x"); parts != nil {
		t.Fatalf("nil Consume: expected nil, got %+v", parts)
	}
	if parts := parser.Flush(); parts != nil {
		t.Fatalf("nil Flush: expected nil, got %+v", parts)
	}
}

func TestOpenAIThinkTagParserEarliestMatch(t *testing.T) {
	parser := &openAIThinkTagParser{}
	input := wrapThought("first") + wrapThink("second")
	parts := parser.Consume(input)
	want := []struct {
		Kind openAIContentPartKind
		Text string
	}{
		{openAIContentPartReasoning, "first"},
		{openAIContentPartThinkingCompleted, ""},
		{openAIContentPartReasoning, "second"},
		{openAIContentPartThinkingCompleted, ""},
	}
	got := partsKindText(parts)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("earliest match: got %+v, want %+v", got, want)
	}
}

// guard: ensure the helper consts really spell the tags we think they do.
func init() {
	if thoughtTagOpen != "\x3cthought\x3e" || !strings.HasPrefix(thoughtTagOpen, "\x3c") {
		panic("thoughtTagOpen is wrong: " + thoughtTagOpen)
	}
}
