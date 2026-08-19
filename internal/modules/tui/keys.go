package tui

import (
	"sync"
	"unicode/utf8"
)

// Key is one decoded input event.
type Key struct {
	Kind  KeyKind
	Rune  rune
	Alt   bool
	Shift bool
	// Unambiguous marks a key that arrived as a CSI-u sequence, which only
	// happens under the kitty keyboard protocol. It is what separates a real
	// ctrl+e from a terminal macro sending the same byte — see kitty.go.
	Unambiguous bool
}

type KeyKind int

const (
	KeyRune KeyKind = iota
	KeyEnter
	KeyBackspace
	KeyDelete
	KeyLeft
	KeyRight
	KeyUp
	KeyDown
	KeyHome
	KeyEnd
	KeyPageUp
	KeyPageDown
	KeyTab
	KeyBacktab // shift+tab
	KeyEsc
	KeyCtrlC
	KeyCtrlD
	KeyCtrlA
	KeyCtrlE
	KeyCtrlK
	KeyCtrlU
	KeyCtrlW
	KeyCtrlZ
	KeyCtrlY
	KeyCtrlG
	KeyCtrlL
	KeyCtrlP
	KeyPaste
)

const escTimeoutMs = 30

// KeyDecoder turns a byte stream from a raw terminal into Keys. It owns a
// small goroutine doing blocking reads; callers consume a channel, so an
// Esc that starts an escape sequence can be told apart from a plain Esc.
type KeyDecoder struct {
	Ch        chan Key
	pasteText string
	quit      chan struct{}
	closeOnce sync.Once
	// bytes carries chunks from the ONE reader goroutine.
	//
	// A single reader is the whole point. Reading was previously done by
	// spawning a goroutine per escape-timeout and abandoning it when the
	// timer won — but an abandoned read is still parked on stdin, so it went
	// on to swallow the user's NEXT keystroke and deliver it to nobody. The
	// symptom was precise and baffling: press Esc to close a menu, and the
	// first character you typed afterwards vanished. With one reader feeding
	// a channel, a chunk that arrives after a timeout simply waits here for
	// the next read instead of being lost.
	bytes chan []byte
}

func DecodeKeys() *KeyDecoder {
	// Roomy enough that ordinary typing never reaches the blocking path; the
	// blocking path is the safety net, not the normal case.
	d := &KeyDecoder{
		Ch:    make(chan Key, 256),
		quit:  make(chan struct{}),
		bytes: make(chan []byte, 8),
	}
	go d.readLoop()
	go d.loop()
	return d
}

// readLoop is the only thing that reads stdin.
func (d *KeyDecoder) readLoop() {
	buf := make([]byte, 1024)
	for {
		n, err := readStdin(buf)
		if err != nil || n == 0 {
			close(d.bytes)
			return
		}
		// Copied: the chunk crosses a channel and buf is reused immediately.
		chunk := make([]byte, n)
		copy(chunk, buf[:n])
		select {
		case d.bytes <- chunk:
		case <-d.quit:
			return
		}
	}
}

func (d *KeyDecoder) loop() {
	var pending []byte
	for {
		chunk, esc, ok := d.readMore(pending)
		pending = nil
		if !ok {
			close(d.Ch)
			return
		}
		if esc {
			d.emit(Key{Kind: KeyEsc})
		}
		if len(chunk) == 0 {
			continue
		}
		if _, drop := decodePasteStart(chunk); drop > 0 {
			d.drainPaste(chunk[drop:])
			continue
		}
		rest := d.parse(chunk)
		if rest != nil {
			// Dangling ESC at the buffer tail: wait for more bytes.
			more, loneEsc, ok := d.readMore(rest)
			if !ok {
				close(d.Ch)
				return
			}
			if loneEsc {
				d.emit(Key{Kind: KeyEsc})
				continue
			}
			// `more` ALREADY carries the dangling prefix — readMore appends
			// the new bytes to what it was given. Appending it to `rest`
			// again made every escape sequence that arrived in two reads
			// decode as "\x1b\x1b[B": a spurious lone Esc, and then the real
			// key. Over ssh, where a sequence splitting across reads is
			// normal, that Esc closed whatever menu the arrow was meant to
			// move through.
			d.parse(more)
		}
	}
}

// readMore blocks on stdin. A bare ESC in `pending` becomes a lone-Esc
// event when no following bytes arrive within escTimeoutMs.
func (d *KeyDecoder) readMore(pending []byte) (chunk []byte, loneEsc, ok bool) {
	if len(pending) > 0 && len(pending) <= 8 {
		select {
		case b, alive := <-d.bytes:
			if !alive {
				return nil, false, false
			}
			return append(pending, b...), false, true
		case <-afterMs(escTimeoutMs):
			// ESC with no followers inside the window is a lone Esc, however
			// long the dangling prefix is. Nothing is dropped by giving up
			// here: the reader owns stdin and whatever arrives next is
			// waiting on d.bytes for the following read.
			return nil, true, true
		}
	}
	b, alive := <-d.bytes
	if !alive {
		return nil, false, false
	}
	return b, false, true
}

// parse consumes complete keys from b, returns the unconsumed tail if it
// is a possible prefix of an escape sequence (a lone ESC).
func (d *KeyDecoder) parse(b []byte) []byte {
	for len(b) > 0 {
		k, n := decodeKeyFull(b)
		if n == 0 {
			return b
		}
		d.emit(k)
		b = b[n:]
	}
	return nil
}

// emit hands a key to the app, BLOCKING until it is taken.
//
// It used to drop the key when the buffer was full, which silently discarded
// typing: every keystroke costs a repaint, so a fast typist outruns the
// consumer and the tail of a sentence simply vanished. Input is the one thing
// a terminal app must never lose — blocking applies backpressure to the pty
// instead, which is what buffers are for.
//
// `quit` keeps that from becoming a hang at shutdown, when nobody is reading.
func (d *KeyDecoder) emit(k Key) {
	select {
	case d.Ch <- k:
	case <-d.quit:
	}
}

// Close releases a decoder blocked on a full channel.
func (d *KeyDecoder) Close() {
	d.closeOnce.Do(func() { close(d.quit) })
}

// decodeKeyFull decodes one key from the head of b. n == 0 means b ends in
// a lone ESC that could prefix a sequence — caller should wait for more.
func decodeKeyFull(b []byte) (Key, int) {
	switch b[0] {
	case 0:
		return Key{Kind: KeyRune, Rune: ' '}, 1 // ctrl+space
	case 1:
		return Key{Kind: KeyCtrlA}, 1
	case 3:
		return Key{Kind: KeyCtrlC}, 1
	case 4:
		return Key{Kind: KeyCtrlD}, 1
	case 5:
		return Key{Kind: KeyCtrlE}, 1
	case 7:
		return Key{Kind: KeyCtrlG}, 1
	case 12:
		return Key{Kind: KeyCtrlL}, 1
	case 16:
		return Key{Kind: KeyCtrlP}, 1
	case 11:
		return Key{Kind: KeyCtrlK}, 1
	case 21:
		return Key{Kind: KeyCtrlU}, 1
	case 23:
		return Key{Kind: KeyCtrlW}, 1
	case 25:
		return Key{Kind: KeyCtrlY}, 1
	case 26:
		return Key{Kind: KeyCtrlZ}, 1
	case 9:
		return Key{Kind: KeyTab}, 1
	case '\r', '\n':
		return Key{Kind: KeyEnter}, 1
	case 127, 8:
		return Key{Kind: KeyBackspace}, 1
	case 0x1b:
		return decodeEscape(b)
	default:
		r, n := utf8.DecodeRune(b)
		if r == utf8.RuneError && n == 1 {
			return Key{Kind: KeyRune, Rune: rune(b[0])}, 1
		}
		return Key{Kind: KeyRune, Rune: r}, n
	}
}

func decodeEscape(b []byte) (Key, int) {
	if len(b) < 2 {
		return Key{}, 0
	}
	switch b[1] {
	case '[':
		return decodeCSI(b)
	case 'O':
		if len(b) < 3 {
			return Key{}, 0
		}
		switch b[2] {
		case 'H':
			return Key{Kind: KeyHome}, 3
		case 'F':
			return Key{Kind: KeyEnd}, 3
		}
		return Key{Kind: KeyEsc}, 1
	default:
		if len(b) >= 2 && b[1] >= 32 && b[1] != 127 {
			r, n := utf8.DecodeRune(b[1:])
			if r == '\r' || r == '\n' {
				return Key{Kind: KeyEnter, Alt: true}, 1 + n
			}
			return Key{Kind: KeyRune, Rune: r, Alt: true}, 1 + n
		}
		return Key{Kind: KeyEsc}, 1
	}
}

func decodeCSI(b []byte) (Key, int) {
	// CSI is ESC [ params letter. Scan for the final byte.
	i := 2
	for i < len(b) && !(b[i] >= 0x40 && b[i] <= 0x7e) {
		i++
	}
	if i >= len(b) {
		return Key{}, 0
	}
	final := b[i]
	params := string(b[2:i])
	n := i + 1
	shift, alt := false, false
	// modifier is the second param: 2=shift 3=alt 4=shift+alt …
	mods := ""
	for j := 0; j < len(params); j++ {
		if params[j] == ';' && j+1 < len(params) {
			mods = params[j+1:]
			break
		}
	}
	if len(mods) > 0 {
		m := int(mods[0] - '0')
		shift = m == 2 || m == 4 || m == 6 || m == 8
		alt = m == 3 || m == 4 || m == 7 || m == 8
	}
	key := func(k KeyKind) Key { return Key{Kind: k, Shift: shift, Alt: alt} }
	switch final {
	case 'A':
		return key(KeyUp), n
	case 'B':
		return key(KeyDown), n
	case 'C':
		return key(KeyRight), n
	case 'D':
		return key(KeyLeft), n
	case 'H':
		return key(KeyHome), n
	case 'F':
		return key(KeyEnd), n
	case 'Z':
		return Key{Kind: KeyBacktab}, n
	case 'u':
		// CSI-u: the kitty keyboard protocol's unambiguous encoding.
		if k, ok := decodeKittyKey(params); ok {
			return k, n
		}
		// Consumed but not reported. Emitting a bare Esc for an unrecognised
		// sequence would close whatever the user had open.
		return Key{}, n
	case '~':
		switch params {
		case "1", "7":
			return key(KeyHome), n
		case "4", "8":
			return key(KeyEnd), n
		case "3":
			return key(KeyDelete), n
		case "5":
			return key(KeyPageUp), n
		case "6":
			return key(KeyPageDown), n
		}
		return Key{Kind: KeyEsc}, n
	default:
		return Key{Kind: KeyEsc}, n
	}
}

func decodePasteStart(b []byte) (k *Key, n int) {
	if len(b) >= 6 && string(b[:6]) == "\x1b[200~" {
		return &Key{}, 6
	}
	return nil, 0
}

// drainPaste reads until the paste-end marker, stores the payload for
// PasteText, and emits one KeyPaste.
func (d *KeyDecoder) drainPaste(have []byte) {
	text := append([]byte{}, have...)
	for !pasteComplete(text) {
		b, alive := <-d.bytes
		if !alive {
			break
		}
		text = append(text, b...)
	}
	end := len(text)
	if i := indexPasteEnd(text); i >= 0 {
		end = i
	}
	d.pasteText = string(text[:end])
	d.emit(Key{Kind: KeyPaste})
}

func pasteComplete(b []byte) bool { return indexPasteEnd(b) >= 0 }

func indexPasteEnd(b []byte) int {
	marker := []byte("\x1b[201~")
	for i := 0; i+6 <= len(b); i++ {
		if string(b[i:i+6]) == string(marker) {
			return i
		}
	}
	return -1
}

// PasteText returns the payload of the most recent KeyPaste.
func (d *KeyDecoder) PasteText() string { return d.pasteText }
