package tools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Line selection for the read tool, loop's tools/utils/read-file-lines.ts.
//
// Two paths, one set of semantics: a file under largeFileBytes is read whole
// (cheap, and the total line count comes free); a larger one is streamed, so
// reading a multi-GB log costs a few KB of memory.
//
// Lines split on "\n", and the empty piece after a file's final newline is
// not a line: a 10-line file that ends with a newline has 10 lines, not 11.

const (
	largeFileBytes = 8 * 1024 * 1024
	sniffBytes     = 4096
)

// lineSelection is the lines to show and what is known about the rest.
type lineSelection struct {
	lines     []string
	startLine int // 1-indexed line number of lines[0]
	// truncatedBy is the budget that stopped the selection: "lines", "bytes" or "".
	truncatedBy string
	hasMore     bool
	// totalLines is -1 when a big file was not scanned to its end.
	totalLines int
	// trailingNewline: the selection ran to the file's final newline.
	trailingNewline bool
	// firstLineBytes is set (>0) when the first line alone is over the byte
	// budget, so nothing is shown. firstLineClipped means it was only
	// measured up to a cap and the real line is longer.
	firstLineBytes   int
	firstLineClipped bool
}

type selectOptions struct {
	offset int // 1-indexed first line; 0 means from the start
	limit  int // 0 means no limit
	size   int64
}

// lineBudget collects lines until a cap is hit. Both read paths share it so
// truncation behaves the same whether the file was buffered or streamed.
type lineBudget struct {
	kept           []string
	truncatedBy    string
	firstLineBytes int
	bytes          int
}

// push reports false when the line did not fit: the budget is spent.
func (b *lineBudget) push(line string) bool {
	if len(b.kept) >= DefaultMaxLines {
		b.truncatedBy = "lines"
		return false
	}
	cost := len(line)
	if len(b.kept) > 0 {
		cost++ // the joining newline
	}
	if b.bytes+cost > DefaultMaxBytes {
		b.truncatedBy = "bytes"
		if len(b.kept) == 0 {
			b.firstLineBytes = len(line)
		}
		return false
	}
	b.kept = append(b.kept, line)
	b.bytes += cost
	return true
}

func beyondEOF(offset, totalLines int) error {
	return fmt.Errorf("Offset %d is beyond end of file (%d lines total)", max(offset, 1), totalLines)
}

// looksBinary reports a NUL byte in the first few KB — the test git uses.
func looksBinary(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	buf := make([]byte, sniffBytes)
	n, err := f.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return bytes.IndexByte(buf[:n], 0) >= 0, nil
}

func selectLines(ctx context.Context, path string, opt selectOptions) (lineSelection, error) {
	if opt.size > largeFileBytes {
		return selectStreaming(ctx, path, opt)
	}
	return selectBuffered(path, opt)
}

func selectBuffered(path string, opt selectOptions) (lineSelection, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return lineSelection{}, err
	}
	all := strings.Split(string(raw), "\n")
	endsWithNewline := len(all) > 1 && all[len(all)-1] == ""
	if endsWithNewline {
		all = all[:len(all)-1]
	}
	start := max(opt.offset-1, 0)
	if start >= len(all) {
		return lineSelection{}, beyondEOF(opt.offset, len(all))
	}
	end := len(all)
	if opt.limit > 0 {
		end = min(start+opt.limit, len(all))
	}
	var budget lineBudget
	stoppedEarly := false
	for _, line := range all[start:end] {
		if !budget.push(line) {
			stoppedEarly = true
			break
		}
	}
	hasMore := stoppedEarly || end < len(all)
	return lineSelection{
		lines:           budget.kept,
		startLine:       start + 1,
		truncatedBy:     budget.truncatedBy,
		hasMore:         hasMore,
		totalLines:      len(all),
		trailingNewline: endsWithNewline && !hasMore,
		firstLineBytes:  budget.firstLineBytes,
	}, nil
}

// lineStream yields a file's lines without holding it in memory. A line
// longer than lineCap is yielded clipped: it can never fit the byte budget,
// and the cap is what keeps a one-line multi-GB file from being buffered.
type lineStream struct {
	reader  *bufio.Reader
	lineCap int
	// endsWithNewline is meaningful only once next has reported the end.
	endsWithNewline bool
}

// next returns the next line. ok is false at the end of the file.
func (s *lineStream) next() (line string, clipped, ok bool, err error) {
	var buf []byte
	sawAny := false
	for {
		chunk, readErr := s.reader.ReadSlice('\n')
		sawAny = sawAny || len(chunk) > 0
		terminated := readErr == nil
		if terminated {
			chunk = chunk[:len(chunk)-1]
		}
		if room := s.lineCap - len(buf); len(chunk) > room {
			chunk, clipped = chunk[:room], true
		}
		buf = append(buf, chunk...)
		switch {
		case terminated:
			s.endsWithNewline = true
			return string(buf), clipped, true, nil
		case errors.Is(readErr, bufio.ErrBufferFull):
			continue
		case errors.Is(readErr, io.EOF):
			if !sawAny {
				return "", false, false, nil
			}
			s.endsWithNewline = false
			return string(buf), clipped, true, nil
		default:
			return "", false, false, readErr
		}
	}
}

func selectStreaming(ctx context.Context, path string, opt selectOptions) (lineSelection, error) {
	f, err := os.Open(path)
	if err != nil {
		return lineSelection{}, err
	}
	defer f.Close()
	// One byte over the budget, so a clipped line always fails the test it is put to.
	stream := &lineStream{reader: bufio.NewReaderSize(f, 64*1024), lineCap: DefaultMaxBytes + 1}
	start := max(opt.offset-1, 0)
	var budget lineBudget
	seen, hasMore, firstLineClipped := 0, false, false
	for !hasMore {
		if ctx.Err() != nil {
			return lineSelection{}, errAborted
		}
		line, clipped, ok, err := stream.next()
		if err != nil {
			return lineSelection{}, err
		}
		if !ok {
			break
		}
		seen++
		if seen <= start {
			continue
		}
		switch {
		case opt.limit > 0 && len(budget.kept) >= opt.limit:
			hasMore = true // this line is past the caller's limit
		case !budget.push(line):
			firstLineClipped = len(budget.kept) == 0 && clipped
			hasMore = true
		}
	}
	reachedEOF := !hasMore
	if reachedEOF && seen <= start {
		return lineSelection{}, beyondEOF(opt.offset, seen)
	}
	total := -1
	if reachedEOF {
		total = seen
	}
	return lineSelection{
		lines:            budget.kept,
		startLine:        start + 1,
		truncatedBy:      budget.truncatedBy,
		hasMore:          hasMore,
		totalLines:       total,
		trailingNewline:  reachedEOF && stream.endsWithNewline,
		firstLineBytes:   budget.firstLineBytes,
		firstLineClipped: firstLineClipped,
	}, nil
}
