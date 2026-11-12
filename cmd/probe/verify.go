//go:build windows

package main

import (
	"fmt"
	"math"
	"sort"

	"lazareplay/internal/mf"
	"lazareplay/internal/testcard"
)

// verify plays a saved clip through the decoders built into Windows and
// checks, by numbers, that it holds what was recorded: every frame of the
// test card once and in order, the right colours, the mouse pointers, even
// timing, and sound of the right length.
func verify() {
	path := text("file", "probe.mp4")
	reader, err := mf.OpenReader(path)
	if err != nil {
		fail(err)
	}
	defer reader.Close()
	fmt.Printf("file states a length of %.3f s\n", float64(reader.Duration())/1e7)
	if err := reader.Video(); err != nil {
		fail(err)
	}
	pointers := option("pointers", 0) == 1
	scale := float64(option("scale", 100)) / 100 // recorded size over screen size

	timed := option("clock", 0) == 1 // the card shows milliseconds, not a frame count
	var numbers []int
	var times, lampOn, origins []int64
	lampLit := false
	unread := 0
	worstBar, worstSpot, worstBlock := 0.0, 0.0, 0.0
	worstSpotName := ""
	for {
		data, at, err := reader.NextVideo()
		if err != nil {
			fail(err)
		}
		if data == nil {
			break
		}
		_, _, stride := reader.Layout()
		rows := len(data) * 2 / 3 / stride
		at2 := func(x, y int) (int, int) { return int(float64(x)*scale + 0.5), int(float64(y)*scale + 0.5) }
		luma := func(x, y int) int {
			x, y = at2(x, y)
			return int(data[y*stride+x])
		}
		colour := func(x, y int) testcard.Colour {
			x, y = at2(x, y)
			c := data[(rows+y/2)*stride+x&^1:]
			return testcard.Colour{Y: float64(data[y*stride+x]), Cb: float64(c[0]), Cr: float64(c[1])}
		}
		times = append(times, at)
		lx, ly := testcard.LampCentre()
		if level := luma(lx, ly); level > 190 && !lampLit {
			lampLit = true
			if len(times) > 1 {
				lampOn = append(lampOn, at)
			}
		} else if level < 60 {
			lampLit = false
		}
		n, ok := testcard.Number(luma)
		if !ok {
			unread++
			continue
		}
		numbers = append(numbers, n)
		if timed {
			// The picture says when it was taken; the file says when it is
			// shown. The difference is the moment the clip begins.
			origins = append(origins, int64(n)*10000-at)
		}
		for i := range testcard.Bars {
			if pointers && i < 3 {
				continue // checked point by point below
			}
			x, y := testcard.BarCentre(i)
			worstBar = math.Max(worstBar, colour(x, y).Distance(testcard.Expected(i)))
		}
		x, y := testcard.BlockCentre(n)
		worstBlock = math.Max(worstBlock, math.Abs(float64(luma(x, y))-235))
		if pointers {
			for _, s := range spots(n) {
				if d := colour(s.x, s.y).Distance(s.want); d > worstSpot {
					worstSpot, worstSpotName = d, fmt.Sprintf("%s in frame %d: got %.0f, want %.0f", s.name, n, colour(s.x, s.y), s.want)
				}
			}
		}
	}
	width, height, stride := reader.Layout()
	fmt.Printf("decoded %d pictures, %dx%d coded (%d bytes per row)\n", len(times), width, height, stride)
	if len(numbers) == 0 {
		fmt.Printf("the test card was not found in any picture (%d could not be read)\n", unread)
	} else {
		missing, repeated, backwards := 0, 0, 0
		for i := 1; i < len(numbers); i++ {
			switch d := numbers[i] - numbers[i-1]; {
			case d == 0:
				repeated++
			case d < 0:
				backwards++
			case d > 1:
				missing += d - 1
			}
		}
		fmt.Printf("card frames %d to %d: %d missing, %d repeated, %d out of order, %d pictures unreadable\n",
			numbers[0], numbers[len(numbers)-1], missing, repeated, backwards, unread)
		fmt.Printf("colour bars: largest error %.1f of 255; moving square: largest error %.1f\n", worstBar, worstBlock)
		if pointers {
			fmt.Printf("mouse pointers: largest error %.1f of 255 (%s)\n", worstSpot, worstSpotName)
		}
	}
	if len(times) > 1 {
		var gaps []int64
		for i := 1; i < len(times); i++ {
			gaps = append(gaps, times[i]-times[i-1])
		}
		sort.Slice(gaps, func(i, j int) bool { return gaps[i] < gaps[j] })
		fmt.Printf("time between pictures: shortest %.2f ms, median %.2f ms, longest %.2f ms; first at %.2f ms, last at %.3f s\n",
			ms(gaps[0]), ms(gaps[len(gaps)/2]), ms(gaps[len(gaps)-1]), ms(times[0]), float64(times[len(times)-1])/1e7)
	}

	rate, channels, err := reader.Audio()
	if err != nil {
		fmt.Println("no sound track")
		return
	}
	var samples, peak int
	var clicks, peaks []int64
	loudest := 0
	var energy float64
	var first, last int64 = -1, 0
	for {
		data, at, err := reader.NextAudio()
		if err != nil {
			fail(err)
		}
		if data == nil {
			break
		}
		if first < 0 {
			first = at
		}
		last = at
		for i := 0; i+1 < len(data); i += 2 {
			v := int(int16(uint16(data[i]) | uint16(data[i+1])<<8))
			energy += float64(v) * float64(v)
			if v < 0 {
				v = -v
			}
			peak = max(peak, v)
			if v > 6000 {
				when := at + int64(i/2/max(channels, 1))*1e7/int64(max(rate, 1))
				if len(clicks) == 0 || when-clicks[len(clicks)-1] > 1e6 {
					clicks = append(clicks, when)
					loudest = 0
				}
				if v > loudest {
					loudest = v
					if len(peaks) < len(clicks) {
						peaks = append(peaks, when)
					} else {
						peaks[len(peaks)-1] = when
					}
				}
			}
			samples++
		}
	}
	frames := samples / max(channels, 1)
	fmt.Printf("sound: %d Hz, %d channels, %.3f s, starts at %.1f ms, last block at %.3f s, peak %d of 32767, average level %.1f\n",
		rate, channels, float64(frames)/float64(max(rate, 1)), ms(first), float64(last)/1e7, peak, math.Sqrt(energy/float64(max(samples, 1))))
	if len(clicks) > 0 {
		line := "clicks heard at, in seconds:"
		for _, click := range clicks {
			line += fmt.Sprintf(" %.4f", float64(click)/1e7)
		}
		fmt.Println(line)
	}
	if timed && len(origins) > 0 && len(peaks) > 0 {
		// Each click was made exactly on a whole second. Where the clip
		// begins is known from the pictures, so where each click should be
		// heard in the file is known too.
		sort.Slice(origins, func(i, j int) bool { return origins[i] < origins[j] })
		origin := origins[len(origins)/2]
		var early []int64
		line := "sound later than it should be, in ms (start of click / loudest point):"
		for i := range peaks {
			second := (clicks[i] + origin + 5e6) / 1e7 * 1e7
			line += fmt.Sprintf(" %+.1f/%+.1f", ms(clicks[i]+origin-second), ms(peaks[i]+origin-second))
			early = append(early, peaks[i]+origin-second)
		}
		sort.Slice(early, func(i, j int) bool { return early[i] < early[j] })
		fmt.Println(line)
		fmt.Printf("pictures disagree about the start of the clip by at most %.1f ms; middle click is %+.1f ms from its second\n",
			ms(origins[len(origins)-1]-origins[0]), ms(early[len(early)/2]))
		return
	}
	if len(clicks) > 0 && len(lampOn) > 0 {
		// Pair each time the lamp came on with the nearest click.
		var gaps []int64
		line := "picture minus sound, in ms:"
		for _, lit := range lampOn {
			best := int64(math.MaxInt64)
			for _, click := range clicks {
				if d := lit - click; abs(d) < abs(best) {
					best = d
				}
			}
			gaps = append(gaps, best)
			line += fmt.Sprintf(" %+.1f", ms(best))
		}
		sort.Slice(gaps, func(i, j int) bool { return gaps[i] < gaps[j] })
		fmt.Println(line)
		fmt.Printf("%d lamp flashes, %d clicks; in the middle case the picture is %+.1f ms from the sound\n", len(lampOn), len(clicks), ms(gaps[len(gaps)/2]))
	}
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
