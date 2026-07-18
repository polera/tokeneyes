package tokeneyes

import (
	"strings"
	"testing"
)

func TestLocalCounterGolden(t *testing.T) {
	counter := NewLocalCounter()
	model, _ := DefaultCatalog().Resolve("gpt-5.5")
	tests := []struct {
		name string
		text string
		want int64
	}{
		{name: "english", text: "hello world", want: 2},
		{name: "empty", text: "", want: 0},
		{name: "source code", text: "func main() { println(\"hello\") }", want: 9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := counter.Count(model, []byte(tt.text))
			if err != nil {
				t.Fatal(err)
			}
			if got.Tokens != tt.want {
				t.Fatalf("tokens=%d, want %d", got.Tokens, tt.want)
			}
			if got.Low != got.Tokens || got.High != got.Tokens || got.Confidence != 1 {
				t.Fatalf("exact count incorrectly bounded: %+v", got)
			}
		})
	}
}

func TestEstimatedCountersAreExplicitlyBounded(t *testing.T) {
	counter := NewLocalCounter()
	for _, name := range []string{"claude", "gemini"} {
		model, _ := DefaultCatalog().Resolve(name)
		got, err := counter.Count(model, []byte("Hello, 世界 👋\nfunc example() { return 42 }"))
		if err != nil {
			t.Fatal(err)
		}
		if got.Low > got.Tokens || got.Tokens > got.High {
			t.Fatalf("%s estimate not within range: %+v", name, got)
		}
		if got.Confidence >= 1 || got.Method == "" {
			t.Fatalf("%s estimate mislabeled: %+v", name, got)
		}
	}
}

func TestNewClaudeTokenizerEstimateIsLabeledAndScaled(t *testing.T) {
	catalog := DefaultCatalog()
	newModel, _ := catalog.Resolve("claude-opus-4-8")
	oldModel, _ := catalog.Resolve("claude-sonnet-4-6")
	content := []byte(strings.Repeat("Token estimation needs representative text. ", 100))

	newCount, err := NewLocalCounter().Count(newModel, content)
	if err != nil {
		t.Fatal(err)
	}
	oldCount, err := NewLocalCounter().Count(oldModel, content)
	if err != nil {
		t.Fatal(err)
	}
	if newCount.Tokens <= oldCount.Tokens || !strings.Contains(newCount.Method, "new-tokenizer") {
		t.Fatalf("new=%+v old=%+v", newCount, oldCount)
	}
}
