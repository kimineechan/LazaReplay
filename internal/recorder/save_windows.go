//go:build windows

package recorder

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"lazareplay/internal/clock"
	"lazareplay/internal/mp4"
	"lazareplay/internal/ring"
)

// Clip describes a saved file.
type Clip struct {
	Path   string
	Length time.Duration
	Bytes  int64
}

// soundFormat describes the sound in the loop.
type soundFormat struct {
	Rate, Channels int
}

// Save writes what the loop holds to an MP4 file at path. Recording carries
// on while it does: the loop is copied in an instant (the footage itself is
// shared, not duplicated) and the file is written from that copy.
func (r *Recorder) Save(path string) (Clip, error) {
	movie, length, err := build(r.ring.Snapshot(), r.sound.Load())
	if err != nil {
		return Clip{}, err
	}
	folder := filepath.Dir(path)
	if err := os.MkdirAll(folder, 0755); err != nil {
		return Clip{}, err
	}
	if size, err := movie.Size(); err == nil {
		if free, ok := room(folder); ok && free < size+8<<20 {
			return Clip{}, fmt.Errorf("There is not enough room on that disk: the clip needs %d MB and %d MB are free.", size>>20+1, free>>20)
		}
	}
	// Write at background priority so a game loading from the same disk is
	// served first. Windows applies that to this thread only.
	runtime.LockOSThread()
	thread, _, _ := getCurrentThread.Call()
	setThreadPriority.Call(thread, 0x00010000) // THREAD_MODE_BACKGROUND_BEGIN
	defer func() {
		setThreadPriority.Call(thread, 0x00020000) // THREAD_MODE_BACKGROUND_END
		runtime.UnlockOSThread()
	}()

	// The file appears under its final name only when it is complete.
	part := path + ".part"
	file, err := os.Create(part)
	if err != nil {
		return Clip{}, err
	}
	written, err := movie.WriteTo(file)
	if closing := file.Close(); err == nil {
		err = closing
	}
	if err == nil {
		err = os.Rename(part, path)
	}
	if err != nil {
		os.Remove(part)
		return Clip{}, err
	}
	return Clip{Path: path, Length: length, Bytes: written}, nil
}

// room returns the free space, in bytes, on the disk that holds a folder.
func room(folder string) (int64, bool) {
	path, err := syscall.UTF16PtrFromString(folder)
	if err != nil {
		return 0, false
	}
	var free uint64
	ok, _, _ := kernel32.NewProc("GetDiskFreeSpaceExW").Call(uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&free)), 0, 0)
	return int64(free), ok != 0
}

// build lays a copy of the loop out as an MP4 file.
func build(clip ring.Clip, sound *soundFormat) (*mp4.Movie, time.Duration, error) {
	if len(clip.Groups) == 0 {
		return nil, 0, errors.New("Nothing has been recorded yet.")
	}
	movie := &mp4.Movie{Created: time.Now()}
	params := clip.Groups[0].Params
	movie.Video.SPS, movie.Video.PPS = params.SPS, params.PPS
	start := clip.Groups[0].Start()

	// Picture times become durations: each picture lasts until the next.
	// Times are converted first and subtracted after, so rounding never
	// accumulates over a long clip.
	scale := func(t int64) int64 { return (t - start) * 9 / 1000 } // 100 ns to 1/90000 s
	var times []int64
	for _, g := range clip.Groups {
		for _, f := range g.Frames {
			times = append(times, f.Time)
		}
	}
	times = append(times, max(clip.End, times[len(times)-1]+1))

	n := 0
	var audio *mp4.Audio
	for _, g := range clip.Groups {
		for i, f := range g.Frames {
			movie.Video.Sizes = append(movie.Video.Sizes, uint32(f.Size))
			movie.Video.Durations = append(movie.Video.Durations, uint32(max(scale(times[n+1])-scale(times[n]), 1)))
			movie.Video.Keys = append(movie.Video.Keys, i == 0)
			n++
		}
		movie.Chunks = append(movie.Chunks, mp4.Chunk{Samples: len(g.Frames), Data: g.Video})
		if sound == nil {
			continue
		}
		// Sound that was captured before the first picture is left out.
		skip := 0
		for audio == nil && skip < len(g.Sound) && g.Sound[skip].Time < start {
			skip++
		}
		if skip == len(g.Sound) {
			continue
		}
		if audio == nil {
			audio = &mp4.Audio{
				Rate: sound.Rate, Channels: sound.Channels, Bitrate: soundBitrate,
				Delay: (g.Sound[skip].Time - start) * int64(sound.Rate) / clock.Second,
			}
		}
		for _, f := range g.Sound[skip:] {
			audio.Sizes = append(audio.Sizes, uint32(f.Size))
		}
		movie.Chunks = append(movie.Chunks, mp4.Chunk{Audio: true, Samples: len(g.Sound) - skip, Data: g.Audio[g.Sound[skip].Offset:]})
	}
	movie.Audio = audio
	return movie, time.Duration((clip.End - start) * 100), nil
}
