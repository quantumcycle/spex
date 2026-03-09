package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

type ciBufferedOutput struct {
	name  string
	lines []string
}

// CIRenderer writes plain line-by-line progress to stderr.
// All methods are safe to call concurrently.
//
// outputTokens controls which process output is shown and when:
//   - Single token ("errors", "success", "all"): print output immediately for matching category.
//   - Multiple tokens (e.g. ["success","errors"]): buffer output and flush in token order at Summary time.
type CIRenderer struct {
	mu           sync.Mutex
	noColor      bool
	outputTokens []string
	nameWidth    int
	buffered     map[string][]ciBufferedOutput // keyed by "success" or "errors"; populated in multi-token mode
}

func NewCIRenderer(nameWidth int, noColor bool, outputTokens []string) *CIRenderer {
	return &CIRenderer{
		nameWidth:    nameWidth,
		noColor:      noColor,
		outputTokens: outputTokens,
		buffered:     map[string][]ciBufferedOutput{"success": {}, "errors": {}},
	}
}

func (r *CIRenderer) OnStart(state *RunnerState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(os.Stderr, "[%-*s] starting\n", r.nameWidth, state.Name)
}

func (r *CIRenderer) OnDone(state *RunnerState) {
	ec := state.ExitCode
	ok := ec != nil && *ec == 0
	dur := formatDuration(state.EndedAt.Sub(state.StartedAt))

	r.mu.Lock()
	defer r.mu.Unlock()

	// Always print the status line.
	if ok {
		if r.noColor {
			fmt.Fprintf(os.Stderr, "[%-*s] done in %s\n", r.nameWidth, state.Name, dur)
		} else {
			fmt.Fprintf(os.Stderr, "[%-*s] \u2713 done in %s\n", r.nameWidth, state.Name, dur)
		}
	} else {
		code := -1
		if ec != nil {
			code = *ec
		}
		if r.noColor {
			fmt.Fprintf(os.Stderr, "[%-*s] exited %d in %s\n", r.nameWidth, state.Name, code, dur)
		} else {
			fmt.Fprintf(os.Stderr, "[%-*s] \u2717 exited %d in %s\n", r.nameWidth, state.Name, code, dur)
		}
	}

	category := "success"
	if !ok {
		category = "errors"
	}

	if len(r.outputTokens) == 1 {
		// Single-token mode: print immediately if this category (or "all") matches.
		t := r.outputTokens[0]
		if t == "all" || t == category {
			printOutputBlock(os.Stderr, state.Name, state.FullOutput)
		}
	} else {
		// Multi-token mode: buffer for later, ordered flush in Summary.
		r.buffered[category] = append(r.buffered[category], ciBufferedOutput{
			name:  state.Name,
			lines: state.FullOutput,
		})
	}
}

func (r *CIRenderer) Summary(runners []*Runner, duration time.Duration) {
	doneCount, errCount := 0, 0
	for _, runner := range runners {
		ec := runner.State.ExitCode
		if ec != nil && *ec == 0 {
			doneCount++
		} else {
			errCount++
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// In multi-token mode, flush buffered output in the order tokens were specified.
	if len(r.outputTokens) > 1 {
		for _, token := range r.outputTokens {
			for _, buf := range r.buffered[token] {
				printOutputBlock(os.Stderr, buf.name, buf.lines)
			}
		}
	}

	fmt.Fprintf(os.Stderr, "\n%d done, %d error(s), %s\n", doneCount, errCount, formatDuration(duration))
}

func printOutputBlock(w *os.File, name string, lines []string) {
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(w, "\n--- %s output ---\n", name)
	fmt.Fprintln(w, strings.Join(lines, "\n"))
	fmt.Fprintln(w, "---")
}
