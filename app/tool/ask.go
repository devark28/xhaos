package tool

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Ask is the effect of ask_user: no permission is needed, but the call waits
// for the user's answers instead of running.
const Ask = "ask"

// Question is one question of an ask_user call, as stored in the call's
// preview and shown to the user.
type Question struct {
	ID       string   `json:"id"`
	Prompt   string   `json:"prompt"`
	Options  []Option `json:"options,omitempty"`
	Multiple bool     `json:"multiple,omitempty"`
	Required bool     `json:"required"`
}

type Option struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Answer is the user's answer to one question: chosen option IDs and/or text.
type Answer struct {
	Choices []string `json:"choices,omitempty"`
	Text    string   `json:"text,omitempty"`
}

type askInput struct {
	Title     string `json:"title"`
	Questions []struct {
		Prompt  string `json:"prompt"`
		Options []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
		Multiple bool  `json:"multiple"`
		Required *bool `json:"required"`
	} `json:"questions"`
}

const (
	maxQuestions = 6
	maxOptions   = 8
)

// askSpec describes ask_user to the model.
var askSpec = map[string]any{
	"title": map[string]any{"type": "string", "description": "A short heading for the questions."},
	"questions": map[string]any{
		"type": "array", "minItems": 1, "maxItems": maxQuestions,
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]any{"type": "string"},
				"options": map[string]any{
					"type": "array", "maxItems": maxOptions,
					"items": map[string]any{
						"type":       "object",
						"properties": map[string]any{"label": str, "description": str},
						"required":   []string{"label"},
					},
				},
				"multiple": map[string]any{"type": "boolean", "description": "Allow choosing several options."},
				"required": map[string]any{"type": "boolean", "description": "Default true; false lets the user skip it."},
			},
			"required": []string{"prompt"},
		},
	},
}

// prepareAsk validates an ask_user call and stores its questions, with IDs,
// as JSON in the call's preview.
func prepareAsk(call Call, raw json.RawMessage) (Call, error) {
	var in askInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return call, fmt.Errorf("invalid input for ask_user: %v", err)
	}
	if len(in.Questions) == 0 || len(in.Questions) > maxQuestions {
		return call, fmt.Errorf("ask_user needs 1 to %d questions", maxQuestions)
	}
	qs := make([]Question, 0, len(in.Questions))
	for i, q := range in.Questions {
		prompt := strings.TrimSpace(q.Prompt)
		if prompt == "" {
			return call, fmt.Errorf("question %d has no prompt", i+1)
		}
		if len(q.Options) > maxOptions {
			return call, fmt.Errorf("question %d has more than %d options", i+1, maxOptions)
		}
		out := Question{ID: fmt.Sprintf("q%d", i+1), Prompt: prompt, Multiple: q.Multiple, Required: q.Required == nil || *q.Required}
		seen := map[string]bool{}
		for j, o := range q.Options {
			label := strings.TrimSpace(o.Label)
			if label == "" || seen[label] {
				return call, fmt.Errorf("question %d: option labels must be non-empty and distinct", i+1)
			}
			seen[label] = true
			out.Options = append(out.Options, Option{ID: fmt.Sprintf("o%d", j+1), Label: label, Description: strings.TrimSpace(o.Description)})
		}
		qs = append(qs, out)
	}
	preview, _ := json.Marshal(map[string]any{"title": strings.TrimSpace(in.Title), "questions": qs})
	call.Effect = Ask
	call.Preview = string(preview)
	call.Summary = strings.TrimSpace(in.Title)
	if call.Summary == "" {
		call.Summary = qs[0].Prompt
	}
	return call, nil
}

// ParseQuestions reads the questions back from an ask_user call's preview.
func ParseQuestions(preview string) ([]Question, error) {
	var p struct {
		Questions []Question `json:"questions"`
	}
	if err := json.Unmarshal([]byte(preview), &p); err != nil || len(p.Questions) == 0 {
		return nil, errors.New("this call has no questions")
	}
	return p.Questions, nil
}

// FormatAnswers checks the user's answers and renders them for the model.
func FormatAnswers(qs []Question, answers map[string]Answer) (string, error) {
	var b strings.Builder
	for _, q := range qs {
		a := answers[q.ID]
		labels := map[string]string{}
		for _, o := range q.Options {
			labels[o.ID] = o.Label
		}
		var chosen []string
		for _, id := range a.Choices {
			label, ok := labels[id]
			if !ok {
				return "", fmt.Errorf("unknown option %q for %q", id, q.Prompt)
			}
			chosen = append(chosen, label)
		}
		if len(chosen) > 1 && !q.Multiple {
			return "", fmt.Errorf("choose one option for %q", q.Prompt)
		}
		text := strings.TrimSpace(a.Text)
		if q.Required && len(chosen) == 0 && text == "" {
			return "", fmt.Errorf("answer %q", q.Prompt)
		}
		fmt.Fprintf(&b, "Q: %s\n", q.Prompt)
		switch {
		case len(chosen) > 0 && text != "":
			fmt.Fprintf(&b, "A: %s\n   %s\n", strings.Join(chosen, "; "), text)
		case len(chosen) > 0:
			fmt.Fprintf(&b, "A: %s\n", strings.Join(chosen, "; "))
		case text != "":
			fmt.Fprintf(&b, "A: %s\n", text)
		default:
			b.WriteString("A: (skipped)\n")
		}
	}
	return strings.TrimSpace(b.String()), nil
}
