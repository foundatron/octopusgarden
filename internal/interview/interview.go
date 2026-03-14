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

const rePromptMsg = "Please enter a response (or type \"done\" to generate the spec)."

const finalInstruction = "The user is done answering questions. " +
	"Generate the complete NLSpec-format specification now based on everything discussed."

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

	scanner := bufio.NewScanner(i.in)
	round := 0

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return "", totalCost, fmt.Errorf("interview: %w", err)
		}

		trimmed := strings.TrimSpace(scanner.Text())
		if trimmed == "" {
			fmt.Fprintln(i.out, rePromptMsg) //nolint:errcheck
			continue
		}

		// round is incremented at the end of each loop body, so round >= maxRounds
		// fires after maxRounds answers have been processed (0-indexed: rounds 0..maxRounds-1).
		if strings.EqualFold(trimmed, "done") || round >= maxRounds {
			if round >= maxRounds {
				fmt.Fprintln(i.out, "Maximum rounds reached. Generating spec now.") //nolint:errcheck
			}
			spec, cost, genErr := i.generateFinal(ctx, sysPrompt, messages)
			return spec, totalCost + cost, genErr
		}

		messages = append(messages, llm.Message{Role: "user", Content: trimmed})
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

	if err := scanner.Err(); err != nil {
		return "", totalCost, fmt.Errorf("interview: scanner: %w", err)
	}

	// EOF without "done" — treat as auto-generate.
	spec, cost, err := i.generateFinal(ctx, sysPrompt, messages)
	return spec, totalCost + cost, err
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
