package tools

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"unicode/utf8"
)

// outputAccumulator collects a command's output for the bash tool (loop's
// tools/utils/output-accumulator.ts). It keeps a bounded tail in memory — the
// end of the output is the part worth reading — and counts everything, so
// the result can say how much was cut. Once the output is over the limits,
// all of it also goes to a temp file the model can be pointed at.
type outputAccumulator struct {
	mu sync.Mutex

	tail             []byte
	tailStartsAtLine bool
	totalBytes       int
	totalLines       int
	currentLineBytes int

	// held is the output so far, kept until a temp file is needed.
	held     []byte
	tempFile *os.File
	tempPath string
	// tempFailed: the file could not be written (an unwritable TMPDIR, a
	// full disk). The truncation notice is then printed without a path.
	tempFailed bool
}

// rollingBytes is how much tail is kept: twice the cap, so a cut on a line
// boundary still leaves a full cap's worth.
const rollingBytes = DefaultMaxBytes * 2

func newOutputAccumulator() *outputAccumulator {
	return &outputAccumulator{tailStartsAtLine: true, totalLines: 1}
}

// Write implements io.Writer.
func (o *outputAccumulator) Write(data []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.count(data)
	o.tail = append(o.tail, data...)
	if len(o.tail) > rollingBytes*2 {
		o.trimTail()
	}
	o.keep(data)
	return len(data), nil
}

func (o *outputAccumulator) count(data []byte) {
	o.totalBytes += len(data)
	newlines := bytes.Count(data, []byte("\n"))
	if newlines == 0 {
		o.currentLineBytes += len(data)
		return
	}
	o.totalLines += newlines
	o.currentLineBytes = len(data) - bytes.LastIndexByte(data, '\n') - 1
}

func (o *outputAccumulator) trimTail() {
	start := len(o.tail) - rollingBytes
	for start < len(o.tail) && !utf8.RuneStart(o.tail[start]) {
		start++
	}
	o.tailStartsAtLine = o.tail[start-1] == '\n'
	o.tail = append([]byte(nil), o.tail[start:]...)
}

func (o *outputAccumulator) overLimits() bool {
	return o.totalBytes > DefaultMaxBytes || o.totalLines > DefaultMaxLines
}

// keep holds the raw output in memory until it is over the limits, then
// moves it to the temp file and appends there from then on.
func (o *outputAccumulator) keep(data []byte) {
	if o.tempFailed {
		return
	}
	if o.tempFile == nil && !o.overLimits() {
		o.held = append(o.held, data...)
		return
	}
	if o.tempFile == nil {
		o.held = append(o.held, data...)
		o.openTempFile()
		return
	}
	if _, err := o.tempFile.Write(data); err != nil {
		o.failTempFile()
	}
}

func (o *outputAccumulator) openTempFile() {
	file, err := os.CreateTemp("", "loop-bash-*.log")
	if err != nil {
		o.failTempFile()
		return
	}
	o.tempFile, o.tempPath = file, file.Name()
	if _, err := file.Write(o.held); err != nil {
		o.failTempFile()
	}
	o.held = nil
}

func (o *outputAccumulator) failTempFile() {
	if o.tempFile != nil {
		_ = o.tempFile.Close()
	}
	o.tempFile, o.tempPath, o.held, o.tempFailed = nil, "", nil, true
}

// outputSnapshot is the kept tail and what was cut to get it.
type outputSnapshot struct {
	Truncation
	// fullOutputPath is the temp file holding everything, when there is one.
	fullOutputPath string
	// lastLineBytes is the size of the output's final line.
	lastLineBytes int
}

func (o *outputAccumulator) snapshot() outputSnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	text := string(o.tail)
	if !o.tailStartsAtLine {
		// The tail was cut mid-line: drop the partial first line.
		if _, rest, found := strings.Cut(text, "\n"); found {
			text = rest
		}
	}
	cut := TruncateTail(text, DefaultMaxLines, DefaultMaxBytes)
	cut.Truncated = o.overLimits()
	if cut.Truncated && cut.TruncatedBy == "" {
		cut.TruncatedBy = "lines"
		if o.totalBytes > DefaultMaxBytes {
			cut.TruncatedBy = "bytes"
		}
	}
	cut.TotalLines, cut.TotalBytes = o.totalLines, o.totalBytes
	return outputSnapshot{Truncation: cut, fullOutputPath: o.tempPath, lastLineBytes: o.currentLineBytes}
}

// close finishes the temp file. The file itself stays: the result names it.
func (o *outputAccumulator) close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.tempFile != nil {
		_ = o.tempFile.Close()
		o.tempFile = nil
	}
}
