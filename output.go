package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type runnerJSON struct {
	Name       string `json:"name"`
	Success    bool   `json:"success"`
	ExitCode   *int   `json:"exit_code"`
	Duration   string `json:"duration"`
	DurationMS int64  `json:"duration_ms"`
	LogFile    string `json:"log_file,omitempty"`
}

type outputJSON struct {
	Success    bool         `json:"success"`
	Duration   string       `json:"duration"`
	DurationMS int64        `json:"duration_ms"`
	Runners    []runnerJSON `json:"runners"`
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
func WriteJSON(runners []*Runner, totalDuration time.Duration) {
	out := outputJSON{
		Success:    true,
		Duration:   formatDuration(totalDuration),
		DurationMS: totalDuration.Milliseconds(),
		Runners:    make([]runnerJSON, 0, len(runners)),
	}

	for _, r := range runners {
		state := r.State
		ec := state.ExitCode
		ok := ec != nil && *ec == 0
		if !ok {
			out.Success = false
		}

		var dur time.Duration
		var durMS int64
		if !state.StartedAt.IsZero() {
			end := state.EndedAt
			if end.IsZero() {
				end = time.Now()
			}
			dur = end.Sub(state.StartedAt)
			durMS = dur.Milliseconds()
		}

		out.Runners = append(out.Runners, runnerJSON{
			Name:       state.Name,
			Success:    ok,
			ExitCode:   ec,
			Duration:   formatDuration(dur),
			DurationMS: durMS,
			LogFile:    state.LogFile,
		})
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error marshalling JSON: %v\n", err)
		return
	}
	os.Stdout.Write(data)         //nolint:errcheck
	os.Stdout.Write([]byte("\n")) //nolint:errcheck
}
