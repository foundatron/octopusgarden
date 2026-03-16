// Package interview provides a conversational spec-drafting assistant that uses
// an LLM to interview the user and produce an NLSpec-format specification.
package interview

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/foundatron/octopusgarden/internal/llm"
)

const maxRounds = 20

const rePromptMsg = "Enter your response (blank line to submit, or type \"done\" to generate the spec)."

const finalInstruction = "The user is done answering questions. " +
	"Generate the complete NLSpec-format specification now based on everything discussed."

// maxInputSize is the maximum size for a single scanner line (1 MiB).
const maxInputSize = 1 << 20

// Interviewer conducts a conversational interview to produce a spec.
type Interviewer struct {
	client llm.Client
	in     io.Reader
	out    io.Writer
	model  string
}

// New creates an Interviewer that reads from in, writes to out, and uses the
// given LLM client and model.
func New(client llm.Client, in io.Reader, out io.Writer, model string) *Interviewer {
	return &Interviewer{
		client: client,
		in:     in,
		out:    out,
		model:  model,
	}
}

// Run conducts the interview starting with initialPrompt and returns the final
// spec content and total LLM cost in USD.
func (i *Interviewer) Run(ctx context.Context, initialPrompt string) (string, float64, error) {
	return i.run(ctx, systemPrompt, []llm.Message{{Role: "user", Content: initialPrompt}})
}

// RunWithSeed conducts the interview starting from an existing spec, asking
// targeted questions to improve it. Returns the final spec and total LLM cost in USD.
func (i *Interviewer) RunWithSeed(ctx context.Context, seedSpec string) (string, float64, error) {
	userMsg := "Here is my current spec. Please review it for completeness and ask questions " +
		"about anything that's missing or ambiguous:\n\n" + seedSpec
	return i.run(ctx, seedSystemPrompt, []llm.Message{{Role: "user", Content: userMsg}})
}

// readMessage collects lines until a blank line (submit) or "done" (finish).
// It selects on both the line channel and ctx.Done so that Ctrl-C is respected
// even while blocked waiting for input. Returns (text, done, err).
func (i *Interviewer) readMessage(ctx context.Context, lines <-chan string, scanDone <-chan error) (string, bool, error) {
	var collected []string
	for {
		select {
		case <-ctx.Done():
			return "", false, fmt.Errorf("interview: %w", ctx.Err())
		case line, ok := <-lines:
			if !ok {
				// Channel closed — scanner hit EOF or error.
				if err := <-scanDone; err != nil {
					return "", false, fmt.Errorf("interview: scanner: %w", err)
				}
				return strings.TrimSpace(strings.Join(collected, "\n")), true, nil
			}
			trimmed := strings.TrimSpace(line)
			if strings.EqualFold(trimmed, "done") {
				return strings.TrimSpace(strings.Join(collected, "\n")), true, nil
			}
			if trimmed == "" {
				if len(collected) > 0 {
					return strings.TrimSpace(strings.Join(collected, "\n")), false, nil
				}
				fmt.Fprintln(i.out, rePromptMsg) //nolint:errcheck
				continue
			}
			collected = append(collected, line)
		}
	}
}

// run is the shared conversation loop used by Run and RunWithSeed.
func (i *Interviewer) run(ctx context.Context, sysPrompt string, messages []llm.Message) (string, float64, error) {
	resp, err := i.client.Generate(ctx, llm.GenerateRequest{
		SystemPrompt: sysPrompt,
		Messages:     messages,
		Model:        i.model,
	})
	if err != nil {
		return "", 0, fmt.Errorf("interview: generate: %w", err)
	}

	totalCost := resp.CostUSD
	messages = append(messages, llm.Message{Role: "assistant", Content: resp.Content})
	fmt.Fprintln(i.out, resp.Content) //nolint:errcheck
	fmt.Fprintln(i.out, rePromptMsg)  //nolint:errcheck

	// Read lines in a background goroutine so readMessage can select on
	// both input and ctx.Done, making Ctrl-C responsive.
	scanner := bufio.NewScanner(i.in)
	scanner.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), maxInputSize)
	lines := make(chan string)
	scanDone := make(chan error, 1)
	go func() {
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
		scanDone <- scanner.Err()
		close(lines)
	}()

	round := 0
	for {
		text, done, err := i.readMessage(ctx, lines, scanDone)
		if err != nil {
			return "", totalCost, err
		}

		if text != "" && round < maxRounds {
			messages = append(messages, llm.Message{Role: "user", Content: text})
			resp, err = i.client.Generate(ctx, llm.GenerateRequest{
				SystemPrompt: sysPrompt,
				Messages:     messages,
				Model:        i.model,
			})
			if err != nil {
				return "", totalCost, fmt.Errorf("interview: generate: %w", err)
			}

			totalCost += resp.CostUSD
			messages = append(messages, llm.Message{Role: "assistant", Content: resp.Content})
			fmt.Fprintln(i.out, resp.Content) //nolint:errcheck
			round++
		}

		if done || round >= maxRounds {
			if round >= maxRounds && !done {
				fmt.Fprintln(i.out, "Maximum rounds reached. Generating spec now.") //nolint:errcheck
			}
			spec, cost, genErr := i.generateFinal(ctx, sysPrompt, messages)
			return spec, totalCost + cost, genErr
		}
	}
}

func (i *Interviewer) generateFinal(ctx context.Context, sysPrompt string, messages []llm.Message) (string, float64, error) {
	msgs := make([]llm.Message, len(messages), len(messages)+1)
	copy(msgs, messages)
	msgs = append(msgs, llm.Message{Role: "user", Content: finalInstruction})
	resp, err := i.client.Generate(ctx, llm.GenerateRequest{
		SystemPrompt: sysPrompt,
		Messages:     msgs,
		Model:        i.model,
	})
	if err != nil {
		return "", 0, fmt.Errorf("interview: generate: %w", err)
	}
	return resp.Content, resp.CostUSD, nil
}
