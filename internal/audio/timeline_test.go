package audio

import "testing"

const rate = 48000

// packet makes n frames of stereo sound, every sample holding value.
func packet(n int, value int16) []int16 {
	out := make([]int16, n*2)
	for i := range out {
		out[i] = value
	}
	return out
}

func at(frames int) int64 { return int64(frames) * Second / rate }

// drain takes everything in blocks of 1024 frames and returns the samples
// and the time of each block.
func drain(t *Timeline) (samples []int16, times []int64) {
	for {
		block, when, ok := t.Take(1024)
		if !ok {
			return samples, times
		}
		samples = append(samples, block...)
		times = append(times, when)
	}
}

func TestSteadySound(t *testing.T) {
	line := NewTimeline(rate, 2)
	start := int64(5 * Second)
	for i := 0; i < 100; i++ { // one second in 10 ms packets
		line.Add(packet(480, int16(i+1)), start+at(i*480))
	}
	samples, times := drain(line)
	if len(times) != 46 { // 48000 / 1024, rounded down
		t.Fatalf("%d blocks", len(times))
	}
	for i, when := range times {
		if want := start + at(i*1024); when != want {
			t.Fatalf("block %d at %d, want %d", i, when, want)
		}
	}
	for i := 0; i < len(samples); i += 2 {
		if want := int16(i/2/480 + 1); samples[i] != want {
			t.Fatalf("frame %d holds %d, want %d", i/2, samples[i], want)
		}
	}
}

func TestSilenceFillsAGap(t *testing.T) {
	line := NewTimeline(rate, 2)
	line.Add(packet(480, 7), Second)
	line.Add(packet(480, 9), Second+at(480)+Second/2) // half a second of nothing between
	line.Add(packet(1024, 0), Second+at(960)+Second/2)
	samples, times := drain(line)
	silent := 0
	for i := 0; i < len(samples); i += 2 {
		if samples[i] == 0 {
			silent++
		}
	}
	if silent < 24000 || silent > 24000+1024 {
		t.Fatalf("%d silent frames for a half-second gap", silent)
	}
	if times[1]-times[0] != at(1024) {
		t.Fatalf("blocks are %d apart", times[1]-times[0])
	}
	// The sound after the gap sits half a second later than the sound before.
	first, second := -1, -1
	for i := 0; i < len(samples); i += 2 {
		if samples[i] == 7 && first < 0 {
			first = i / 2
		}
		if samples[i] == 9 && second < 0 {
			second = i / 2
		}
	}
	if second-first != 480+24000 {
		t.Fatalf("second packet lies %d frames after the first, want %d", second-first, 480+24000)
	}
}

func TestIdleKeepsPaceWithTheClock(t *testing.T) {
	line := NewTimeline(rate, 2)
	now := int64(10 * Second)
	line.Idle(now)
	for i := 1; i <= 300; i++ { // three seconds with nothing playing
		line.Idle(now + int64(i)*Second/100)
	}
	samples, times := drain(line)
	frames := len(samples) / 2
	if frames < 2*rate+rate/2 || frames > 3*rate {
		t.Fatalf("%d frames of silence for three idle seconds", frames)
	}
	if times[0] < now-Second/10 || times[0] > now {
		t.Fatalf("silence starts at %d, the clock was at %d", times[0], now)
	}
	// Sound that starts now lands where the clock says, to within the slack.
	line.Add(packet(480, 5), now+3*Second)
	line.Add(packet(2048, 5), now+3*Second+at(480))
	_, times = drain(line)
	last := times[len(times)-1]
	if d := last - (now + 3*Second); d < -Second/10 || d > Second/10 {
		t.Fatalf("after silence the timeline is %d away from the clock", d)
	}
}

func TestFollowsASlowClock(t *testing.T) {
	// The sound card delivers 480 frames every 10.001 ms: its clock runs a
	// hundred parts in a million slow. Over a minute that is 6 ms, which a
	// plain count of samples would get wrong. No sample may be dropped or
	// invented while the timeline follows.
	line := NewTimeline(rate, 2)
	step := int64(100_010)
	var total int
	var lastTime int64
	for i := 0; i < 6000; i++ {
		line.Add(packet(480, 3), Second+int64(i)*step)
		samples, times := drain(line)
		total += len(samples) / 2
		for _, s := range samples {
			if s != 3 {
				t.Fatal("silence was inserted into steady sound")
			}
		}
		if len(times) > 0 {
			lastTime = times[len(times)-1]
		}
	}
	if total < 6000*480-1024 {
		t.Fatalf("%d frames came out of %d", total, 6000*480)
	}
	// The last block taken began at frame total-1024 of the stream, which
	// by the card's own clock was captured at:
	want := Second + int64(total-1024)*step/480
	if d := lastTime - want; d < -20_000 || d > 20_000 {
		t.Fatalf("timeline is %d units from the reported times after a minute", d)
	}
}

func TestOverlapIsDropped(t *testing.T) {
	line := NewTimeline(rate, 2)
	line.Add(packet(9600, 1), Second)
	line.Add(packet(9600, 2), Second) // the same fifth of a second again
	line.Add(packet(1024, 3), Second+at(9600))
	samples, _ := drain(line)
	twos := 0
	for i := 0; i < len(samples); i += 2 {
		if samples[i] == 2 {
			twos++
		}
	}
	if twos != 0 {
		t.Fatalf("%d frames of a repeated packet were kept", twos)
	}
}

func TestLongBreakStartsAfresh(t *testing.T) {
	line := NewTimeline(rate, 2)
	line.Add(packet(2048, 1), Second)
	drain(line)
	line.Add(packet(2048, 2), 100*Second) // the computer slept
	samples, times := drain(line)
	if len(samples) != 2048*2 || samples[0] != 2 {
		t.Fatalf("%d samples after the break, first %d", len(samples), samples[0])
	}
	if times[0] != 100*Second {
		t.Fatalf("timeline resumes at %d", times[0])
	}
}

func TestUnknownTimes(t *testing.T) {
	line := NewTimeline(rate, 2)
	line.Add(packet(480, 1), 0) // nothing to anchor to yet: dropped
	if _, _, ok := line.Take(1); ok {
		t.Fatal("a packet with no time was kept before any anchor")
	}
	line.Add(packet(1024, 2), Second)
	line.Add(packet(1024, 3), 0) // taken as following on
	_, times := drain(line)
	if len(times) != 2 || times[1]-times[0] != at(1024) {
		t.Fatalf("times %v", times)
	}
}

func TestCutLinesUpASecondRunOfSound(t *testing.T) {
	const frame = Second / 1000 // one frame at 1000 a second
	line := NewTimeline(1000, 1)
	if got := line.Cut(5*Second, 3); len(got) != 3 || got[0] != 0 || got[2] != 0 {
		t.Fatalf("before any sound has arrived the span should be silence, got %v", got)
	}
	line.Add([]int16{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, Second)
	if !line.Covers(Second+10*frame) || line.Covers(Second+11*frame) {
		t.Fatal("ten frames from one second should cover exactly up to ten frames later")
	}
	// A span that starts three frames in: the first three are thrown away.
	if got := line.Cut(Second+3*frame, 4); got[0] != 4 || got[3] != 7 {
		t.Fatalf("got %v, want 4 5 6 7", got)
	}
	// The next span carries on, and is silence where the sound runs out.
	if got := line.Cut(Second+7*frame, 4); got[0] != 8 || got[2] != 10 || got[3] != 0 {
		t.Fatalf("got %v, want 8 9 10 0", got)
	}
	// A span that starts before the sound does is silence up to it.
	late := NewTimeline(1000, 1)
	late.Add([]int16{1, 2, 3}, Second)
	if got := late.Cut(Second-2*frame, 4); got[0] != 0 || got[1] != 0 || got[2] != 1 || got[3] != 2 {
		t.Fatalf("got %v, want 0 0 1 2", got)
	}
	if got := late.Cut(Second+2*frame, 2); got[0] != 3 || got[1] != 0 {
		t.Fatalf("got %v, want 3 0", got)
	}
	// Sound that is wholly too old is dropped and gives silence.
	old := NewTimeline(1000, 2)
	old.Add([]int16{1, 1, 2, 2}, Second)
	if got := old.Cut(3*Second, 2); len(got) != 4 || got[0] != 0 || got[3] != 0 {
		t.Fatalf("got %v, want silence", got)
	}
}

func TestReadySaysWhenABlockIsWaiting(t *testing.T) {
	line := NewTimeline(1000, 2)
	line.Add([]int16{1, 1, 2, 2, 3, 3}, Second)
	if at, ok := line.Ready(3); !ok || at != Second {
		t.Fatalf("three frames are waiting from one second, got %d %v", at, ok)
	}
	if _, ok := line.Ready(4); ok {
		t.Fatal("four frames are not waiting")
	}
}

func TestMixAddsAndHoldsAtTheLoudest(t *testing.T) {
	into := []int16{100, -100, 30000, -30000, 5}
	Mix(into, []int16{50, -50, 10000, -10000})
	want := []int16{150, -150, 32767, -32768, 5}
	for i := range want {
		if into[i] != want[i] {
			t.Fatalf("got %v, want %v", into, want)
		}
	}
}
