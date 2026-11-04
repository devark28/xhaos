// Package anthropic adapts the Claude Messages API (official Go SDK) to llm.
package anthropic

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	sdkparam "github.com/anthropics/anthropic-sdk-go/v2/packages/param"

	"xhaos/app/llm"
)

const Kind = "anthropic"

// DefaultModels are offered when a provider config lists none.
var DefaultModels = []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"}

type Adapter struct {
	// Options are appended to every client (tests use option.WithHTTPClient).
	Options []option.RequestOption
}

func (Adapter) Kind() string { return Kind }

func (a Adapter) client(creds llm.Credentials) sdk.Client {
	opts := []option.RequestOption{option.WithAPIKey(creds.APIKey)}
	if creds.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(creds.BaseURL))
	}
	return sdk.NewClient(append(opts, a.Options...)...)
}

func (a Adapter) Stream(ctx context.Context, creds llm.Credentials, turn llm.Turn, onDelta func(string)) (llm.Result, error) {
	messages, err := params(turn.Messages)
	if err != nil {
		return llm.Result{}, err
	}
	params := sdk.MessageNewParams{
		Model:        sdk.Model(turn.Model),
		MaxTokens:    64000,
		Messages:     messages,
		OutputConfig: sdk.OutputConfigParam{Effort: sdk.OutputConfigEffortHigh},
	}
	if turn.System != "" {
		params.System = []sdk.TextBlockParam{{Text: turn.System}}
	}
	for _, t := range turn.Tools {
		params.Tools = append(params.Tools, sdk.ToolUnionParam{OfTool: &sdk.ToolParam{
			Name:        t.Name,
			Description: sdk.String(t.Description),
			InputSchema: sdk.ToolInputSchemaParam{Properties: t.Schema.Properties, Required: t.Schema.Required},
			// Large inputs (file contents) stream as generated; the harness
			// validates every input before running a tool.
			EagerInputStreaming: sdk.Bool(true),
		}})
	}
	client := a.client(creds)
	stream := client.Messages.NewStreaming(ctx, params)
	var msg sdk.Message
	for stream.Next() {
		event := stream.Current()
		if err := msg.Accumulate(event); err != nil {
			return llm.Result{}, err
		}
		if delta, ok := event.AsAny().(sdk.ContentBlockDeltaEvent); ok {
			if text, ok := delta.Delta.AsAny().(sdk.TextDelta); ok && onDelta != nil {
				onDelta(text.Text)
			}
		}
	}
	if err := stream.Err(); err != nil {
		return llm.Result{}, err
	}
	var text strings.Builder
	var calls []llm.ToolCall
	for _, block := range msg.Content {
		switch b := block.AsAny().(type) {
		case sdk.TextBlock:
			text.WriteString(b.Text)
		case sdk.ToolUseBlock:
			calls = append(calls, llm.ToolCall{ID: b.ID, Name: b.Name, Input: b.Input})
		}
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return llm.Result{}, err
	}
	return llm.Result{
		Text:         text.String(),
		Calls:        calls,
		StopReason:   string(msg.StopReason),
		Raw:          raw,
		InputTokens:  msg.Usage.InputTokens,
		OutputTokens: msg.Usage.OutputTokens,
	}, nil
}

// params converts the conversation. Consecutive user messages (tool results,
// then owner messages sent while tools waited) merge into one; the caller
// orders tool results first, as the API requires.
func params(in []llm.Message) ([]sdk.MessageParam, error) {
	out := make([]sdk.MessageParam, 0, len(in))
	for _, m := range in {
		if m.Role == "user" {
			var blocks []sdk.ContentBlockParamUnion
			for _, r := range m.Results {
				blocks = append(blocks, sdk.NewToolResultBlock(r.CallID, r.Content, r.IsError))
			}
			if m.Text != "" {
				blocks = append(blocks, sdk.NewTextBlock(m.Text))
			}
			for _, f := range m.Files {
				data := base64.StdEncoding.EncodeToString(f.Data)
				if f.Type == "application/pdf" {
					blocks = append(blocks, sdk.NewDocumentBlock(sdk.Base64PDFSourceParam{Data: data}))
				} else {
					blocks = append(blocks, sdk.NewImageBlockBase64(f.Type, data))
				}
			}
			if len(blocks) == 0 {
				continue
			}
			if n := len(out); n > 0 && out[n-1].OfUser != nil {
				out[n-1].OfUser.Content = append(out[n-1].OfUser.Content, blocks...)
				continue
			}
			out = append(out, sdk.NewUserMessage(blocks...))
			continue
		}
		if m.RawKind == Kind && len(m.Raw) > 0 {
			// Replay our own reply verbatim (thinking blocks included).
			var prior sdk.Message
			if err := json.Unmarshal(m.Raw, &prior); err != nil {
				return nil, fmt.Errorf("replay assistant message: %w", err)
			}
			param := prior.ToParam()
			if param.OfAssistant != nil {
				for _, b := range param.OfAssistant.Content {
					// A null toolset_name comes back from the round trip as "",
					// which the API rejects; leave it out instead.
					if b.OfToolUse != nil && b.OfToolUse.ToolsetName.Value == "" {
						b.OfToolUse.ToolsetName = sdkparam.Opt[string]{}
					}
				}
			}
			if m.StripThinking && param.OfAssistant != nil {
				kept := param.OfAssistant.Content[:0]
				for _, b := range param.OfAssistant.Content {
					if b.OfThinking == nil && b.OfRedactedThinking == nil {
						kept = append(kept, b)
					}
				}
				if len(kept) == 0 {
					continue // the reply was only reasoning
				}
				param.OfAssistant.Content = kept
			}
			out = append(out, param)
			continue
		}
		var blocks []sdk.ContentBlockParamUnion
		if m.Text != "" {
			blocks = append(blocks, sdk.NewTextBlock(m.Text))
		}
		for _, c := range m.Calls {
			blocks = append(blocks, sdk.NewToolUseBlock(c.ID, c.Input, c.Name))
		}
		out = append(out, sdk.NewAssistantMessage(blocks...))
	}
	return out, nil
}

func (a Adapter) Models(ctx context.Context, creds llm.Credentials) ([]string, error) {
	client := a.client(creds)
	page, err := client.Models.List(ctx, sdk.ModelListParams{})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(page.Data))
	for _, m := range page.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}
