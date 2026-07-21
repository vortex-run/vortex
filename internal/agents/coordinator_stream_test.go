package agents

import (
	"context"
	"strings"
	"testing"
	"time"
)

// streamingStub implements both AIGateway and StreamingAIGateway: classify
// prompts answer GENERAL_QUESTION; the answer path emits scripted deltas.
type streamingStub struct {
	deltas []string
}

func (g streamingStub) Complete(_ context.Context, _, systemPrompt string) (string, error) {
	if strings.Contains(strings.ToLower(systemPrompt), "classify") {
		return string(IntentGeneralQuestion), nil
	}
	return strings.Join(g.deltas, ""), nil
}

func (g streamingStub) CompleteStream(_ context.Context, _, _ string, onDelta func(string)) (string, error) {
	for _, d := range g.deltas {
		onDelta(d)
	}
	return strings.Join(g.deltas, ""), nil
}

func TestHandleMessageStream_StreamsFilteredDeltas(t *testing.T) {
	// Deltas split mid-word and mid-line; one line is an internal artifact
	// that must never reach the stream.
	c := newTestCoordinator(t, streamingStub{deltas: []string{
		"The answer", " is 42.\nGoal: internal", " leak\nEnjoy!",
	}})
	var got []string
	reply, err := c.HandleMessageStream(context.Background(), "what is 6 times 7?", "s1",
		func(d string) { got = append(got, d) })
	if err != nil {
		t.Fatal(err)
	}
	want := "The answer is 42.\nEnjoy!"
	if reply != want {
		t.Errorf("reply = %q, want %q", reply, want)
	}
	if streamed := strings.Join(got, ""); streamed != want {
		t.Errorf("streamed = %q, want %q", streamed, want)
	}
	// Line granularity: the artifact line was withheld entirely, and content
	// arrived in more than one delta (it actually streamed).
	if len(got) < 2 {
		t.Errorf("deltas = %q, want incremental delivery", got)
	}
	for _, d := range got {
		if strings.Contains(strings.ToLower(d), "goal:") {
			t.Errorf("internal artifact reached the stream: %q", d)
		}
	}
}

func TestHandleMessageStream_SkipBlockNeverStreams(t *testing.T) {
	c := newTestCoordinator(t, streamingStub{deltas: []string{
		"Use this procedure unless told otherwise:\n1. step one\n2. step two\n\nJust the answer.",
	}})
	var got []string
	reply, err := c.HandleMessageStream(context.Background(), "what is 6 times 7?", "s1",
		func(d string) { got = append(got, d) })
	if err != nil {
		t.Fatal(err)
	}
	if reply != "Just the answer." {
		t.Errorf("reply = %q", reply)
	}
	if streamed := strings.Join(got, ""); streamed != "Just the answer." {
		t.Errorf("streamed = %q", streamed)
	}
}

func TestHandleMessageStream_NonStreamingGatewaySingleDelta(t *testing.T) {
	// A gateway without CompleteStream degrades to one filtered delta.
	c := newTestCoordinator(t, StubAIGateway{AnswerReply: "plain reply"})
	var got []string
	reply, err := c.HandleMessageStream(context.Background(), "what is 6 times 7?", "s1",
		func(d string) { got = append(got, d) })
	if err != nil {
		t.Fatal(err)
	}
	if reply != "plain reply" {
		t.Errorf("reply = %q", reply)
	}
	if len(got) != 1 || got[0] != "plain reply" {
		t.Errorf("deltas = %q, want exactly one full delta", got)
	}
}

func TestHandleMessageStream_NilDeltaEqualsHandleMessage(t *testing.T) {
	c := newTestCoordinator(t, StubAIGateway{AnswerReply: "same"})
	reply, err := c.HandleMessageStream(context.Background(), "what is 6 times 7?", "s1", nil)
	if err != nil || reply != "same" {
		t.Errorf("reply = %q err = %v", reply, err)
	}
}

// TestStreamFilterMatchesBatchFilter is the equivalence property: for any
// chunking of any input, the concatenated stream-filter output must equal
// filterCoordinatorResponse of the whole input.
func TestStreamFilterMatchesBatchFilter(t *testing.T) {
	inputs := []string{
		"plain single line",
		"multi\nline\nanswer",
		"Goal: hidden\nvisible",
		"visible\nGoal: hidden",
		"a\n\nb\n\n",
		"\n\nleading blanks\n",
		"Use this procedure now:\nstep\nmore\n\nafter block",
		"text with (tool: read_file) inline\nclean line",
		"Steps:\n1. one\n2. two\n\ndone",
		"Tasks: 3 completed, 1 failed\nreal answer",
		"",
		"\n\n\n",
		"answer   ", // trailing intra-line spaces: batch trims, stream may keep
	}
	chunkings := []int{1, 2, 3, 5, 100}
	for _, in := range inputs {
		want := filterCoordinatorResponse(in)
		for _, n := range chunkings {
			var b strings.Builder
			sf := newStreamFilter(func(d string) { b.WriteString(d) })
			for i := 0; i < len(in); i += n {
				end := i + n
				if end > len(in) {
					end = len(in)
				}
				sf.Write(in[i:end])
			}
			sf.Close()
			// The stream cannot trim intra-line leading/trailing spaces the
			// way the batch path's final TrimSpace does; compare trimmed.
			if got := strings.TrimSpace(b.String()); got != want {
				t.Errorf("input %q chunk %d: stream = %q, batch = %q", in, n, got, want)
			}
		}
	}
}

func TestRuntimeSubmit_StreamsDeltas(t *testing.T) {
	c := newTestCoordinator(t, streamingStub{deltas: []string{"one\n", "two\n", "three"}})
	r, err := NewRuntime(RuntimeConfig{Bus: NewBus(), Coordinator: c})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.Stop(ctx)
	}()

	ch, err := r.Submit(context.Background(), "what is 6 times 7?", "s1")
	if err != nil {
		t.Fatal(err)
	}
	var chunks []string
	for chunk := range ch {
		chunks = append(chunks, chunk)
	}
	if got := strings.Join(chunks, ""); got != "one\ntwo\nthree" {
		t.Errorf("joined chunks = %q", got)
	}
	if len(chunks) < 2 {
		t.Errorf("chunks = %q, want incremental delivery over the channel", chunks)
	}
}
