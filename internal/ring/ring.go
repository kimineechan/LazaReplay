// Package ring is the loop: it holds the most recent stretch of compressed
// video and sound in memory and forgets everything older.
//
// Video can only start playing from a keyframe, so the loop is kept as a
// list of groups, each one keyframe and the pictures that follow it. Old
// groups are dropped whole. A group is never changed once it is closed,
// which lets a save copy the list in an instant and then write the file
// without holding up the recorder.
package ring

import (
	"bytes"
	"sync"
)

// Frame locates one picture or one block of sound inside a group.
type Frame struct {
	Offset int   // where its bytes begin
	Size   int   // how many there are
	Time   int64 // when it was captured, in 100 ns units
}

// Params are the parameter sets a decoder needs before any picture.
type Params struct {
	SPS, PPS []byte
}

// Group is one keyframe, the pictures that depend on it, and the sound
// captured alongside them.
type Group struct {
	Video  []byte
	Frames []Frame
	Audio  []byte
	Sound  []Frame
	Params *Params
}

// Start returns the time of the keyframe.
func (g *Group) Start() int64 { return g.Frames[0].Time }

// Ring is safe for use by several goroutines.
type Ring struct {
	mu     sync.Mutex
	window int64 // how far back to keep, in 100 ns units
	limit  int   // most bytes to hold, 0 for no limit
	groups []*Group
	bytes  int
	params *Params
	newest int64 // time of the latest picture
	frame  int64 // how long one picture is shown at the full frame rate

	// The group being filled. Its buffers are reused from group to group.
	open   bool
	using  *Params // parameter sets in force when it was started
	video  []byte
	frames []Frame
	audio  []byte
	sound  []Frame
}

// New makes a loop that keeps the last window and never holds more than
// limit bytes. frame is how long one picture lasts at the full frame rate.
// Both times are in 100 ns units.
func New(window, frame int64, limit int) *Ring {
	return &Ring{window: window, frame: frame, limit: limit}
}

// SetFrame changes how long one picture lasts at the full frame rate.
func (r *Ring) SetFrame(frame int64) {
	r.mu.Lock()
	r.frame = frame
	r.mu.Unlock()
}

// SetLimit changes the most bytes the loop may hold; 0 removes the limit.
func (r *Ring) SetLimit(limit int) {
	r.mu.Lock()
	r.limit = limit
	r.trim()
	r.mu.Unlock()
}

// SetParams records the parameter sets in force from now on.
func (r *Ring) SetParams(sps, pps []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.params != nil && bytes.Equal(r.params.SPS, sps) && bytes.Equal(r.params.PPS, pps) {
		return
	}
	r.params = &Params{SPS: bytes.Clone(sps), PPS: bytes.Clone(pps)}
}

// AddVideo stores one picture. data is copied. Pictures before the first
// keyframe are dropped, because nothing could decode them.
func (r *Ring) AddVideo(data []byte, time int64, key bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if key {
		r.close()
		r.open, r.using = true, r.params
	}
	if !r.open || r.params == nil {
		r.open = false
		return
	}
	r.frames = append(r.frames, Frame{Offset: len(r.video), Size: len(data), Time: time})
	r.video = append(r.video, data...)
	r.bytes += len(data)
	r.newest = max(r.newest, time)
	r.trim()
}

// AddAudio stores one block of compressed sound. data is copied.
func (r *Ring) AddAudio(data []byte, time int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.open {
		return
	}
	r.sound = append(r.sound, Frame{Offset: len(r.audio), Size: len(data), Time: time})
	r.audio = append(r.audio, data...)
	r.bytes += len(data)
}

// close turns the group being filled into a finished one of exact size.
func (r *Ring) close() {
	if r.open && len(r.frames) > 0 {
		r.groups = append(r.groups, r.snapshot())
	}
	r.video, r.frames, r.audio, r.sound = r.video[:0], r.frames[:0], r.audio[:0], r.sound[:0]
	r.open = false
}

func (r *Ring) snapshot() *Group {
	return &Group{
		Video: bytes.Clone(r.video), Frames: append([]Frame(nil), r.frames...),
		Audio: bytes.Clone(r.audio), Sound: append([]Frame(nil), r.sound...),
		Params: r.using,
	}
}

// trim forgets groups that have fallen out of the window: a group goes once
// the one after it starts at or before the edge of the window, so the edge
// is always covered. The byte limit overrides the window.
func (r *Ring) trim() {
	edge := r.newest - r.window
	drop := 0
	for drop < len(r.groups) {
		var next int64
		if drop+1 < len(r.groups) {
			next = r.groups[drop+1].Start()
		} else if r.open && len(r.frames) > 0 {
			next = r.frames[0].Time
		} else {
			break
		}
		g := r.groups[drop]
		over := r.limit > 0 && r.bytes > r.limit
		if next > edge && !over && !stale(g, edge) {
			break
		}
		r.bytes -= len(g.Video) + len(g.Audio)
		drop++
	}
	if drop > 0 {
		r.groups = append(r.groups[:0:0], r.groups[drop:]...)
	}
}

// stale reports whether a group ended well before the edge of the window.
// Normally the next group follows within a couple of seconds, so the last
// picture of a group that straddles the edge is still wanted. After a break
// in recording that picture would be shown for the whole break; the group
// is dropped instead and the clip starts after the break.
func stale(g *Group, edge int64) bool {
	const longest = 5 * 10_000_000
	return g.Frames[len(g.Frames)-1].Time+longest < edge
}

// Clip is a copy of the loop taken at one moment.
type Clip struct {
	Groups []*Group
	End    int64 // when the last picture stops being shown
}

// Snapshot returns the last window of footage. The groups it returns are
// never modified afterwards, so the caller may read them at leisure.
func (r *Ring) Snapshot() Clip {
	r.mu.Lock()
	defer r.mu.Unlock()
	groups := append([]*Group(nil), r.groups...)
	if r.open && len(r.frames) > 0 {
		groups = append(groups, r.snapshot())
	}
	// Keep only the newest run that shares one set of parameters.
	first := len(groups)
	for first > 0 && groups[first-1].Params == groups[len(groups)-1].Params {
		first--
	}
	groups = groups[first:]
	// Start from the last group that begins at or before the window's edge.
	edge := r.newest - r.window
	for len(groups) > 1 && (groups[1].Start() <= edge || stale(groups[0], edge)) {
		groups = groups[1:]
	}
	return Clip{Groups: groups, End: r.newest + r.frame}
}

// Stats reports how much footage is held: its length in 100 ns units, never
// more than the window, and its size in bytes.
func (r *Ring) Stats() (length int64, size int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	start := r.newest
	if len(r.groups) > 0 {
		start = r.groups[0].Start()
	} else if r.open && len(r.frames) > 0 {
		start = r.frames[0].Time
	}
	if r.bytes == 0 {
		return 0, 0
	}
	return min(r.newest+r.frame-start, r.window), r.bytes
}

// Reset forgets everything.
func (r *Ring) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.groups, r.bytes, r.newest = nil, 0, 0
	r.video, r.frames, r.audio, r.sound = nil, nil, nil, nil
	r.open = false
}
