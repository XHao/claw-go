package client

import (
	"fmt"
	"sync"
	"time"
)

// spinnerFrames are the animation frames shown while waiting for the LLM.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Spinner shows an animated indicator in the terminal while waiting.
// Call Stop() to clear it and return the cursor to the beginning of the line.
type Spinner struct {
	label  string
	stop   chan struct{}
	done   chan struct{}
	mu     sync.Mutex
	active bool
}

// NewSpinner creates and immediately starts a spinner.
func NewSpinner(label string) *Spinner {
	s := &Spinner{
		label: label,
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	s.active = true
	go s.run()
	return s
}

func (s *Spinner) run() {
	defer close(s.done)
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()
	i := 0
	for {
		select {
		case <-s.stop:
			// Erase the spinner line cleanly.
			fmt.Printf("\r\033[K")
			return
		case <-ticker.C:
			s.mu.Lock()
			label := s.label
			s.mu.Unlock()
			frame := spinnerFrames[i%len(spinnerFrames)]
			fmt.Printf("\r%s", S.Dim(frame+" "+label))
			i++
		}
	}
}

// Stop halts the spinner and clears the line.
func (s *Spinner) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return
	}
	s.active = false
	close(s.stop)
	<-s.done
}

// UpdateLabel changes the label shown next to the spinner (thread-safe).
func (s *Spinner) UpdateLabel(label string) {
	s.mu.Lock()
	s.label = label
	s.mu.Unlock()
}
