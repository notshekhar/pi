package status

import (
	"sync"
	"testing"
	"time"
)

// collect subscribes and returns a snapshot func, so a test reads what was
// published rather than what the bus thinks it is.
func collect(b *Bus) func() []Event {
	var mu sync.Mutex
	var got []Event
	b.On(func(e Event) {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
	})
	return func() []Event {
		mu.Lock()
		defer mu.Unlock()
		return append([]Event(nil), got...)
	}
}

func TestWorkingAndBlockedArePublishedAtOnce(t *testing.T) {
	b := New(0)
	events := collect(b)

	b.SetWorking()
	close1 := b.ModalOpened("bash approval")
	close1()
	b.SetIdle()

	want := []Event{
		{State: Working},
		{State: Blocked, Label: "bash approval"},
		{State: Working},
		{State: Idle},
	}
	got := events()
	if len(got) != len(want) {
		t.Fatalf("published %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// A calmer state settles first. The seams flicker naturally — an ask flow
// closes one prompt and opens the next, a queued turn starts as the last one
// ends — and every blip would otherwise publish "idle" and immediately take
// it back.
func TestDownwardTransitionsSettle(t *testing.T) {
	b := New(50 * time.Millisecond)
	events := collect(b)

	b.SetWorking()
	done := b.ModalOpened("question")
	// Closing one prompt and opening another inside the settle window must
	// never publish the gap between them.
	done()
	b.ModalOpened("question")

	time.Sleep(120 * time.Millisecond)
	// working (the turn started), then blocked — and nothing after it. The
	// gap between the two prompts must never have been published.
	got := events()
	if len(got) != 2 || got[0].State != Working || got[1].State != Blocked {
		t.Fatalf("a flicker between two prompts was published: %v", got)
	}
	if cur := b.Current(); cur.State != Blocked {
		t.Fatalf("current = %v, want blocked", cur)
	}
}

// And a settled transition does eventually arrive.
func TestSettledTransitionIsPublished(t *testing.T) {
	b := New(20 * time.Millisecond)
	events := collect(b)

	b.SetWorking()
	b.SetIdle()
	if last := events()[len(events())-1]; last.State != Working {
		t.Fatalf("idle was published immediately: %v", events())
	}
	time.Sleep(80 * time.Millisecond)
	if last := events()[len(events())-1]; last.State != Idle {
		t.Fatalf("idle never arrived: %v", events())
	}
}

// Nested prompts are one stretch of blocked, and the closer is idempotent
// because the natural way to call it is a defer on a path that may also close
// explicitly.
func TestNestedPromptsAreOneStretch(t *testing.T) {
	b := New(0)
	events := collect(b)

	outer := b.ModalOpened("question")
	inner := b.ModalOpened("bash approval")
	inner()
	inner()
	if last := events()[len(events())-1]; last.State != Blocked {
		t.Fatalf("closing the inner prompt ended the stretch: %v", events())
	}
	outer()
	if last := events()[len(events())-1]; last.State != Idle {
		t.Fatalf("closing the outer prompt did not end it: %v", events())
	}
}
