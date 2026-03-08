package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

type runnerJSON struct {
	Name     string `json:"name"`
	Ok       bool   `json:"ok"`
	ExitCode *int   `json:"exit_code"`
	Duration string `json:"duration"`
	Output   string `json:"output"`
}

type outputJSON struct {
	Ok       bool         `json:"ok"`
	Duration string       `json:"duration"`
	Runners  []runnerJSON `json:"runners"`
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%dm%ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// WriteJSON marshals the final run summary to stdout.
// Must be called only after all runner goroutines have exited.
func WriteJSON(runners []*Runner, totalDuration time.Duration, verbose bool) {
	out := outputJSON{
		Ok:      true,
		Duration: formatDuration(totalDuration),
		Runners:  make([]runnerJSON, 0, len(runners)),
	}

	for _, r := range runners {
		state := r.State
		ec := state.ExitCode
		ok := ec != nil && *ec == 0
		if !ok {
			out.Ok = false
		}

		var outputStr string
		if !ok || verbose {
			if len(state.FullOutput) > 0 {
				outputStr = strings.Join(state.FullOutput, "\n")
			}
		} else if lines := state.Output.Lines(); len(lines) > 0 {
			outputStr = strings.Join(lines, "\n")
		}

		var dur time.Duration
		if !state.StartedAt.IsZero() {
			end := state.EndedAt
			if end.IsZero() {
				end = time.Now()
			}
			dur = end.Sub(state.StartedAt)
		}

		out.Runners = append(out.Runners, runnerJSON{
			Name:     state.Name,
			Ok:       ok,
			ExitCode: ec,
			Duration: formatDuration(dur),
			Output:   outputStr,
		})
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error marshalling JSON: %v\n", err)
		return
	}
	os.Stdout.Write(data)   //nolint:errcheck
	os.Stdout.Write([]byte("\n")) //nolint:errcheck
}
