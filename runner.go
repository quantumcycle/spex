package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Status represents the lifecycle state of a runner.
type Status int

const (
	StatusPending Status = iota
	StatusRunning
	StatusDone
	StatusError
)

// RingBuffer holds the last N lines of output, thread-safe.
type RingBuffer struct {
	mu    sync.Mutex
	lines []string
	size  int
	pos   int
	count int
}

func NewRingBuffer(size int) *RingBuffer {
	rb := &RingBuffer{size: size}
	if size > 0 {
		rb.lines = make([]string, size)
	}
	return rb
}

func (rb *RingBuffer) Add(line string) {
	if rb.size == 0 {
		return
	}
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.lines[rb.pos] = line
	rb.pos = (rb.pos + 1) % rb.size
	if rb.count < rb.size {
		rb.count++
	}
}

// Lines returns stored lines in insertion order (oldest first).
func (rb *RingBuffer) Lines() []string {
	if rb.size == 0 {
		return nil
	}
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if rb.count == 0 {
		return nil
	}
	result := make([]string, rb.count)
	start := 0
	if rb.count == rb.size {
		start = rb.pos
	}
	for i := 0; i < rb.count; i++ {
		result[i] = rb.lines[(start+i)%rb.size]
	}
	return result
}

// RunnerState is the shared state for one subprocess.
type RunnerState struct {
	mu         sync.Mutex
	Name       string
	Cmd        string
	Status     Status
	ExitCode   *int
	Output     *RingBuffer // last N lines
	FullOutput []string    // all lines (for failed processes)
	StartedAt  time.Time
	EndedAt    time.Time
	LogFile    string // absolute path to log file; empty if --log-dir not set
	cmd        *exec.Cmd
}

// safeFilename replaces any character that isn't alphanumeric, '-', or '_' with '_'.
func safeFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// ReadSnapshot returns a consistent snapshot of the time-sensitive fields
// under the state mutex. Safe to call from any goroutine (e.g., View()).
func (rs *RunnerState) ReadSnapshot() (status Status, exitCode *int, startedAt, endedAt time.Time) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.Status, rs.ExitCode, rs.StartedAt, rs.EndedAt
}

// Signal sends a signal to the runner's process group.
func (rs *RunnerState) Signal(sig syscall.Signal) {
	rs.mu.Lock()
	cmd := rs.cmd
	rs.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, sig) //nolint:errcheck
	}
}

// ForceKill sends SIGKILL to the runner's process group.
func (rs *RunnerState) ForceKill() {
	rs.mu.Lock()
	cmd := rs.cmd
	rs.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) //nolint:errcheck
	}
}

// Runner manages a single subprocess.
type Runner struct {
	State *RunnerState
}

func NewRunner(name, cmd string, tail int, logDir, runSeed string) *Runner {
	state := &RunnerState{
		Name:   name,
		Cmd:    cmd,
		Status: StatusPending,
		Output: NewRingBuffer(tail),
	}
	if logDir != "" {
		state.LogFile = filepath.Join(logDir, safeFilename(name)+"-"+runSeed+".log")
	}
	return &Runner{State: state}
}

// Run starts the command, streams output, and invokes callbacks.
//
//   - onLine is called (from the scanner goroutine) for each output line.
//     May be nil if line-by-line notification is not needed.
//   - onDone is called exactly once with the final RunnerState after the
//     process exits and all output has been drained.
func (r *Runner) Run(onLine func(string), onDone func(*RunnerState)) {
	state := r.State

	// OS pipe gives us a single FD we can pass directly as Stdout and Stderr.
	pr, pw, err := os.Pipe()
	if err != nil {
		state.mu.Lock()
		state.Status = StatusError
		code := -1
		state.ExitCode = &code
		state.EndedAt = time.Now()
		state.mu.Unlock()
		onDone(state)
		return
	}

	cmd := exec.Command("sh", "-c", state.Cmd)
	cmd.Stdout = pw
	cmd.Stderr = pw
	// New process group so signals can be sent to the whole tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	state.mu.Lock()
	state.Status = StatusRunning
	state.StartedAt = time.Now()
	state.cmd = cmd
	state.mu.Unlock()

	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		state.mu.Lock()
		state.Status = StatusError
		code := -1
		state.ExitCode = &code
		state.EndedAt = time.Now()
		state.mu.Unlock()
		onDone(state)
		return
	}

	// Parent closes its write end; the child holds the only remaining writer.
	// When the child exits the OS closes its copy, giving the reader EOF.
	pw.Close()

	var logFile *os.File
	if state.LogFile != "" {
		f, err := os.Create(state.LogFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: cannot create log file %s: %v\n", state.LogFile, err)
			state.LogFile = ""
		} else {
			logFile = f
		}
	}

	var scanWg sync.WaitGroup
	scanWg.Add(1)
	go func() {
		defer scanWg.Done()
		defer pr.Close()
		defer func() {
			if logFile != nil {
				logFile.Close()
			}
		}()
		scanner := bufio.NewScanner(pr)
		for scanner.Scan() {
			line := scanner.Text()
			state.mu.Lock()
			if strings.TrimSpace(line) != "" {
				state.Output.Add(line)
			}
			state.FullOutput = append(state.FullOutput, line)
			state.mu.Unlock()
			if logFile != nil {
				fmt.Fprintln(logFile, line)
			}
			if onLine != nil {
				onLine(line)
			}
		}
	}()

	waitErr := cmd.Wait()
	scanWg.Wait() // drain any buffered pipe data before reading state

	state.mu.Lock()
	state.EndedAt = time.Now()
	exitCode := 0
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
		state.Status = StatusError
	} else {
		state.Status = StatusDone
	}
	state.ExitCode = &exitCode
	state.mu.Unlock()

	onDone(state)
}
