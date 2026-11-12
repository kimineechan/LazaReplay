//go:build windows

package recorder

import (
	"runtime"
	"time"

	"lazareplay/internal/audio"
	"lazareplay/internal/clock"
	"lazareplay/internal/com"
	"lazareplay/internal/mf"
)

const (
	soundBlock = 1024 // frames in one block of AAC
	// The encoder holds sound back by this many frames before it comes out,
	// so each block is stamped that much earlier than the samples fed in
	// with it. Measured with the self-test's lamp and click.
	soundDelay = 0
)

// listen is the sound thread. Like the capture thread it keeps trying: if
// the playback device goes away or changes, it waits a moment and starts
// again on whatever device is the default by then.
func (r *Recorder) listen() {
	defer r.done.Done()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	com.Init()
	for !r.stopped() {
		err := guarded(r.hear)
		r.set(func(s *Status) {
			s.Sound = false
			if err != nil {
				s.Silent = describe(err)
			}
		})
		select {
		case <-r.stop:
			return
		case <-time.After(time.Second):
		}
	}
}

// describe puts a sound failure in plain words.
func describe(err error) string {
	switch com.Code(err) {
	case 0x80070490: // no such element
		return "No playback device was found, so clips will be silent."
	case 0x88890004: // the device was unplugged or disabled
		return "The playback device went away. Sound will resume on the next one."
	}
	if err == audio.ErrDeviceChanged {
		return "The playback device changed. Sound is moving to the new one."
	}
	return "Sound cannot be recorded (" + err.Error() + ")."
}

// hear records sound until stopped or until the device fails.
func (r *Recorder) hear() error {
	var input audio.Input
	var err error
	if r.options.SoundSource != nil {
		input, err = r.options.SoundSource(r.epoch)
	} else {
		input, err = audio.Loopback()
	}
	if err != nil {
		return err
	}
	defer input.Close()
	rate, channels := input.Format()
	encoder, err := mf.NewAAC(rate, channels, soundBitrate)
	if err != nil {
		return err
	}
	defer encoder.Close()
	r.sound.Store(&soundFormat{Rate: rate, Channels: channels})
	r.set(func(s *Status) { s.Sound, s.Silent = true, "" })

	line := audio.NewTimeline(rate, channels)
	var times []int64 // when each block handed to the encoder began
	store := func(block []byte) {
		if len(times) == 0 {
			return
		}
		r.ring.AddAudio(block, times[0]-r.epoch-soundDelay*clock.Second/int64(rate))
		times = times[1:]
	}
	for !r.stopped() {
		// A hundred short wake-ups a second. Each finds a packet of ten
		// milliseconds waiting, or nothing.
		time.Sleep(10 * time.Millisecond)
		arrived := false
		for {
			samples, at, err := input.Read()
			if err != nil {
				return err
			}
			if samples == nil {
				break
			}
			line.Add(samples, at)
			arrived = true
		}
		if !arrived {
			line.Idle(clock.Now())
		}
		for {
			block, at, ok := line.Take(soundBlock)
			if !ok {
				break
			}
			if len(times) > 8 {
				times = times[1:]
			}
			times = append(times, at)
			if err := encoder.Encode(block, at-r.epoch, store); err != nil {
				return err
			}
		}
	}
	return nil
}
