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
	// How long a block of what the computer plays is held back for the
	// microphone's sound of the same moment to arrive, before going
	// without it.
	micPatience = clock.Second / 4
)

// listen is the sound thread. Like the capture thread it keeps trying: if
// the device goes away or changes, it waits a moment and starts again on
// whatever device is the default by then.
func (r *Recorder) listen() {
	defer r.done.Done()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	com.Init()
	for !r.stopped() {
		err := guarded(r.hear)
		r.set(func(s *Status) {
			s.Sound, s.Mic = false, false
			switch {
			case err == nil:
			case r.micAlone():
				s.MicNote = describeMic(err)
			default:
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

// micAlone reports whether the microphone is the only sound recorded.
func (r *Recorder) micAlone() bool {
	return r.options.Mic && !r.options.Sound && r.options.SoundSource == nil
}

// describe puts a failure to record what the computer plays in plain words.
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

// describeMic puts a failure to record the microphone in plain words.
func describeMic(err error) string {
	switch com.Code(err) {
	case 0x80070490: // no such element
		return "The microphone is not connected."
	case 0x88890004: // the device was unplugged or disabled
		return "The microphone went away."
	case 0x80070005: // access denied
		return "Windows is blocking the microphone."
	}
	if err == audio.ErrDeviceChanged {
		return "The microphone is changing."
	}
	return "The microphone cannot be recorded (" + err.Error() + ")."
}

// microphone starts capture from the microphone the user chose.
func (r *Recorder) microphone() (audio.Input, error) {
	if r.options.MicSource != nil {
		return r.options.MicSource(r.epoch)
	}
	return audio.Microphone(r.options.MicDevice)
}

// hear records sound until stopped or until the device fails.
//
// What the computer plays leads. When a microphone is recorded as well it
// is added to that, sample by sample, each block of one with the
// microphone's sound of the same moment. A microphone that fails or is
// unplugged does not stop the rest: the clip goes on without it and it is
// tried again. With only the microphone asked for, it leads by itself.
func (r *Recorder) hear() error {
	o := r.options
	alone := r.micAlone()
	var input audio.Input
	var err error
	switch {
	case o.SoundSource != nil:
		input, err = o.SoundSource(r.epoch)
	case alone:
		input, err = r.microphone()
	default:
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
	r.set(func(s *Status) {
		s.Sound, s.Silent = !alone, ""
		if alone {
			s.Mic, s.MicNote = true, ""
		}
	})

	var mic audio.Input // the microphone, while it is being added in
	var voice *audio.Timeline
	var retry int64 // when to try for the microphone again
	drop := func(why error) {
		if mic != nil {
			mic.Close()
		}
		mic, voice, retry = nil, nil, clock.Now()+2*clock.Second
		r.set(func(s *Status) { s.Mic, s.MicNote = false, describeMic(why) })
	}
	defer func() {
		if mic != nil {
			mic.Close()
		}
	}()

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
		if o.Mic && !alone && mic == nil && clock.Now() >= retry {
			if mic, err = r.microphone(); err != nil {
				mic = nil
				drop(err)
			} else if micRate, micChannels := mic.Format(); micRate != rate || micChannels != channels {
				drop(errDifferent)
				retry = 1 << 62 // it will not change by waiting
			} else {
				voice = audio.NewTimeline(rate, channels)
				r.set(func(s *Status) { s.Mic, s.MicNote = true, "" })
			}
		}
		for mic != nil {
			samples, at, err := mic.Read()
			if err != nil {
				drop(err)
			} else if samples == nil {
				break
			} else {
				voice.Add(samples, at)
			}
		}
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
			at, ok := line.Ready(soundBlock)
			if !ok {
				break
			}
			if voice != nil {
				// The microphone's packets arrive on their own schedule.
				// Wait a moment for the ones that belong with this block.
				end := at + soundBlock*clock.Second/int64(rate)
				if !voice.Covers(end) && clock.Now()-end < micPatience {
					break
				}
			}
			block, at, _ := line.Take(soundBlock)
			if voice != nil {
				audio.Mix(block, voice.Cut(at, soundBlock))
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
