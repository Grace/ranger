// Package narrate turns a ranking into prose without letting a model change it.
//
// The causal engine in internal/localize decides which operation is
// responsible. This package exists only to say that decision in English, for
// the pager message or the incident channel, where a table of robust-z scores
// is the wrong shape.
//
// The ordering matters and is the whole point. A model that reads spans and
// names a culprit is guessing with extra steps: it cannot be reproduced, it
// cannot be audited, and when it is wrong it is wrong fluently. Here the
// answer exists before the model is called, the model is handed the finished
// ranking rather than the trace, and its output is checked against that
// ranking before anyone sees it. If the prose names a different service than
// the engine did, the prose is discarded. That check is the feature; the
// generation is a convenience.
//
// Narration is off unless an endpoint is configured. Ranger's answer does not
// depend on a network call, and a tool that localizes an incident should not
// stop working because a model provider is having one.
package narrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Grace/ranger/internal/localize"
)

// ErrDisabled is returned when no endpoint is configured. It is not a failure:
// it is the default, and callers should fall back to the deterministic summary.
var ErrDisabled = errors.New("narration is not configured")

// ErrContradicted is returned when the model's prose names a service the
// ranking did not. The prose is discarded rather than shown.
//
// This is the case the package exists to catch. A narration that renames the
// culprit is worse than no narration, because it reads like the tool's answer
// and is not.
var ErrContradicted = errors.New("narration contradicted the ranking; discarded")

// Options configures the narrator. Zero value is disabled.
type Options struct {
	// Endpoint is an OpenAI-compatible chat completions URL. Empty disables
	// narration entirely.
	Endpoint string

	// Model is the model id to request.
	Model string

	// Key is the bearer token, if the endpoint wants one. A local runtime
	// usually does not.
	Key string

	// Timeout bounds the call. Narration is decoration on a finished answer,
	// so it gets a short one and failure is not fatal.
	Timeout time.Duration

	// HTTP is the client to use. Nil means a client built from Timeout.
	HTTP *http.Client
}

const defaultTimeout = 20 * time.Second

// Narrator renders a Result as prose.
type Narrator struct {
	opt Options
}

// New returns a Narrator. It is usable when opt.Endpoint is empty; every call
// simply returns ErrDisabled.
func New(opt Options) *Narrator {
	if opt.Timeout <= 0 {
		opt.Timeout = defaultTimeout
	}
	if opt.HTTP == nil {
		opt.HTTP = &http.Client{Timeout: opt.Timeout}
	}
	return &Narrator{opt: opt}
}

// Enabled reports whether narration will be attempted.
func (n *Narrator) Enabled() bool { return n != nil && n.opt.Endpoint != "" }

// Narrate returns a paragraph describing res.
//
// The model never sees a span. It is given the finished ranking — the operation
// names, the verdicts, and the measured shifts — and asked to write that up.
// The result is then checked against res before being returned.
func (n *Narrator) Narrate(ctx context.Context, res localize.Result) (string, error) {
	if !n.Enabled() {
		return "", ErrDisabled
	}

	facts, err := Facts(res)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(ctx, n.opt.Timeout)
	defer cancel()

	text, err := n.complete(ctx, facts)
	if err != nil {
		return "", err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("narration was empty")
	}
	if err := Check(text, res); err != nil {
		return "", err
	}
	return text, nil
}

// Facts renders the ranking as the only input the model is given.
//
// Everything here is already a conclusion. There are no raw spans, no trace
// ids, and no request content — partly because the model does not need them to
// write a paragraph, and partly because trace payloads carry customer data and
// a narration endpoint is somebody else's server.
func Facts(res localize.Result) (string, error) {
	var b strings.Builder
	if !res.Localized {
		fmt.Fprintf(&b, "outcome: no operation in this window explains the symptom\n")
	} else {
		fmt.Fprintf(&b, "outcome: localized\n")
	}
	fmt.Fprintf(&b, "operations ranked: %d (skipped for thin samples: %d)\n", res.Considered, res.Skipped)
	if len(res.Excluded) > 0 {
		fmt.Fprintf(&b, "services excluded before ranking: %s\n", strings.Join(res.Excluded, ", "))
	}
	top := res.Candidates
	if len(top) > 5 {
		top = top[:5]
	}
	if len(top) == 0 {
		return "", errors.New("no candidates to narrate")
	}
	b.WriteString("\nranking:\n")
	for i, c := range top {
		fmt.Fprintf(&b, "%d. %s — %s; self time %+s, total duration %+s, %.0f%% of its own baseline, z %.1f\n",
			i+1, c.Op.String(), c.Verdict,
			c.SelfTimeShift.Round(time.Microsecond), c.DurationShift.Round(time.Microsecond),
			c.RelativeShift*100, c.SelfTimeZ)
	}
	return b.String(), nil
}

const system = `You are writing one short paragraph for an on-call engineer.

You are given a finished root-cause ranking produced by a deterministic engine.
You did not produce it and you cannot change it. Rules:

- The service named at rank 1 is the answer. Never name a different service as
  the cause, and never hedge that it might be something else in the list.
- "waiting on something below it" means that operation is a victim, not a
  cause. Never describe it as responsible.
- If the outcome is that nothing explains the symptom, say exactly that. Do not
  offer a guess.
- Use only the numbers given. Do not invent trace ids, timestamps, commits, or
  causes such as "a memory leak" that are not in the input.
- Three sentences at most. No preamble, no bullet points, no headings.`

func (n *Narrator) complete(ctx context.Context, facts string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model":       n.opt.Model,
		"temperature": 0,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": facts},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.opt.Endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if n.opt.Key != "" {
		req.Header.Set("Authorization", "Bearer "+n.opt.Key)
	}
	res, err := n.opt.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("narration endpoint returned %d", res.StatusCode)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return "", fmt.Errorf("narration response was not chat completions JSON: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", errors.New("narration response had no choices")
	}
	return out.Choices[0].Message.Content, nil
}

// Check rejects prose that disagrees with the ranking it was given.
//
// Two failures are worth catching and both have been seen from real models.
// The first is naming a service that is not the top candidate — usually the
// second-ranked one, which is often the caller waiting on the real cause, and
// is exactly the confusion the trace-DAG walk exists to resolve. The second is
// naming a cause at all when the engine declined to localize.
//
// The check is deliberately blunt: it compares service names, not meaning. It
// will not catch prose that is subtly wrong about magnitude, and it is not
// trying to. It catches the failure that would make a reader act on the wrong
// service at three in the morning.
func Check(text string, res localize.Result) error {
	lower := strings.ToLower(text)

	if !res.Localized {
		// Nothing was localized, so any service named as the cause is invented.
		for _, c := range res.Candidates {
			if mentions(lower, c.Op.Service) {
				return fmt.Errorf("%w: engine declined to localize, prose named %q", ErrContradicted, c.Op.Service)
			}
		}
		return nil
	}

	if len(res.Candidates) == 0 {
		return errors.New("localized result with no candidates")
	}
	culprit := res.Candidates[0].Op.Service
	if !mentions(lower, culprit) {
		return fmt.Errorf("%w: engine named %q, prose did not mention it", ErrContradicted, culprit)
	}
	for _, c := range res.Candidates[1:] {
		if c.Op.Service == culprit {
			continue
		}
		// A victim may legitimately be discussed, but only the culprit may be
		// the one the prose is about, and a model that mentions a non-culprit
		// service without the culprit's name has already failed the check
		// above. Here we catch the subtler case: the prose leads with a
		// different service.
		if leadsWith(lower, c.Op.Service, culprit) {
			return fmt.Errorf("%w: engine named %q, prose leads with %q", ErrContradicted, culprit, c.Op.Service)
		}
	}
	return nil
}

// mentions reports whether the prose refers to a service by name.
func mentions(lowerText, service string) bool {
	s := strings.ToLower(strings.TrimSpace(service))
	return s != "" && strings.Contains(lowerText, s)
}

// leadsWith reports whether other appears before culprit in the text.
func leadsWith(lowerText, other, culprit string) bool {
	o := strings.Index(lowerText, strings.ToLower(other))
	c := strings.Index(lowerText, strings.ToLower(culprit))
	return o >= 0 && c >= 0 && o < c
}

// Summary is the deterministic fallback, used when narration is disabled,
// fails, or is discarded. It says the same thing without a model.
func Summary(res localize.Result) string {
	if !res.Localized || len(res.Candidates) == 0 {
		return fmt.Sprintf("No operation in this window explains the symptom. %d operations ranked, %d skipped for thin samples.",
			res.Considered, res.Skipped)
	}
	c := res.Candidates[0]
	return fmt.Sprintf("%s is the most likely cause: %s, with self time %+s against its own baseline (%.0f%%). %d operations ranked.",
		c.Op.String(), c.Verdict, c.SelfTimeShift.Round(time.Millisecond), c.RelativeShift*100, res.Considered)
}
