package tokeneyes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestAnthropicVerificationContract(t *testing.T) {
	v := NewHTTPVerifier()
	v.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/messages/count_tokens" || r.Header.Get("x-api-key") != "key" {
			t.Errorf("bad request metadata: %s %s", r.URL.Path, r.Header.Get("x-api-key"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "claude-opus-4-7" {
			t.Errorf("model=%v", body["model"])
		}
		return response(200, `{"input_tokens":42}`), nil
	})}
	v.LookupEnv = func(name string) string {
		if name == "ANTHROPIC_API_KEY" {
			return "key"
		}
		return ""
	}
	m, _ := DefaultCatalog().Resolve("claude")
	got, err := v.Verify(context.Background(), m, AssembledRequest{System: "system", Content: "content", Tools: `[{"name":"x","description":"x","input_schema":{"type":"object"}}]`})
	if err != nil || got.Tokens != 42 || !strings.Contains(got.Method, "anthropic") {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestGeminiVerificationContractAndErrors(t *testing.T) {
	v := NewHTTPVerifier()
	v.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("key") != "key" {
			t.Error("missing key")
		}
		return response(200, `{"totalTokens":77}`), nil
	})}
	v.LookupEnv = func(name string) string {
		if name == "GEMINI_API_KEY" {
			return "key"
		}
		return ""
	}
	m, _ := DefaultCatalog().Resolve("gemini")
	got, err := v.Verify(context.Background(), m, AssembledRequest{Content: "hello"})
	if err != nil || got.Tokens != 77 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	v.LookupEnv = func(string) string { return "" }
	if _, err := v.Verify(context.Background(), m, AssembledRequest{}); err == nil {
		t.Fatal("missing credentials accepted")
	}
}

func TestProviderRateLimitError(t *testing.T) {
	v := NewHTTPVerifier()
	v.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(429, `{"error":{"message":"rate limited"}}`), nil
	})}
	v.LookupEnv = func(string) string { return "key" }
	m, _ := DefaultCatalog().Resolve("gemini")
	_, err := v.Verify(context.Background(), m, AssembledRequest{})
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("err=%v", err)
	}
}

func TestGeminiVerificationUsesMixedInlineParts(t *testing.T) {
	v := NewHTTPVerifier()
	v.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(body)
		if !strings.Contains(string(encoded), `"inlineData"`) || !strings.Contains(string(encoded), `"mimeType":"image/png"`) {
			t.Fatalf("payload=%s", encoded)
		}
		return response(200, `{"totalTokens":9}`), nil
	})}
	v.LookupEnv = func(string) string { return "key" }
	m, _ := DefaultCatalog().Resolve("gemini")
	got, err := v.Verify(context.Background(), m, AssembledRequest{Parts: []RequestPart{{Type: "text", Text: "look"}, {Type: "image", MIME: "image/png", Data: []byte("png")}}})
	if err != nil || got.Tokens != 9 || got.Transport != "inline" || got.CleanupStatus != "not_required" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestGeminiUploadRequiresSeparateAuthorizationAndCleansUp(t *testing.T) {
	m, _ := DefaultCatalog().Resolve("gemini")
	m.Media.MaxInlineBytes = 1
	part := RequestPart{AssetID: "asset-x", Type: "image", MIME: "image/png", Data: []byte("large")}
	v := NewHTTPVerifier()
	v.LookupEnv = func(string) string { return "key" }
	calls := 0
	v.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		switch {
		case strings.Contains(r.URL.Path, "/upload/v1beta/files"):
			resp := response(200, "")
			resp.Header.Set("X-Goog-Upload-URL", "https://upload.test/session")
			return resp, nil
		case r.URL.Host == "upload.test":
			return response(200, `{"file":{"name":"files/temp","uri":"gemini://temp"}}`), nil
		case strings.Contains(r.URL.Path, ":countTokens"):
			return response(200, `{"totalTokens":12}`), nil
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/files/temp"):
			return response(200, "{}"), nil
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL)
			return nil, nil
		}
	})}
	if _, err := v.Verify(context.Background(), m, AssembledRequest{Parts: []RequestPart{part}}); err == nil || !strings.Contains(err.Error(), "--allow-file-upload") {
		t.Fatalf("upload accepted without authorization: %v", err)
	}
	if calls != 0 {
		t.Fatalf("network called before authorization: %d", calls)
	}
	got, err := v.Verify(context.Background(), m, AssembledRequest{Parts: []RequestPart{part}, AllowFileUpload: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Tokens != 12 || got.Transport != "upload" || got.CleanupStatus != "deleted" || calls != 4 {
		t.Fatalf("got=%+v calls=%d", got, calls)
	}
}
