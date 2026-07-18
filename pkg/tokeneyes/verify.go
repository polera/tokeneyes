package tokeneyes

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type HTTPVerifier struct {
	Client        *http.Client
	AnthropicBase string
	GeminiBase    string
	LookupEnv     func(string) string
}

func NewHTTPVerifier() *HTTPVerifier {
	return &HTTPVerifier{Client: &http.Client{Timeout: 30 * time.Second}, AnthropicBase: "https://api.anthropic.com", GeminiBase: "https://generativelanguage.googleapis.com", LookupEnv: os.Getenv}
}

func (v *HTTPVerifier) Verify(ctx context.Context, model Model, assembled AssembledRequest) (VerificationResult, error) {
	if v.Client == nil {
		v.Client = &http.Client{Timeout: 30 * time.Second}
	}
	if v.LookupEnv == nil {
		v.LookupEnv = os.Getenv
	}
	switch model.Verification {
	case "anthropic-count":
		return v.verifyAnthropic(ctx, model, assembled)
	case "gemini-count":
		return v.verifyGemini(ctx, model, assembled)
	default:
		return VerificationResult{}, fmt.Errorf("%s does not support provider token verification", model.ID)
	}
}

func (v *HTTPVerifier) verifyAnthropic(ctx context.Context, model Model, a AssembledRequest) (result VerificationResult, err error) {
	key := v.LookupEnv("ANTHROPIC_API_KEY")
	if key == "" {
		return VerificationResult{}, fmt.Errorf("ANTHROPIC_API_KEY is not set")
	}
	upload := partsBytes(a.Parts) > model.Media.MaxInlineBytes && model.Media.MaxInlineBytes > 0
	if upload && !a.AllowFileUpload {
		return VerificationResult{}, fmt.Errorf("mixed input exceeds %d-byte inline limit; retry with --verify --allow-file-upload", model.Media.MaxInlineBytes)
	}
	var uploaded []string
	if upload {
		result.Transport = "upload"
		result.CleanupStatus = "pending"
		defer func() {
			cleanupErr := v.deleteAnthropicFiles(uploaded)
			if cleanupErr != nil {
				result.CleanupStatus = "failed"
				if err == nil {
					err = fmt.Errorf("provider count succeeded but temporary-file cleanup failed: %w", cleanupErr)
				}
			} else {
				result.CleanupStatus = "deleted"
			}
		}()
	} else {
		result.Transport = "inline"
		result.CleanupStatus = "not_required"
	}
	content := make([]any, 0, len(a.Parts)+1)
	if len(a.Parts) == 0 {
		content = append(content, map[string]any{"type": "text", "text": a.Content})
	}
	for _, part := range a.Parts {
		switch part.Type {
		case "text":
			if part.Text != "" {
				content = append(content, map[string]any{"type": "text", "text": part.Text})
			}
		case "image":
			if upload {
				id, e := v.uploadAnthropicFile(ctx, key, part)
				if e != nil {
					return result, e
				}
				uploaded = append(uploaded, id)
				content = append(content, map[string]any{"type": "image", "source": map[string]any{"type": "file", "file_id": id}})
			} else {
				content = append(content, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": part.MIME, "data": base64.StdEncoding.EncodeToString(part.Data)}})
			}
		case "document":
			if upload {
				id, e := v.uploadAnthropicFile(ctx, key, part)
				if e != nil {
					return result, e
				}
				uploaded = append(uploaded, id)
				content = append(content, map[string]any{"type": "document", "source": map[string]any{"type": "file", "file_id": id}})
			} else {
				content = append(content, map[string]any{"type": "document", "source": map[string]any{"type": "base64", "media_type": part.MIME, "data": base64.StdEncoding.EncodeToString(part.Data)}})
			}
		default:
			return VerificationResult{}, fmt.Errorf("anthropic count endpoint cannot accept %s", part.Type)
		}
	}
	payload := map[string]any{"model": model.ID, "messages": []any{map[string]any{"role": "user", "content": content}}}
	if a.System != "" {
		payload["system"] = a.System
	}
	if a.Tools != "" {
		var tools any
		if err := json.Unmarshal([]byte(a.Tools), &tools); err != nil {
			return VerificationResult{}, fmt.Errorf("--tools must contain a JSON array for verification: %w", err)
		}
		payload["tools"] = tools
	}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(v.AnthropicBase, "/")+"/v1/messages/count_tokens", bytes.NewReader(b))
	if err != nil {
		return VerificationResult{}, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	var response struct {
		InputTokens int64 `json:"input_tokens"`
	}
	if err := doJSON(v.Client, req, &response); err != nil {
		return VerificationResult{}, err
	}
	result.Tokens = response.InputTokens
	result.Method = "anthropic:messages.count_tokens (mixed aggregate)"
	return result, nil
}

func (v *HTTPVerifier) verifyGemini(ctx context.Context, model Model, a AssembledRequest) (result VerificationResult, err error) {
	key := v.LookupEnv("GEMINI_API_KEY")
	if key == "" {
		key = v.LookupEnv("GOOGLE_API_KEY")
	}
	if key == "" {
		return VerificationResult{}, fmt.Errorf("GEMINI_API_KEY or GOOGLE_API_KEY is not set")
	}
	upload := partsBytes(a.Parts) > model.Media.MaxInlineBytes && model.Media.MaxInlineBytes > 0
	if upload && !a.AllowFileUpload {
		return VerificationResult{}, fmt.Errorf("mixed input exceeds %d-byte inline limit; retry with --verify --allow-file-upload", model.Media.MaxInlineBytes)
	}
	var uploaded []string
	if upload {
		result.Transport = "upload"
		result.CleanupStatus = "pending"
		defer func() {
			cleanupErr := v.deleteGeminiFiles(uploaded, key)
			if cleanupErr != nil {
				result.CleanupStatus = "failed"
				if err == nil {
					err = fmt.Errorf("provider count succeeded but temporary-file cleanup failed: %w", cleanupErr)
				}
			} else {
				result.CleanupStatus = "deleted"
			}
		}()
	} else {
		result.Transport = "inline"
		result.CleanupStatus = "not_required"
	}
	text := a.Content
	if a.Tools != "" {
		text += "\n\n[tool declarations]\n" + a.Tools
	}
	parts := make([]any, 0, len(a.Parts)+1)
	if len(a.Parts) == 0 {
		parts = append(parts, map[string]any{"text": text})
	} else {
		for _, part := range a.Parts {
			if part.Type == "text" {
				if part.Text != "" {
					parts = append(parts, map[string]any{"text": part.Text})
				}
			} else if upload {
				name, uri, e := v.uploadGeminiFile(ctx, key, part)
				if e != nil {
					return result, e
				}
				uploaded = append(uploaded, name)
				parts = append(parts, map[string]any{"fileData": map[string]any{"mimeType": part.MIME, "fileUri": uri}})
			} else {
				parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": part.MIME, "data": base64.StdEncoding.EncodeToString(part.Data)}})
			}
		}
		if a.Tools != "" {
			parts = append(parts, map[string]any{"text": "[tool declarations]\n" + a.Tools})
		}
	}
	payload := map[string]any{"contents": []any{map[string]any{"role": "user", "parts": parts}}}
	if a.System != "" {
		payload["systemInstruction"] = map[string]any{"parts": []any{map[string]any{"text": a.System}}}
	}
	b, _ := json.Marshal(payload)
	endpoint := fmt.Sprintf("%s/v1beta/models/%s:countTokens?key=%s", strings.TrimRight(v.GeminiBase, "/"), url.PathEscape(model.ID), url.QueryEscape(key))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return VerificationResult{}, err
	}
	req.Header.Set("content-type", "application/json")
	var response struct {
		TotalTokens int64 `json:"totalTokens"`
	}
	if err := doJSON(v.Client, req, &response); err != nil {
		return VerificationResult{}, err
	}
	result.Tokens = response.TotalTokens
	result.Method = "gemini:models.countTokens (mixed aggregate)"
	return result, nil
}

func partsBytes(parts []RequestPart) int64 {
	var n int64
	for _, p := range parts {
		n += int64(len(p.Data))
	}
	return n
}

func (v *HTTPVerifier) uploadAnthropicFile(ctx context.Context, key string, part RequestPart) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	w, err := mw.CreateFormFile("file", part.AssetID)
	if err != nil {
		return "", err
	}
	if _, err = w.Write(part.Data); err != nil {
		return "", err
	}
	if err = mw.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(v.AnthropicBase, "/")+"/v1/files", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", mw.FormDataContentType())
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "files-api-2025-04-14")
	var response struct {
		ID string `json:"id"`
	}
	if err = doJSON(v.Client, req, &response); err != nil {
		return "", err
	}
	if response.ID == "" {
		return "", fmt.Errorf("anthropic file upload returned no id")
	}
	return response.ID, nil
}

func (v *HTTPVerifier) deleteAnthropicFiles(ids []string) error {
	var first error
	for _, id := range ids {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, strings.TrimRight(v.AnthropicBase, "/")+"/v1/files/"+url.PathEscape(id), nil)
		if err == nil {
			req.Header.Set("x-api-key", v.LookupEnv("ANTHROPIC_API_KEY"))
			req.Header.Set("anthropic-version", "2023-06-01")
			req.Header.Set("anthropic-beta", "files-api-2025-04-14")
			err = doStatus(v.Client, req)
		}
		cancel()
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (v *HTTPVerifier) uploadGeminiFile(ctx context.Context, key string, part RequestPart) (string, string, error) {
	meta, _ := json.Marshal(map[string]any{"file": map[string]any{"display_name": part.AssetID}})
	endpoint := strings.TrimRight(v.GeminiBase, "/") + "/upload/v1beta/files?key=" + url.QueryEscape(key)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(meta))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("X-Goog-Upload-Protocol", "resumable")
	req.Header.Set("X-Goog-Upload-Command", "start")
	req.Header.Set("X-Goog-Upload-Header-Content-Length", fmt.Sprint(len(part.Data)))
	req.Header.Set("X-Goog-Upload-Header-Content-Type", part.MIME)
	resp, err := v.Client.Do(req)
	if err != nil {
		return "", "", err
	}
	_, drainErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	drainErr = errors.Join(drainErr, resp.Body.Close())
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("gemini upload start returned HTTP %d", resp.StatusCode)
	}
	if drainErr != nil {
		return "", "", drainErr
	}
	uploadURL := resp.Header.Get("X-Goog-Upload-URL")
	if uploadURL == "" {
		return "", "", fmt.Errorf("gemini upload start returned no upload URL")
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, bytes.NewReader(part.Data))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("content-type", part.MIME)
	req.Header.Set("X-Goog-Upload-Offset", "0")
	req.Header.Set("X-Goog-Upload-Command", "upload, finalize")
	var completed struct {
		File struct {
			Name string `json:"name"`
			URI  string `json:"uri"`
		} `json:"file"`
	}
	if err = doJSON(v.Client, req, &completed); err != nil {
		return "", "", err
	}
	if completed.File.Name == "" || completed.File.URI == "" {
		return "", "", fmt.Errorf("gemini file upload returned incomplete reference")
	}
	return completed.File.Name, completed.File.URI, nil
}

func (v *HTTPVerifier) deleteGeminiFiles(names []string, key string) error {
	var first error
	for _, name := range names {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		endpoint := strings.TrimRight(v.GeminiBase, "/") + "/v1beta/" + strings.TrimLeft(name, "/") + "?key=" + url.QueryEscape(key)
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
		if err == nil {
			err = doStatus(v.Client, req)
		}
		cancel()
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}

func doStatus(client *http.Client, req *http.Request) (err error) {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	_, drainErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("provider cleanup returned HTTP %d", resp.StatusCode)
	}
	return drainErr
}

func doJSON(client *http.Client, req *http.Request, out any) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(b, &apiErr)
		message := apiErr.Error.Message
		if message == "" {
			message = strings.TrimSpace(string(b))
		}
		return fmt.Errorf("provider returned HTTP %d: %s", resp.StatusCode, message)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("decode provider response: %w", err)
	}
	return nil
}
