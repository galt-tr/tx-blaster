package ui

import (
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type Stats struct {
	TotalSent      int64
	TotalFailures  int64
	TotalDuplicate int64
	CurrentUTXO    string
	CurrentRate    int
	TargetRate     int
	StartTime      time.Time
	IsPaused       bool
	LastUpdateTime time.Time
}

type TviewTerminal struct {
	app         *tview.Application
	stats       Stats
	mu          sync.RWMutex
	stopCh      chan struct{}
	pauseCh     chan bool
	rateCh      chan int
	quitCh      chan struct{}

	// UI components
	pages       *tview.Pages
	mainFlex    *tview.Flex
	statusView  *tview.TextView
	statsTable  *tview.Table
	activityView *tview.TextView
	messageView *tview.TextView
	helpView    *tview.TextView
}

func NewTviewTerminal() *TviewTerminal {
	t := &TviewTerminal{
		app:      tview.NewApplication(),
		stopCh:   make(chan struct{}),
		pauseCh:  make(chan bool, 1),
		rateCh:   make(chan int, 1),
		quitCh:   make(chan struct{}, 1),
		stats: Stats{
			StartTime: time.Now(),
		},
	}

	t.setupUI()
	return t
}

func (t *TviewTerminal) setupUI() {
	// Create header
	header := tview.NewTextView().
		SetText("[yellow::b]TX BLASTER - Interactive Mode[-::-]").
		SetTextAlign(tview.AlignCenter).
		SetDynamicColors(true)
	header.SetBorder(true)

	// Create status bar
	t.statusView = tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignLeft)
	t.statusView.SetBorder(true).SetTitle(" Status ")

	// Create statistics table
	t.statsTable = tview.NewTable().
		SetBorders(false)
	t.statsTable.SetBorder(true).SetTitle(" Statistics ")
	t.initStatsTable()

	// Create activity view
	t.activityView = tview.NewTextView().
		SetDynamicColors(true)
	t.activityView.SetBorder(true).SetTitle(" Current Activity ")

	// Create message view
	t.messageView = tview.NewTextView().
		SetDynamicColors(true).
		SetMaxLines(3)
	t.messageView.SetBorder(true).SetTitle(" Messages ")

	// Create help view
	t.helpView = tview.NewTextView().
		SetDynamicColors(true).
		SetText(" [cyan::b]P[-::-] Pause/Resume  [cyan::b]R[-::-] Change Rate  [cyan::b]Q[-::-] Quit  [cyan::b]H[-::-] Help")
	t.helpView.SetBorder(true).SetTitle(" Controls ")

	// Create main layout using Flex (more reliable than Grid)
	t.mainFlex = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(header, 3, 0, false).
		AddItem(t.statusView, 3, 0, false).
		AddItem(t.statsTable, 8, 0, false).
		AddItem(t.activityView, 3, 0, false).
		AddItem(t.messageView, 4, 0, false).
		AddItem(t.helpView, 3, 0, false)

	// Create pages to handle modals
	t.pages = tview.NewPages().
		AddPage("main", t.mainFlex, true, true)

	// Set up application-level keyboard handlers
	t.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		// Handle keys regardless of focus
		if event.Key() == tcell.KeyRune {
			switch event.Rune() {
			case 'p', 'P':
				go func() {
					select {
					case t.pauseCh <- true:
					default:
					}
				}()
				return nil
			case 'r', 'R':
				t.showRateDialog()
				return nil
			case 'q', 'Q':
				go func() {
					select {
					case t.quitCh <- struct{}{}:
					default:
					}
					t.app.Stop()
				}()
				return nil
			case 'h', 'H':
				t.showHelp()
				return nil
			}
		} else if event.Key() == tcell.KeyCtrlC || event.Key() == tcell.KeyEsc {
			go func() {
				select {
				case t.quitCh <- struct{}{}:
				default:
				}
				t.app.Stop()
			}()
			return nil
		}
		return event
	})

	// Set root
	t.app.SetRoot(t.pages, true)

	// Enable mouse support
	t.app.EnableMouse(true)
}

func (t *TviewTerminal) initStatsTable() {
	// Create a simple 2-column layout
	t.statsTable.SetCell(0, 0, tview.NewTableCell("Sent:").SetTextColor(tcell.ColorWhite))
	t.statsTable.SetCell(0, 1, tview.NewTableCell("0").SetTextColor(tcell.ColorGreen))
	t.statsTable.SetCell(0, 2, tview.NewTableCell("    Success Rate:").SetTextColor(tcell.ColorWhite))
	t.statsTable.SetCell(0, 3, tview.NewTableCell("0.0%").SetTextColor(tcell.ColorWhite))

	t.statsTable.SetCell(1, 0, tview.NewTableCell("Failed:").SetTextColor(tcell.ColorWhite))
	t.statsTable.SetCell(1, 1, tview.NewTableCell("0").SetTextColor(tcell.ColorRed))
	t.statsTable.SetCell(1, 2, tview.NewTableCell("    Overall TPS:").SetTextColor(tcell.ColorWhite))
	t.statsTable.SetCell(1, 3, tview.NewTableCell("0.00").SetTextColor(tcell.ColorWhite))

	t.statsTable.SetCell(2, 0, tview.NewTableCell("Duplicates:").SetTextColor(tcell.ColorWhite))
	t.statsTable.SetCell(2, 1, tview.NewTableCell("0").SetTextColor(tcell.ColorYellow))
	t.statsTable.SetCell(2, 2, tview.NewTableCell("    Target TPS:").SetTextColor(tcell.ColorWhite))
	t.statsTable.SetCell(2, 3, tview.NewTableCell("0").SetTextColor(tcell.ColorWhite))
}

func (t *TviewTerminal) Run() {
	// Start update loop
	go t.updateLoop()

	// Run the application
	if err := t.app.Run(); err != nil {
		panic(err)
	}
}

func (t *TviewTerminal) updateLoop() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-t.stopCh:
			return
		case <-ticker.C:
			t.updateDisplay()
		}
	}
}

func (t *TviewTerminal) updateDisplay() {
	t.mu.RLock()
	stats := t.stats
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

	// Update status
	statusText := fmt.Sprintf(" Status: [green::b]RUNNING[-::-]    Runtime: %s", formatDuration(runtime))
	if stats.IsPaused {
		statusText = fmt.Sprintf(" Status: [yellow::b]PAUSED[-::-]     Runtime: %s", formatDuration(runtime))
	}

	// Update views using QueueUpdateDraw for thread safety
	t.app.QueueUpdateDraw(func() {
		t.statusView.SetText(statusText)

		// Update stats table
		t.statsTable.GetCell(0, 1).SetText(fmt.Sprintf("%d", stats.TotalSent))
		t.statsTable.GetCell(0, 3).SetText(fmt.Sprintf("%.1f%%", successRate))
		t.statsTable.GetCell(1, 1).SetText(fmt.Sprintf("%d", stats.TotalFailures))
		t.statsTable.GetCell(1, 3).SetText(fmt.Sprintf("%.2f", overallTPS))
		t.statsTable.GetCell(2, 1).SetText(fmt.Sprintf("%d", stats.TotalDuplicate))
		t.statsTable.GetCell(2, 3).SetText(fmt.Sprintf("%d", stats.TargetRate))

		// Update activity
		currentUTXO := stats.CurrentUTXO
		if currentUTXO == "" {
			currentUTXO = "Waiting..."
		}
		t.activityView.SetText(fmt.Sprintf(" UTXO: [cyan]%s[-]", currentUTXO))
	})
}

func (t *TviewTerminal) showRateDialog() {
	t.mu.RLock()
	currentRate := t.stats.TargetRate
	t.mu.RUnlock()

	// Create input field
	input := tview.NewInputField().
		SetLabel("New Rate (TPS): ").
		SetFieldWidth(20).
		SetAcceptanceFunc(func(textToCheck string, lastChar rune) bool {
			if textToCheck == "" {
				return true
			}
			_, err := strconv.Atoi(textToCheck)
			return err == nil
		}).
		SetText(fmt.Sprintf("%d", currentRate))

	// Create form
	form := tview.NewForm().
		AddFormItem(input).
		AddButton("Set Rate", func() {
			if text := input.GetText(); text != "" {
				if newRate, err := strconv.Atoi(text); err == nil && newRate > 0 {
					select {
					case t.rateCh <- newRate:
						t.ShowMessage(fmt.Sprintf("Rate changed to %d TPS", newRate))
					default:
					}
				}
			}
			t.pages.RemovePage("rate-dialog")
		}).
		AddButton("Cancel", func() {
			t.pages.RemovePage("rate-dialog")
		})

	form.SetBorder(true).SetTitle("Change Blast Rate").SetTitleAlign(tview.AlignCenter)

	// Show the form as a modal
	t.pages.AddPage("rate-dialog", createModal(form, 40, 10), true, true)
}

func (t *TviewTerminal) showHelp() {
	helpText := `[yellow::b]TX Blaster Interactive Controls:[-::-]

  [green]P[-]      - Pause/Resume blasting
  [green]R[-]      - Change blast rate (TPS)
  [green]Q[-]      - Quit the application
  [green]H[-]      - Show this help
  [green]Ctrl+C[-] - Force quit

Press any key to close this help.`

	modal := tview.NewModal().
		SetText(helpText).
		AddButtons([]string{"Close"}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			t.pages.RemovePage("help")
		})

	t.pages.AddPage("help", modal, true, true)
}

func (t *TviewTerminal) UpdateStats(stats Stats) {
	t.mu.Lock()
	t.stats = stats
	t.stats.LastUpdateTime = time.Now()
	t.mu.Unlock()
}

func (t *TviewTerminal) ShowMessage(msg string) {
	timestamp := time.Now().Format("15:04:05")
	t.app.QueueUpdateDraw(func() {
		currentText := t.messageView.GetText(false)
		newText := fmt.Sprintf("[%s] %s\n%s", timestamp, msg, currentText)
		// Keep only last 3 lines
		lines := []string{}
		for i, line := range splitLines(newText) {
			if i < 3 && line != "" {
				lines = append(lines, line)
			}
		}
		t.messageView.SetText(joinLines(lines))
	})
}

func (t *TviewTerminal) Stop() {
	close(t.stopCh)
	t.app.Stop()
}

func (t *TviewTerminal) PauseChannel() <-chan bool {
	return t.pauseCh
}

func (t *TviewTerminal) RateChannel() <-chan int {
	return t.rateCh
}

func (t *TviewTerminal) QuitChannel() <-chan struct{} {
	return t.quitCh
}

// Helper functions

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second

	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

func createModal(content tview.Primitive, width, height int) tview.Primitive {
	return tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(content, height, 1, true).
			AddItem(nil, 0, 1, false), width, 1, true).
		AddItem(nil, 0, 1, false)
}

func splitLines(text string) []string {
	lines := []string{}
	current := ""
	for _, r := range text {
		if r == '\n' {
			lines = append(lines, current)
			current = ""
		} else {
			current += string(r)
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func joinLines(lines []string) string {
	result := ""
	for i, line := range lines {
		if i > 0 {
			result += "\n"
		}
		result += line
	}
	return result
}