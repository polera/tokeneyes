package tokeneyes

import (
	"fmt"
	"math"
	"regexp"
	"unicode"
	"unicode/utf8"

	"github.com/tiktoken-go/tokenizer"
)

type LocalCounter struct{}

func NewLocalCounter() *LocalCounter { return &LocalCounter{} }

func (LocalCounter) Count(model Model, content []byte) (Count, error) {
	switch model.Tokenizer {
	case "o200k_base":
		codec, err := tokenizer.Get(tokenizer.O200kBase)
		if err != nil {
			return Count{}, err
		}
		ids, _, err := codec.Encode(string(content))
		if err != nil {
			return Count{}, err
		}
		n := int64(len(ids))
		return Count{Tokens: n, Low: n, High: n, Method: "local-bpe:o200k_base", Confidence: 1}, nil
	case "cl100k_base":
		codec, err := tokenizer.Get(tokenizer.Cl100kBase)
		if err != nil {
			return Count{}, err
		}
		ids, _, err := codec.Encode(string(content))
		if err != nil {
			return Count{}, err
		}
		n := int64(len(ids))
		return Count{Tokens: n, Low: n, High: n, Method: "local-bpe:cl100k_base", Confidence: 1}, nil
	case "claude-estimator-v1":
		return estimate(content, "calibrated:claude-v1", 0.15, 0.94), nil
	case "claude-estimator-v2":
		// Anthropic documents that Opus 4.7 and later use a newer tokenizer
		// which produces approximately 30% more tokens, depending on content.
		return estimateScaled(content, "estimated:claude-new-tokenizer-v1", 1.30, 0.25, 0.85), nil
	case "gemini-estimator-v1":
		return estimate(content, "calibrated:gemini-v1", 0.18, 0.92), nil
	default:
		return Count{}, fmt.Errorf("unsupported tokenizer %q for %s", model.Tokenizer, model.ID)
	}
}

var codeSignals = regexp.MustCompile(`[{}\[\]();:=<>]|=>|::|\b(func|function|class|const|var|let|package|import|def)\b`)

// estimate is intentionally a bounded estimate: it accounts for ASCII word
// pieces, punctuation-heavy code, and CJK/emoji runes independently.
func estimate(content []byte, method string, errorRate, confidence float64) Count {
	return estimateScaled(content, method, 1, errorRate, confidence)
}

func estimateScaled(content []byte, method string, scale, errorRate, confidence float64) Count {
	if len(content) == 0 {
		return Count{Method: method, Confidence: confidence}
	}
	s := string(content)
	var asciiLetters, whitespace, punctuation, nonASCII int64
	for _, r := range s {
		switch {
		case r > unicode.MaxASCII:
			nonASCII++
		case unicode.IsSpace(r):
			whitespace++
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			asciiLetters++
		default:
			punctuation++
		}
	}
	// Natural-language words average about four ASCII characters per token;
	// punctuation and non-ASCII scripts fragment more often.
	value := float64(asciiLetters)/4.05 + float64(punctuation)*0.72 + float64(nonASCII)*0.76 + float64(whitespace)/18
	if codeSignals.Match(content) {
		value *= 1.07
	}
	if !utf8.Valid(content) {
		value = float64(len(content)) / 3
	}
	value *= scale
	n := int64(math.Ceil(value))
	if n == 0 {
		n = 1
	}
	low := int64(math.Floor(float64(n) * (1 - errorRate)))
	high := int64(math.Ceil(float64(n) * (1 + errorRate)))
	if low < 1 {
		low = 1
	}
	return Count{Tokens: n, Low: low, High: high, Method: method, Confidence: confidence}
}
