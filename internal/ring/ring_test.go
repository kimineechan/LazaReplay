package ring

import (
	"bytes"
	"testing"
)

const second = 10_000_000

// fill adds pictures at 10 per second with a keyframe every second, each
// picture holding its own number so the contents can be checked.
func fill(r *Ring, from, to int) {
	for i := from; i < to; i++ {
		r.AddVideo([]byte{byte(i), byte(i >> 8), 0xAA}, int64(i)*second/10, i%10 == 0)
		r.AddAudio([]byte{byte(i)}, int64(i)*second/10)
	}
}

func check(t *testing.T, clip Clip, first, last int) {
	t.Helper()
	n := first
	for _, g := range clip.Groups {
		if len(g.Frames) != len(g.Sound) {
			t.Fatalf("group has %d pictures but %d sound blocks", len(g.Frames), len(g.Sound))
		}
		for i, f := range g.Frames {
			data := g.Video[f.Offset : f.Offset+f.Size]
			if !bytes.Equal(data, []byte{byte(n), byte(n >> 8), 0xAA}) {
				t.Fatalf("picture %d holds %v", n, data)
			}
			if f.Time != int64(n)*second/10 {
				t.Fatalf("picture %d has time %d", n, f.Time)
			}
			if s := g.Sound[i]; g.Audio[s.Offset] != byte(n) {
				t.Fatalf("sound %d holds %d", n, g.Audio[s.Offset])
			}
			n++
		}
	}
	if n-1 != last {
		t.Fatalf("clip runs from %d to %d, want %d to %d", first, n-1, first, last)
	}
}

func TestKeepsTheWindow(t *testing.T) {
	r := New(5*second, second/10, 0)
	r.SetParams([]byte{1}, []byte{2})
	fill(r, 0, 200)
	// Newest picture is 199 at 19.9 s. The window's edge is 14.9 s, inside
	// the group that starts at 14.0 s, so the clip starts there.
	clip := r.Snapshot()
	check(t, clip, 140, 199)
	if clip.End != 200*second/10 {
		t.Fatalf("clip ends at %d", clip.End)
	}
	length, size := r.Stats()
	if length != 5*second {
		t.Fatalf("length %d", length)
	}
	if size != 60*4 {
		t.Fatalf("size %d, want %d", size, 60*4)
	}
}

func TestShortClipBeforeTheWindowFills(t *testing.T) {
	r := New(5*second, second/10, 0)
	r.SetParams([]byte{1}, []byte{2})
	fill(r, 0, 25)
	check(t, r.Snapshot(), 0, 24)
	if length, _ := r.Stats(); length != 25*second/10 {
		t.Fatalf("length %d", length)
	}
}

func TestDropsPicturesBeforeTheFirstKeyframe(t *testing.T) {
	r := New(5*second, second/10, 0)
	r.SetParams([]byte{1}, []byte{2})
	fill(r, 3, 25)
	check(t, r.Snapshot(), 10, 24)
}

func TestNothingWithoutParameters(t *testing.T) {
	r := New(5*second, second/10, 0)
	fill(r, 0, 25)
	if clip := r.Snapshot(); len(clip.Groups) != 0 {
		t.Fatalf("%d groups stored without parameter sets", len(clip.Groups))
	}
	if _, size := r.Stats(); size != 0 {
		t.Fatalf("size %d", size)
	}
}

func TestByteLimitShortensTheLoop(t *testing.T) {
	r := New(60*second, second/10, 100)
	r.SetParams([]byte{1}, []byte{2})
	fill(r, 0, 200)
	if _, size := r.Stats(); size > 100+40 {
		t.Fatalf("holds %d bytes with a limit of 100", size)
	}
	clip := r.Snapshot()
	if len(clip.Groups) == 0 || clip.Groups[len(clip.Groups)-1].Frames[0].Time < 190*second/10 {
		t.Fatal("the newest group was lost")
	}
}

func TestNewParametersStartAFreshClip(t *testing.T) {
	r := New(60*second, second/10, 0)
	r.SetParams([]byte{1}, []byte{2})
	fill(r, 0, 50)
	r.SetParams([]byte{1}, []byte{2}) // unchanged: no effect
	fill(r, 50, 60)
	r.SetParams([]byte{9}, []byte{2})
	fill(r, 60, 85)
	clip := r.Snapshot()
	check(t, clip, 60, 84)
	if clip.Groups[0].Params.SPS[0] != 9 {
		t.Fatal("clip carries the old parameter sets")
	}
}

func TestSnapshotIsNotDisturbedByLaterPictures(t *testing.T) {
	r := New(2*second, second/10, 0)
	r.SetParams([]byte{1}, []byte{2})
	fill(r, 0, 35)
	clip := r.Snapshot()
	fill(r, 35, 400)
	check(t, clip, 10, 34)
}

func TestReset(t *testing.T) {
	r := New(2*second, second/10, 0)
	r.SetParams([]byte{1}, []byte{2})
	fill(r, 0, 35)
	r.Reset()
	if clip := r.Snapshot(); len(clip.Groups) != 0 {
		t.Fatal("footage left after reset")
	}
	fill(r, 40, 55)
	check(t, r.Snapshot(), 40, 54)
}
