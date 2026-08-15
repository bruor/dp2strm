package proxy

import (
	"io"
	"log"
	"os"
	"sync"
)

// LogRingBuffer captures the most recent log entries in memory for remote inspection.
type LogRingBuffer struct {
	mu    sync.RWMutex
	lines []string
	max   int
}

// GlobalLogBuffer holds up to 1000 recent log messages.
var GlobalLogBuffer = NewLogRingBuffer(1000)

func NewLogRingBuffer(max int) *LogRingBuffer {
	return &LogRingBuffer{
		lines: make([]string, 0, max),
		max:   max,
	}
}

func (b *LogRingBuffer) Write(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	str := string(p)
	if len(b.lines) >= b.max {
		b.lines = b.lines[1:]
	}
	b.lines = append(b.lines, str)
	return len(p), nil
}

// GetLogs returns a snapshot of all buffered log lines.
func (b *LogRingBuffer) GetLogs() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	res := make([]string, len(b.lines))
	copy(res, b.lines)
	return res
}

// InitGlobalLogger configures standard logger to write simultaneously to stdout and the memory ring buffer.
func InitGlobalLogger() {
	multi := io.MultiWriter(os.Stdout, GlobalLogBuffer)
	log.SetOutput(multi)
}
