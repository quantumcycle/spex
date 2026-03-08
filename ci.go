package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// CIRenderer writes plain line-by-line progress to stderr.
// All methods are safe to call concurrently.
type CIRenderer struct {
	mu        sync.Mutex
	noColor   bool
	verbose   bool
	nameWidth int
}

func NewCIRenderer(nameWidth int, noColor bool, verbose bool) *CIRenderer {
	return &CIRenderer{nameWidth: nameWidth, noColor: noColor, verbose: verbose}
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

	if ok {
		if r.noColor {
			fmt.Fprintf(os.Stderr, "[%-*s] done in %s\n", r.nameWidth, state.Name, dur)
		} else {
			fmt.Fprintf(os.Stderr, "[%-*s] \u2713 done in %s\n", r.nameWidth, state.Name, dur)
		}
		if r.verbose {
			output := state.FullOutput
			if len(output) > 0 {
				fmt.Fprintf(os.Stderr, "\n--- %s output ---\n", state.Name)
				fmt.Fprintln(os.Stderr, strings.Join(output, "\n"))
				fmt.Fprintln(os.Stderr, "---\n")
			}
		}
		return
	}

	code := -1
	if ec != nil {
		code = *ec
	}
	if r.noColor {
		fmt.Fprintf(os.Stderr, "[%-*s] exited %d in %s\n", r.nameWidth, state.Name, code, dur)
	} else {
		fmt.Fprintf(os.Stderr, "[%-*s] \u2717 exited %d in %s\n", r.nameWidth, state.Name, code, dur)
	}

	// Print full buffered output immediately for non-zero exits.
	output := state.FullOutput
	if len(output) > 0 {
		fmt.Fprintf(os.Stderr, "\n--- %s output ---\n", state.Name)
		fmt.Fprintln(os.Stderr, strings.Join(output, "\n"))
		fmt.Fprintln(os.Stderr, "---")
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
	fmt.Fprintf(os.Stderr, "\n%d done, %d error(s), %s\n", doneCount, errCount, formatDuration(duration))
}
