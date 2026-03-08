package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
)

func main() {
	var (
		maxParallel int
		tail        int
		failFast    bool
		verbose     bool
		name        string
	)

	flag.IntVar(&maxParallel, "max-parallel", 4, "Max number of concurrent processes")
	flag.IntVar(&maxParallel, "p", 4, "Max number of concurrent processes (shorthand)")
	flag.IntVar(&tail, "tail", 10, "Number of output lines shown per process in the status board")
	flag.IntVar(&tail, "n", 10, "Number of output lines shown per process (shorthand)")
	flag.BoolVar(&failFast, "fail-fast", false, "Kill all running processes when one exits non-zero")
	flag.BoolVar(&verbose, "verbose", false, "Print full output for all runners, not just failures")
	flag.BoolVar(&verbose, "v", false, "Print full output for all runners (shorthand)")
	flag.StringVar(&name, "name", "spex", "Label shown in the status header")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: spex [flags] <<EOF\n")
		fmt.Fprintf(os.Stderr, "  name1<TAB>command1\n")
		fmt.Fprintf(os.Stderr, "  name2<TAB>command2\n")
		fmt.Fprintf(os.Stderr, "EOF\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if term.IsTerminal(int(os.Stdin.Fd())) {
		flag.Usage()
		os.Exit(0)
	}

	// Read name<TAB>cmd pairs from stdin.
	var runners []*Runner
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "skipping invalid line (expected name<TAB>cmd): %q\n", line)
			continue
		}
		name := strings.TrimSpace(parts[0])
		cmd := parts[1]
		if name == "" || cmd == "" {
			fmt.Fprintf(os.Stderr, "skipping invalid line (empty name or cmd): %q\n", line)
			continue
		}
		runners = append(runners, NewRunner(name, cmd, tail))
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "reading stdin: %v\n", err)
		os.Exit(1)
	}
	if len(runners) == 0 {
		fmt.Fprintln(os.Stderr, "no runners specified on stdin")
		os.Exit(1)
	}

	noColor := os.Getenv("NO_COLOR") != ""
	isTTY := term.IsTerminal(int(os.Stderr.Fd()))

	nameWidth := 0
	for _, r := range runners {
		if len(r.State.Name) > nameWidth {
			nameWidth = len(r.State.Name)
		}
	}

	start := time.Now()

	if isTTY && !noColor {
		runTUI(runners, maxParallel, failFast, verbose, name, tail, nameWidth, start)
	} else {
		runCI(runners, maxParallel, failFast, verbose, noColor, name, nameWidth, start)
	}
}

// runCI runs the CI (non-TTY) path: plain line-by-line stderr output.
func runCI(runners []*Runner, maxParallel int, failFast bool, verbose bool, noColor bool, name string, nameWidth int, start time.Time) {
	renderer := NewCIRenderer(nameWidth, noColor, verbose)
	sem := make(chan struct{}, maxParallel)
	doneCh := make(chan *RunnerState, len(runners))

	var (
		cancelled bool
		cancelMu  sync.Mutex
	)
	cancel := func() {
		cancelMu.Lock()
		cancelled = true
		cancelMu.Unlock()
	}
	isCancelled := func() bool {
		cancelMu.Lock()
		defer cancelMu.Unlock()
		return cancelled
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	var wg sync.WaitGroup
	for _, r := range runners {
		r := r
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}        // acquire
			defer func() { <-sem }() // release

			if isCancelled() {
				doneCh <- r.State
				return
			}
			renderer.OnStart(r.State)
			r.Run(nil, func(s *RunnerState) { doneCh <- s })
		}()
	}

	go func() {
		wg.Wait()
		close(doneCh)
	}()

	var signalReceived os.Signal

loop:
	for {
		select {
		case state, ok := <-doneCh:
			if !ok {
				break loop
			}
			renderer.OnDone(state)
			if failFast && !isCancelled() {
				ec := state.ExitCode
				if ec == nil || *ec != 0 {
					cancel()
					for _, runner := range runners {
						runner.State.Signal(syscall.SIGTERM)
					}
				}
			}

		case sig := <-sigCh:
			signalReceived = sig
			cancel()
			sysSig := sig.(syscall.Signal)
			for _, runner := range runners {
				runner.State.Signal(sysSig)
			}
			go func() {
				time.Sleep(5 * time.Second)
				for _, runner := range runners {
					runner.State.ForceKill()
				}
			}()
		}
	}

	elapsed := time.Since(start)
	renderer.Summary(runners, elapsed)
	WriteJSON(runners, elapsed, verbose)
	exitWithCode(runners, signalReceived)
}

// runTUI runs the interactive TUI path using bubbletea.
func runTUI(runners []*Runner, maxParallel int, failFast bool, verbose bool, name string, tail int, nameWidth int, start time.Time) {
	var (
		cancelled bool
		cancelMu  sync.Mutex
	)
	cancel := func() {
		cancelMu.Lock()
		cancelled = true
		cancelMu.Unlock()
	}
	isCancelled := func() bool {
		cancelMu.Lock()
		defer cancelMu.Unlock()
		return cancelled
	}

	model := NewModel(runners, failFast, name, tail, nameWidth, cancel, start)

	// When stdin is consumed (piped runner list), bubbletea needs /dev/tty for
	// raw-mode setup and keyboard events.
	tuiInput := os.Stdin
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		if tty, err := os.Open("/dev/tty"); err == nil {
			defer tty.Close()
			tuiInput = tty
		}
	}

	p := tea.NewProgram(
		model,
		tea.WithInput(tuiInput),
		tea.WithOutput(os.Stderr),
		// No tea.WithAltScreen() — output scrolls naturally.
	)

	// Signal handling: forward to runners and notify the model.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	var signalReceived os.Signal

	go func() {
		sig, ok := <-sigCh
		if !ok {
			return
		}
		signalReceived = sig
		cancel()
		sysSig := sig.(syscall.Signal)
		for _, r := range runners {
			r.State.Signal(sysSig)
		}
		// Force-kill anything still alive after 5 seconds.
		go func() {
			time.Sleep(5 * time.Second)
			for _, r := range runners {
				r.State.ForceKill()
			}
		}()
		p.Send(SigMsg{Signal: sig})
	}()

	// Launch runner goroutines. Each sends messages to the bubbletea program.
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for _, r := range runners {
		r := r
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}        // acquire
			defer func() { <-sem }() // release

			if isCancelled() {
				p.Send(RunnerDoneMsg{Name: r.State.Name, Skipped: true})
				return
			}

			p.Send(RunnerStartedMsg{Name: r.State.Name})
			r.Run(
				func(line string) {
					p.Send(RunnerOutputMsg{Name: r.State.Name, Line: line})
				},
				func(s *RunnerState) {
					p.Send(RunnerDoneMsg{Name: s.Name})
				},
			)
		}()
	}

	// Run the TUI; blocks until the model calls tea.Quit.
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
	}

	signal.Stop(sigCh)
	close(sigCh)
	wg.Wait()

	elapsed := time.Since(start)
	PrintFinalSummary(runners, elapsed, name, nameWidth, verbose)
	WriteJSON(runners, elapsed, verbose)
	exitWithCode(runners, signalReceived)
}

// exitWithCode determines the process exit code and exits.
func exitWithCode(runners []*Runner, sig os.Signal) {
	if sig != nil {
		if sig == syscall.SIGINT {
			os.Exit(130)
		}
		os.Exit(143)
	}
	for _, r := range runners {
		ec := r.State.ExitCode
		if ec == nil || *ec != 0 {
			os.Exit(1)
		}
	}
	os.Exit(0)
}
