package ui

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"
)

// SimpleTerminal provides a basic terminal UI that works with Warp and other modern terminals
type SimpleTerminal struct {
	stats       Stats
	mu          sync.RWMutex
	stopCh      chan struct{}
	pauseCh     chan bool
	rateCh      chan int
	quitCh      chan struct{}
	messageCh   chan string
	message     string
	msgTimer    *time.Timer

	// Terminal state
	oldState    *term.State
	isPaused    atomic.Bool
	isRunning   atomic.Bool
	lastLines   int
}

func NewSimpleTerminal() *SimpleTerminal {
	t := &SimpleTerminal{
		stopCh:    make(chan struct{}),
		pauseCh:   make(chan bool, 1),
		rateCh:    make(chan int, 1),
		quitCh:    make(chan struct{}, 1),
		messageCh: make(chan string, 10),
		stats: Stats{
			StartTime: time.Now(),
		},
		lastLines: 15, // Expected number of lines in our UI
	}
	return t
}

func (t *SimpleTerminal) Run() {
	// Set terminal to raw mode for input
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		// Fall back to normal mode if raw mode fails
		fmt.Printf("Warning: Could not set raw mode: %v\n", err)
	} else {
		t.oldState = oldState
	}

	t.isRunning.Store(true)

	// Initial render
	t.clearScreen()
	t.render()

	// Start goroutines
	go t.updateLoop()
	go t.keyboardLoop()
	go t.messageHandler()

	// Wait for quit signal
	<-t.quitCh
	t.Stop()
}

func (t *SimpleTerminal) keyboardLoop() {
	reader := bufio.NewReader(os.Stdin)

	for t.isRunning.Load() {
		char, err := reader.ReadByte()
		if err != nil {
			continue
		}

		switch char {
		case 'p', 'P':
			t.isPaused.Store(!t.isPaused.Load())
			select {
			case t.pauseCh <- true:
			default:
			}
			if t.isPaused.Load() {
				t.ShowMessage("⏸  Blasting paused")
			} else {
				t.ShowMessage("▶  Blasting resumed")
			}

		case 'r', 'R':
			t.handleRateChange()

		case 'q', 'Q', 3: // 3 is Ctrl+C
			select {
			case t.quitCh <- struct{}{}:
			default:
			}
			return

		case 'h', 'H':
			t.showHelp()
		}
	}
}

func (t *SimpleTerminal) handleRateChange() {
	// Temporarily restore terminal for input
	if t.oldState != nil {
		term.Restore(int(os.Stdin.Fd()), t.oldState)
	}

	// Move to bottom of our UI area and clear line
	fmt.Printf("\033[%d;1H\033[K", t.lastLines+1)
	fmt.Print("Enter new rate (TPS): ")
	fmt.Print("\033[?25h") // Show cursor

	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)

	fmt.Print("\033[?25l") // Hide cursor

	if rate, err := strconv.Atoi(input); err == nil && rate > 0 {
		select {
		case t.rateCh <- rate:
			t.ShowMessage(fmt.Sprintf("Rate changed to %d TPS", rate))
		default:
		}
	}

	// Back to raw mode
	if t.oldState != nil {
		term.MakeRaw(int(os.Stdin.Fd()))
	}

	// Clear the input line and re-render
	fmt.Printf("\033[%d;1H\033[K", t.lastLines+1)
	t.render()
}

func (t *SimpleTerminal) showHelp() {
	// Save current position
	fmt.Print("\033[s")

	// Move to bottom and show help
	fmt.Printf("\033[%d;1H\033[K", t.lastLines+2)
	fmt.Println("\033[93mControls: [P]ause [R]ate [Q]uit [H]elp\033[0m")
	fmt.Println("\033[93mPress any key to continue...\033[0m")

	reader := bufio.NewReader(os.Stdin)
	reader.ReadByte()

	// Clear help text and restore position
	fmt.Printf("\033[%d;1H\033[K", t.lastLines+2)
	fmt.Printf("\033[%d;1H\033[K", t.lastLines+3)
	fmt.Print("\033[u")
}

func (t *SimpleTerminal) updateLoop() {
	ticker := time.NewTicker(500 * time.Millisecond) // Slower update rate for Warp
	defer ticker.Stop()

	for {
		select {
		case <-t.stopCh:
			return
		case <-ticker.C:
			if t.isRunning.Load() {
				t.render()
			}
		}
	}
}

func (t *SimpleTerminal) messageHandler() {
	for {
		select {
		case <-t.stopCh:
			return
		case msg := <-t.messageCh:
			t.mu.Lock()
			t.message = msg
			if t.msgTimer != nil {
				t.msgTimer.Stop()
			}
			t.msgTimer = time.AfterFunc(3*time.Second, func() {
				t.mu.Lock()
				t.message = ""
				t.mu.Unlock()
			})
			t.mu.Unlock()
		}
	}
}

func (t *SimpleTerminal) clearScreen() {
	fmt.Print("\033[2J\033[H\033[?25l") // Clear screen, move to top, hide cursor
}

func (t *SimpleTerminal) render() {
	t.mu.RLock()
	stats := t.stats
	msg := t.message
	isPaused := t.isPaused.Load()
	t.mu.RUnlock()

	runtime := time.Since(stats.StartTime)

	// Calculate rates
	var overallTPS, successRate float64
	total := stats.TotalSent + stats.TotalFailures
	if runtime.Seconds() > 0 {
		overallTPS = float64(stats.TotalSent) / runtime.Seconds()
	}
	if total > 0 {
		successRate = float64(stats.TotalSent) * 100 / float64(total)
	}

	// Move cursor to home without clearing (to avoid flicker)
	fmt.Print("\033[H")

	// Status line
	statusColor := "\033[32m" // green
	statusText := "RUNNING"
	if isPaused {
		statusColor = "\033[33m" // yellow
		statusText = "PAUSED "
	}

	// Print UI with fixed positions
	fmt.Printf("\033[1;1H\033[36m━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\033[0m\033[K\n")
	fmt.Printf("\033[2;1H\033[1;36m TX BLASTER - Interactive Mode\033[0m\033[K\n")
	fmt.Printf("\033[3;1H\033[36m━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\033[0m\033[K\n")
	fmt.Printf("\033[4;1H Status: %s%-7s\033[0m  Runtime: %-20s\033[K\n", statusColor, statusText, formatDuration(runtime))
	fmt.Printf("\033[5;1H\033[K\n")

	// Stats section
	fmt.Printf("\033[6;1H \033[1mStatistics:\033[0m\033[K\n")
	fmt.Printf("\033[7;1H   Sent: \033[32m%-10d\033[0m   Success Rate: %.1f%%\033[K\n", stats.TotalSent, successRate)
	fmt.Printf("\033[8;1H   Failed: \033[31m%-10d\033[0m   Overall TPS: %.2f\033[K\n", stats.TotalFailures, overallTPS)
	fmt.Printf("\033[9;1H   Duplicates: \033[33m%-10d\033[0m   Target TPS: %d\033[K\n", stats.TotalDuplicate, stats.TargetRate)
	fmt.Printf("\033[10;1H\033[K\n")

	// Current activity
	currentUTXO := stats.CurrentUTXO
	if currentUTXO == "" {
		currentUTXO = "Waiting..."
	}
	fmt.Printf("\033[11;1H \033[1mCurrent:\033[0m %s\033[K\n", currentUTXO)
	fmt.Printf("\033[12;1H\033[K\n")

	// Message line
	if msg != "" {
		fmt.Printf("\033[13;1H \033[93m➤ %s\033[0m\033[K\n", msg)
	} else {
		fmt.Printf("\033[13;1H\033[K\n")
	}

	// Controls
	fmt.Printf("\033[14;1H\033[36m━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\033[0m\033[K\n")
	fmt.Printf("\033[15;1H [P]ause [R]ate [Q]uit [H]elp\033[K\n")
}

func (t *SimpleTerminal) UpdateStats(stats Stats) {
	t.mu.Lock()
	t.stats = stats
	t.stats.LastUpdateTime = time.Now()
	t.mu.Unlock()
}

func (t *SimpleTerminal) ShowMessage(msg string) {
	select {
	case t.messageCh <- msg:
	default:
	}
}

func (t *SimpleTerminal) Stop() {
	t.isRunning.Store(false)
	close(t.stopCh)

	// Restore terminal
	if t.oldState != nil {
		term.Restore(int(os.Stdin.Fd()), t.oldState)
	}

	// Show cursor and clear screen
	fmt.Print("\033[?25h\033[2J\033[H")
}

func (t *SimpleTerminal) PauseChannel() <-chan bool {
	return t.pauseCh
}

func (t *SimpleTerminal) RateChannel() <-chan int {
	return t.rateCh
}

func (t *SimpleTerminal) QuitChannel() <-chan struct{} {
	return t.quitCh
}