package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
)

// PullProgress is what Pull reports back to the CLI for live status. The
// dashboard can subscribe to the same events via the events.Hub once we
// wire ai topics (Phase 6.1).
type PullProgress struct {
	Status string `json:"status"`
	// Some providers (Ollama) report digest/percent for the current layer.
	Digest    string `json:"digest,omitempty"`
	Completed int64  `json:"completed,omitempty"`
	Total     int64  `json:"total,omitempty"`
}

// Pull downloads `model` into `provider`'s local cache.
//
// Per-provider routing:
//
//   - Ollama  → `ollama pull <model>`, streamed line-by-line.
//   - LocalAI → POST /models/apply with a YAML doc derived from the model name.
//   - vLLM    → no-op; vLLM downloads on first inference and refuses
//     manual pulls (huggingface_hub does the work).
//
// Streams human-readable progress to `out`. Returns an error if the pull
// transport itself fails — model-not-found is the provider's error, surfaced
// as-is.
func Pull(ctx context.Context, p Provider, model string, out io.Writer) error {
	if model == "" {
		return errors.New("pull: model is required")
	}
	switch p {
	case ProviderOllama:
		return pullOllama(ctx, model, out)
	case ProviderLocalAI:
		return pullLocalAI(ctx, model, out)
	case ProviderVLLM:
		fmt.Fprintln(out, "vLLM downloads models on first inference — nothing to do.")
		return nil
	}
	return fmt.Errorf("pull: unknown provider %q", p)
}

func pullOllama(ctx context.Context, model string, out io.Writer) error {
	if _, err := exec.LookPath("ollama"); err != nil {
		return fmt.Errorf("ollama: binary not found on PATH (apigw ai add ollama first)")
	}
	cmd := exec.CommandContext(ctx, "ollama", "pull", model)
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}

// pullLocalAI hits LocalAI's installation endpoint. LocalAI accepts a
// model gallery URL or a model name — we treat anything containing "/" as
// a URL, anything else as a name (resolved against the default gallery).
//
// Reference: https://localai.io/models/
func pullLocalAI(ctx context.Context, model string, out io.Writer) error {
	det, err := Detect(ProviderLocalAI, 0)
	if err != nil {
		return err
	}
	if !det.Listening {
		return fmt.Errorf("localai: not listening on %d — start it before pulling", det.Port)
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/models/apply", det.Port)
	payload := map[string]any{"id": model}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(out, resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("localai apply returned %s", resp.Status)
	}
	fmt.Fprintln(out)
	return nil
}

// ListLocalModels returns the models currently available for `p`, by
// querying the provider's API. Used by `apigw ai status <provider>` to show
// "you have llama3, mistral, ..." inline.
//
// Best-effort: returns nil + nil error for providers without a list API or
// when the provider isn't running.
func ListLocalModels(ctx context.Context, p Provider) ([]string, error) {
	det, _ := Detect(p, 0)
	if !det.Listening {
		return nil, nil
	}
	switch p {
	case ProviderOllama:
		return listOllamaModels(ctx, det.Port)
	case ProviderLocalAI:
		return listLocalAIModels(ctx, det.Port)
	}
	return nil, nil
}

func listOllamaModels(ctx context.Context, port int) ([]string, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/api/tags", port)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var doc struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(doc.Models))
	for _, m := range doc.Models {
		out = append(out, m.Name)
	}
	return out, nil
}

func listLocalAIModels(ctx context.Context, port int) ([]string, error) {
	// LocalAI exposes /v1/models (OpenAI-compatible).
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/models", port)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var doc struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(doc.Data))
	for _, m := range doc.Data {
		out = append(out, m.ID)
	}
	return out, nil
}
