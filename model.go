package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Messages flowing into the bubbletea event loop.

// RunnerStartedMsg is sent by a goroutine just before calling runner.Run().
type RunnerStartedMsg struct{ Name string }

// RunnerOutputMsg is sent for each line of output from a running process.
type RunnerOutputMsg struct{ Name, Line string }

// RunnerDoneMsg is sent when a runner's process exits.
// If Skipped is true, the runner was cancelled before it could start.
type RunnerDoneMsg struct {
	Name    string
	Skipped bool
}

// TickMsg drives the spinner animation at 100 ms intervals.
type TickMsg time.Time

// SigMsg is sent to the model when SIGINT/SIGTERM is received.
type SigMsg struct{ Signal os.Signal }

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return TickMsg(t)
	})
}

// lipgloss renderer and styles, all backed by stderr so that color capability
// is detected against the TUI output stream rather than stdout.
var (
	stderrRenderer = lipgloss.NewRenderer(os.Stderr)
	styleHeader    = stderrRenderer.NewStyle().Bold(true)
	stylePending   = stderrRenderer.NewStyle().Faint(true)
	styleSpinner   = stderrRenderer.NewStyle().Foreground(lipgloss.Color("214")) // amber
	styleSuccess   = stderrRenderer.NewStyle().Foreground(lipgloss.Color("82"))  // green
	styleError     = stderrRenderer.NewStyle().Foreground(lipgloss.Color("196")) // red
	styleTail      = stderrRenderer.NewStyle().Faint(true)
)

// Model is the bubbletea model for the live TUI status board.
type Model struct {
	runners   []*Runner
	name      string
	nameWidth int
	tail      int
	failFast  bool
	cancelFn  func() // shared cancel function from the outer goroutine scope

	doneCount    int
	cancelled    bool // model-local tracking; prevents double fail-fast
	spinnerFrame int
	width        int
	finished     bool
	start        time.Time
}

func NewModel(runners []*Runner, failFast bool, name string, tail int, nameWidth int, cancelFn func(), start time.Time) Model {
	return Model{
		runners:   runners,
		name:      name,
		tail:      tail,
		nameWidth: nameWidth,
		failFast:  failFast,
		cancelFn:  cancelFn,
		start:     start,
		width:     80, // overwritten by the first tea.WindowSizeMsg
	}
}

func (m Model) Init() tea.Cmd {
	return tickCmd()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case RunnerStartedMsg, RunnerOutputMsg:
		// State is already updated on the shared RunnerState; just re-render.
		return m, nil

	case RunnerDoneMsg:
		m.doneCount++

		// Trigger fail-fast if this runner exited non-zero.
		if !msg.Skipped && m.failFast && !m.cancelled {
			for _, r := range m.runners {
				if r.State.Name == msg.Name {
					_, ec, _, _ := r.State.ReadSnapshot()
					if ec != nil && *ec != 0 {
						m.cancelled = true
						m.cancelFn()
						for _, runner := range m.runners {
							runner.State.Signal(syscall.SIGTERM)
						}
					}
					break
				}
			}
		}

		if m.doneCount == len(m.runners) {
			m.finished = true
			return m, tea.Quit
		}
		return m, nil

	case SigMsg:
		// Cancel already signalled to runners by the signal goroutine; just
		// sync the model's local flag to suppress spurious fail-fast triggers.
		if !m.cancelled {
			m.cancelled = true
			m.cancelFn()
		}
		return m, nil

	case TickMsg:
		m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
		if m.finished {
			return m, nil
		}
		return m, tickCmd()

	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	}

	return m, nil
}

func (m Model) View() string {
	// When finished, return an empty string so bubbletea clears the live board.
	// The final summary is printed imperatively after p.Run() returns.
	if m.finished {
		return ""
	}

	var sb strings.Builder

	// Count states for the header.
	var running, done, pending int
	for _, r := range m.runners {
		status, _, _, _ := r.State.ReadSnapshot()
		switch status {
		case StatusRunning:
			running++
		case StatusDone, StatusError:
			done++
		default:
			pending++
		}
	}
	total := len(m.runners)

	// Header line.
	header := styleHeader.Render(m.name) + fmt.Sprintf("  %d/%d done", done, total)
	if running > 0 {
		header += fmt.Sprintf(" • %d running", running)
	}
	if pending > 0 {
		header += fmt.Sprintf(" • %d pending", pending)
	}
	sb.WriteString(header + "\n\n")

	// Per-runner lines — done first, then running, then pending.
	sorted := make([]*Runner, len(m.runners))
	copy(sorted, m.runners)
	statusRank := func(r *Runner) int {
		s, _, _, _ := r.State.ReadSnapshot()
		switch s {
		case StatusDone, StatusError:
			return 0
		case StatusRunning:
			return 1
		default:
			return 2
		}
	}
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && statusRank(sorted[j]) < statusRank(sorted[j-1]); j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	for _, r := range sorted {
		status, ec, startedAt, endedAt := r.State.ReadSnapshot()

		switch status {
		case StatusPending:
			icon := stylePending.Render("·")
			name := stylePending.Render(fmt.Sprintf("%-*s", m.nameWidth, r.State.Name))
			sb.WriteString(fmt.Sprintf("  %s %s  pending\n", icon, name))

		case StatusRunning:
			icon := styleSpinner.Render(spinnerFrames[m.spinnerFrame])
			dur := formatDuration(time.Since(startedAt))
			sb.WriteString(fmt.Sprintf("  %s %-*s  %s\n", icon, m.nameWidth, r.State.Name, dur))
			maxLen := m.width - 6
			if maxLen < 10 {
				maxLen = 10
			}
			lines := r.State.Output.Lines()
			for range m.tail - len(lines) {
				sb.WriteString("\n")
			}
			for _, line := range lines {
				sb.WriteString(styleTail.Render("    ↳ "+truncate(line, maxLen)) + "\n")
			}

		case StatusDone, StatusError:
			dur := formatDuration(endedAt.Sub(startedAt))
			if ec != nil && *ec == 0 {
				icon := styleSuccess.Render("✓")
				sb.WriteString(fmt.Sprintf("  %s %-*s  %s\n", icon, m.nameWidth, r.State.Name, dur))
			} else {
				icon := styleError.Render("✗")
				code := -1
				if ec != nil {
					code = *ec
				}
				sb.WriteString(fmt.Sprintf("  %s %-*s  %s  (exit %d)\n",
					icon, m.nameWidth, r.State.Name, dur, code))
			}
		}
	}

	return sb.String()
}

// PrintFinalSummary writes the end-of-run summary to stderr.
// Must be called after the bubbletea program has exited (all goroutines done).
//
// outputTokens controls which output is shown:
//   - Single token ("errors", "success", "all"): show matching category only.
//   - Multiple tokens (e.g. ["success","errors"]): show in the given order.
func PrintFinalSummary(runners []*Runner, elapsed time.Duration, name string, nameWidth int, outputTokens []string) {
	total := len(runners)
	fmt.Fprintf(os.Stderr, "%s  %d/%d done in %s\n\n",
		styleHeader.Render(name), total, total, formatDuration(elapsed))

	byCategory := map[string][]*RunnerState{"success": {}, "errors": {}}
	for _, r := range runners {
		state := r.State
		_, ec, startedAt, endedAt := state.ReadSnapshot()

		var dur string
		if !startedAt.IsZero() && !endedAt.IsZero() {
			dur = formatDuration(endedAt.Sub(startedAt))
		} else {
			dur = "cancelled"
		}

		if ec != nil && *ec == 0 {
			fmt.Fprintf(os.Stderr, "  %s %-*s  %s\n",
				styleSuccess.Render("✓"), nameWidth, state.Name, dur)
			byCategory["success"] = append(byCategory["success"], state)
		} else {
			code := -1
			if ec != nil {
				code = *ec
			}
			if code >= 0 {
				fmt.Fprintf(os.Stderr, "  %s %-*s  %s  (exit %d)\n",
					styleError.Render("✗"), nameWidth, state.Name, dur, code)
			} else {
				fmt.Fprintf(os.Stderr, "  %s %-*s  %s\n",
					styleError.Render("✗"), nameWidth, state.Name, dur)
			}
			byCategory["errors"] = append(byCategory["errors"], state)
		}
	}

	printOutput := func(state *RunnerState) {
		state.mu.Lock()
		output := state.FullOutput
		state.mu.Unlock()
		printOutputBlock(os.Stderr, state.Name, output)
	}

	if len(outputTokens) == 1 && outputTokens[0] == "all" {
		for _, r := range runners {
			printOutput(r.State)
		}
		return
	}

	// Single token or multi-token: iterate tokens in order and print matching states.
	for _, token := range outputTokens {
		for _, state := range byCategory[token] {
			printOutput(state)
		}
	}
}

// truncate shortens s to at most maxLen runes, appending "…" if truncated.
func truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen == 1 {
		return "…"
	}
	return string(runes[:maxLen-1]) + "…"
}
