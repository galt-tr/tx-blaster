package logger

import (
	"io"
	"log"
	"os"
	"syscall"
)

var (
	suppressLogs = false
	originalStdout int
	originalStderr int
	originalLogOutput io.Writer
	devNull *os.File
)

// SuppressAll suppresses all console output including log, fmt.Print, etc.
func SuppressAll() {
	if suppressLogs {
		return
	}

	suppressLogs = true

	// Open /dev/null
	var err error
	devNull, err = os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return // Can't suppress, but continue anyway
	}

	// Save original file descriptors
	originalStdout, _ = syscall.Dup(1)
	originalStderr, _ = syscall.Dup(2)

	// Redirect stdout and stderr to /dev/null at the file descriptor level
	syscall.Dup2(int(devNull.Fd()), 1)
	syscall.Dup2(int(devNull.Fd()), 2)

	// Also redirect log package output
	originalLogOutput = log.Writer()
	log.SetOutput(io.Discard)
}

// SuppressLogsOnly suppresses only log output, keeping stdout for UI
func SuppressLogsOnly() {
	if suppressLogs {
		return
	}

	suppressLogs = true

	// Only redirect stderr (where logs typically go) to /dev/null
	var err error
	devNull, err = os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return // Can't suppress, but continue anyway
	}

	// Save original stderr only
	originalStderr, _ = syscall.Dup(2)

	// Redirect stderr to /dev/null at the file descriptor level
	syscall.Dup2(int(devNull.Fd()), 2)

	// Also redirect log package output
	originalLogOutput = log.Writer()
	log.SetOutput(io.Discard)
}

// RestoreAll restores all console output
func RestoreAll() {
	if !suppressLogs {
		return
	}

	suppressLogs = false

	// Restore stdout and stderr file descriptors
	if originalStdout != 0 {
		syscall.Dup2(originalStdout, 1)
		syscall.Close(originalStdout)
	}
	if originalStderr != 0 {
		syscall.Dup2(originalStderr, 2)
		syscall.Close(originalStderr)
	}

	// Close /dev/null
	if devNull != nil {
		devNull.Close()
	}

	// Restore log output
	if originalLogOutput != nil {
		log.SetOutput(originalLogOutput)
	}
}

// IsSuppressed returns whether logs are currently suppressed
func IsSuppressed() bool {
	return suppressLogs
}