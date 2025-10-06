package ui

// Terminal is the common interface for both terminal UIs
type Terminal interface {
	Run()
	Stop()
	UpdateStats(stats Stats)
	ShowMessage(msg string)
	PauseChannel() <-chan bool
	RateChannel() <-chan int
	QuitChannel() <-chan struct{}
}

// NewTerminal creates the appropriate terminal based on the flag
func NewTerminal(useSimple bool) Terminal {
	if useSimple {
		return NewSimpleTerminal()
	}
	return NewTviewTerminal()
}