// Package interview provides a conversational spec-drafting assistant that uses
// an LLM to interview the user and produce an NLSpec-format specification.
package interview

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

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
	errOut  io.Writer
	model   string
}

// New creates an Interviewer that reads from in, displays output via display,
// and uses the given LLM client and model. Status output (spinners) is written
// to errOut; pass nil to suppress.
func New(client llm.Client, in io.Reader, display Display, errOut io.Writer, model string) *Interviewer {
	return &Interviewer{
		client:  client,
		in:      in,
		display: display,
		errOut:  errOut,
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

// lineAction signals what readMessage should do after processing a line.
type lineAction int

const (
	lineContinue lineAction = iota // keep collecting
	lineSubmit                     // two blank lines — submit message
	lineDone                       // "done" keyword or EOF
)

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
				return i.handleEOF(scanDone, collected)
			}
			action, newPrevBlank := i.processLine(line, collected, prevBlank)
			prevBlank = newPrevBlank
			switch action {
			case lineDone:
				return joinCollected(collected), true, nil
			case lineSubmit:
				return joinCollected(collected), false, nil
			default:
				if trimmed := strings.TrimSpace(line); trimmed != "" || len(collected) > 0 {
					collected = append(collected, line)
				}
			}
		}
	}
}

// handleEOF drains the scanner error channel and returns collected text.
func (i *Interviewer) handleEOF(scanDone <-chan error, collected []string) (string, bool, error) {
	if err := <-scanDone; err != nil {
		return "", false, fmt.Errorf("interview: scanner: %w", err)
	}
	return joinCollected(collected), true, nil
}

// processLine determines the action for a single input line. It returns the
// action and the updated prevBlank state.
func (i *Interviewer) processLine(line string, collected []string, prevBlank bool) (lineAction, bool) {
	trimmed := strings.TrimSpace(line)
	if strings.EqualFold(trimmed, "done") {
		return lineDone, false
	}
	if trimmed != "" {
		return lineContinue, false
	}
	// Blank line handling.
	if prevBlank && len(collected) > 0 {
		return lineSubmit, true
	}
	if len(collected) > 0 {
		return lineContinue, true
	}
	i.display.SystemMessage(rePromptMsg)
	return lineContinue, false
}

func joinCollected(collected []string) string {
	return strings.TrimSpace(strings.Join(collected, "\n"))
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
			stop := i.startSpinner("Generating spec...")
			spec, cost, genErr := i.generateFinal(ctx, sysPrompt, messages)
			stop()
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

// startSpinner displays a braille spinner with the given label on errOut.
// It returns a stop function that halts the spinner and clears the line.
// If errOut is nil, the returned function is a no-op.
func (i *Interviewer) startSpinner(label string) func() {
	if i.errOut == nil {
		return func() {}
	}
	frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
	done := make(chan struct{})
	ticker := time.NewTicker(100 * time.Millisecond)
	go func() {
		idx := 0
		for {
			select {
			case <-ticker.C:
				fmt.Fprintf(i.errOut, "\r%c %s", frames[idx%len(frames)], label) //nolint:errcheck
				idx++
			case <-done:
				return
			}
		}
	}()
	return func() {
		close(done)
		ticker.Stop()
		// Clear the spinner line: overwrite with spaces, then carriage return.
		clearLen := len(label) + 4
		fmt.Fprintf(i.errOut, "\r%s\r", strings.Repeat(" ", clearLen)) //nolint:errcheck
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
