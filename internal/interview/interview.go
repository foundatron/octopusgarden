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

const rePromptMsg = "Enter your response (two blank lines to submit, or type \"done\" to generate the spec)."

const finalInstruction = "The user is done answering questions. " +
	"Generate the complete NLSpec-format specification now based on everything discussed."

// maxInputSize is the maximum size for a single scanner line (1 MiB).
const maxInputSize = 1 << 20

// Display controls how interview output is presented to the user.
type Display interface {
	AssistantMessage(text string) // LLM response (rendered as markdown in styled mode)
	InputPrompt()                 // Show prompt indicator before user types
	InputSummary(lineCount int)   // After submit: "[ pasted: N lines ]" if >3 lines
	SystemMessage(text string)    // Hints, status messages
	Separator()                   // Horizontal rule between Q&A rounds
}

// Interviewer conducts a conversational interview to produce a spec.
type Interviewer struct {
	client  llm.Client
	in      io.Reader
	display Display
	model   string
}

// New creates an Interviewer that reads from in, displays output via display,
// and uses the given LLM client and model.
func New(client llm.Client, in io.Reader, display Display, model string) *Interviewer {
	return &Interviewer{
		client:  client,
		in:      in,
		display: display,
		model:   model,
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

// readMessage collects lines until two consecutive blank lines (submit) or
// "done" (finish). Single blank lines are preserved in the collected text.
// It selects on both the line channel and ctx.Done so that Ctrl-C is respected
// even while blocked waiting for input. Returns (text, done, err).
func (i *Interviewer) readMessage(ctx context.Context, lines <-chan string, scanDone <-chan error) (string, bool, error) {
	var collected []string
	prevBlank := false
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
				if prevBlank && len(collected) > 0 {
					return strings.TrimSpace(strings.Join(collected, "\n")), false, nil
				}
				if len(collected) > 0 {
					collected = append(collected, line)
					prevBlank = true
					continue
				}
				i.display.SystemMessage(rePromptMsg)
				continue
			}
			prevBlank = false
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
	i.display.AssistantMessage(resp.Content)
	i.display.SystemMessage(rePromptMsg)
	i.display.InputPrompt()

	lines, scanDone := i.startScanner(ctx)

	round := 0
	for {
		text, done, err := i.readMessage(ctx, lines, scanDone)
		if err != nil {
			return "", totalCost, err
		}

		if text != "" && round < maxRounds {
			cost, err := i.processAnswer(ctx, sysPrompt, &messages, text)
			if err != nil {
				return "", totalCost, err
			}
			totalCost += cost
			round++
		}

		if done || round >= maxRounds {
			if round >= maxRounds && !done {
				i.display.SystemMessage("Maximum rounds reached. Generating spec now.")
			}
			spec, cost, genErr := i.generateFinal(ctx, sysPrompt, messages)
			return spec, totalCost + cost, genErr
		}
	}
}

// startScanner reads lines from i.in in a background goroutine so
// readMessage can select on both input and ctx.Done.
func (i *Interviewer) startScanner(ctx context.Context) (<-chan string, <-chan error) {
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
	return lines, scanDone
}

// processAnswer sends the user's text to the LLM, appends both messages, and
// displays the response. Returns the cost of the LLM call.
func (i *Interviewer) processAnswer(ctx context.Context, sysPrompt string, messages *[]llm.Message, text string) (float64, error) {
	lineCount := strings.Count(text, "\n") + 1
	i.display.InputSummary(lineCount)
	i.display.SystemMessage("Thinking...")
	*messages = append(*messages, llm.Message{Role: "user", Content: text})
	resp, err := i.client.Generate(ctx, llm.GenerateRequest{
		SystemPrompt: sysPrompt,
		Messages:     *messages,
		Model:        i.model,
	})
	if err != nil {
		return 0, fmt.Errorf("interview: generate: %w", err)
	}

	*messages = append(*messages, llm.Message{Role: "assistant", Content: resp.Content})
	i.display.Separator()
	i.display.AssistantMessage(resp.Content)
	i.display.SystemMessage(rePromptMsg)
	i.display.InputPrompt()
	return resp.CostUSD, nil
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
