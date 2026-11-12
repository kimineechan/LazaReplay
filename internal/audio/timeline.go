// Package audio captures what the computer plays and lines it up with the
// clock the video uses.
package audio

// Second is one second in the 100-nanosecond units used for every time.
const Second = 10_000_000

const (
	jump  = Second / 10      // a disagreement this large is a real break, not jitter
	slack = Second * 4 / 100 // how far behind the clock silence is filled to
	reset = 2 * Second       // a break this long starts the timeline afresh
)

// Timeline turns the packets of sound Windows delivers into one unbroken
// run of samples whose position in time is known.
//
// Windows delivers nothing at all while nothing is playing, so gaps are
// filled with silence; otherwise the sound track of a clip would be shorter
// than its pictures and drift out of step. The sound card's clock also runs
// very slightly faster or slower than the computer's, so the timeline
// follows the times Windows reports a little at a time instead of trusting
// a count of samples for ever.
type Timeline struct {
	rate     int
	channels int
	samples  []int16 // waiting to be taken, channels interleaved
	anchor   int64   // time of the first sample ever taken since the anchor was set
	taken    int64   // frames taken since then
	started  bool
}

// NewTimeline makes a timeline for sound of the given rate and channels.
func NewTimeline(rate, channels int) *Timeline {
	return &Timeline{rate: rate, channels: channels}
}

// head is the time of the first waiting sample.
func (t *Timeline) head() int64 { return t.anchor + t.taken*Second/int64(t.rate) }

// next is the time the next sample to arrive is expected to have.
func (t *Timeline) next() int64 {
	return t.head() + int64(len(t.samples)/t.channels)*Second/int64(t.rate)
}

func (t *Timeline) silence(length int64) {
	frames := int(length * int64(t.rate) / Second)
	t.samples = append(t.samples, make([]int16, frames*t.channels)...)
}

// Add appends a packet of samples whose first sample was captured at the
// given time. A time of zero means Windows could not say.
func (t *Timeline) Add(samples []int16, at int64) {
	if !t.started {
		if at == 0 {
			return
		}
		t.started, t.anchor, t.taken = true, at, 0
	}
	if at != 0 {
		switch off := at - t.next(); {
		case off > reset || off < -reset:
			// The computer slept, or the clock stepped. Start again from here.
			t.samples, t.anchor, t.taken = t.samples[:0], at, 0
		case off > jump:
			t.silence(off)
		case off < -jump:
			// The packet overlaps what is already here; drop the overlap.
			drop := int(-off*int64(t.rate)/Second) * t.channels
			if drop >= len(samples) {
				return
			}
			samples = samples[drop:]
		default:
			t.anchor += off / 16 // follow the reported time gently
		}
	}
	t.samples = append(t.samples, samples...)
}

// Idle is called when no packet has arrived. If the timeline has fallen
// behind the clock, nothing is playing, and the gap is filled with silence.
func (t *Timeline) Idle(now int64) {
	if !t.started {
		t.started, t.anchor, t.taken = true, now-slack, 0
		return
	}
	if behind := now - t.next(); behind > jump {
		if behind > reset {
			t.samples, t.anchor, t.taken = t.samples[:0], now-slack, 0
			return
		}
		t.silence(behind - slack)
	}
}

// Take removes the given number of frames (a frame is one sample for each
// channel) and returns them with the time of the first. ok is false when
// fewer are waiting. The slice is valid until the next call.
func (t *Timeline) Take(frames int) (samples []int16, at int64, ok bool) {
	n := frames * t.channels
	if len(t.samples) < n {
		return nil, 0, false
	}
	at = t.head()
	samples = t.samples[:n:n]
	t.samples = t.samples[n:]
	t.taken += int64(frames)
	if len(t.samples) == 0 {
		t.samples = nil // let the consumed memory go
	}
	return samples, at, true
}
