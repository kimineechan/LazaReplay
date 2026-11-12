//go:build windows

package main

import (
	"fmt"
	"math"
	"sort"

	"lazareplay/internal/mf"
	"lazareplay/internal/testcard"
)

// findings is what playing a clip back showed.
type findings struct {
	Stated   float64 // the length the file states for itself, in seconds
	Pictures int
	Width    int // of the decoded picture; the height can include a few spare rows
	Height   int

	// The test card, when the clip shows it.
	Card                       bool
	First, Last                int // frame numbers read from the card
	Missing, Repeated, Reverse int
	Unread                     int     // pictures in which the card could not be read
	Bars, Square, Spots        float64 // largest colour errors, out of 255
	SpotNote                   string

	Shortest, Median, Longest float64 // milliseconds between pictures
	End                       float64 // time of the last picture, in seconds

	Sound          bool
	Rate, Channels int
	SoundLength    float64 // seconds
	SoundStart     float64 // milliseconds
	Peak           int     // loudest sample, out of 32767
	Level          float64 // average level
	Tone           float64 // share of the sound that is a 1000 Hz tone, 0 to 1

	Clicks  []int64   // when each click starts, in 100 ns units
	Gaps    []float64 // picture minus sound for each lamp flash, in milliseconds
	Offsets []float64 // in clock mode: how late each click is, in milliseconds
	Spread  float64   // in clock mode: how far the pictures disagree about the start
}

// middle returns the middle value of a list.
func middle(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	return sorted[len(sorted)/2]
}

// examine plays a clip through the decoders built into Windows and reads
// it as numbers. scale is the size of the recorded picture over the size
// of the screen it shows. With pointers set the colours under the three
// test pointers are checked; with timed set the card is taken to show the
// time in milliseconds instead of a frame count.
func examine(path string, scale float64, pointers, timed bool) (*findings, error) {
	reader, err := mf.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	f := &findings{Stated: float64(reader.Duration()) / 1e7}
	if err := reader.Video(); err != nil {
		return nil, err
	}
	var numbers []int
	var times, lampOn, origins []int64
	lampLit := false
	for {
		data, at, err := reader.NextVideo()
		if err != nil {
			return nil, err
		}
		if data == nil {
			break
		}
		_, _, stride := reader.Layout()
		rows := len(data) * 2 / 3 / stride
		place := func(x, y int) (int, int) {
			return min(int(float64(x)*scale+0.5), stride-1), min(int(float64(y)*scale+0.5), rows-1)
		}
		luma := func(x, y int) int {
			x, y = place(x, y)
			return int(data[y*stride+x])
		}
		colour := func(x, y int) testcard.Colour {
			x, y = place(x, y)
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
			f.Unread++
			continue
		}
		// The bars must be there too, or this is not the card at all.
		wx, wy := testcard.BarCentre(0)
		bx, by := testcard.BarCentre(7)
		if luma(wx, wy) < 200 || luma(bx, by) > 50 {
			f.Unread++
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
			f.Bars = math.Max(f.Bars, colour(x, y).Distance(testcard.Expected(i)))
		}
		x, y := testcard.BlockCentre(n)
		f.Square = math.Max(f.Square, math.Abs(float64(luma(x, y))-235))
		if pointers {
			for _, s := range spots(n) {
				if d := colour(s.x, s.y).Distance(s.want); d > f.Spots {
					f.Spots, f.SpotNote = d, fmt.Sprintf("%s in frame %d: got %.0f, want %.0f", s.name, n, colour(s.x, s.y), s.want)
				}
			}
		}
	}
	f.Pictures = len(times)
	f.Width, f.Height, _ = reader.Layout()
	if len(numbers) > 0 {
		f.Card, f.First, f.Last = true, numbers[0], numbers[len(numbers)-1]
		for i := 1; i < len(numbers); i++ {
			switch d := numbers[i] - numbers[i-1]; {
			case d == 0:
				f.Repeated++
			case d < 0:
				f.Reverse++
			case d > 1:
				f.Missing += d - 1
			}
		}
	}
	if len(times) > 1 {
		var gaps []float64
		for i := 1; i < len(times); i++ {
			gaps = append(gaps, ms(times[i]-times[i-1]))
		}
		sort.Float64s(gaps)
		f.Shortest, f.Median, f.Longest = gaps[0], gaps[len(gaps)/2], gaps[len(gaps)-1]
		f.End = float64(times[len(times)-1]) / 1e7
	}

	rate, channels, err := reader.Audio()
	if err != nil {
		return f, nil // a silent clip
	}
	f.Sound, f.Rate, f.Channels = true, rate, channels
	channels, rate = max(channels, 1), max(rate, 1)
	var samples, loudest, phase int
	var energy, sine, cosine, all float64
	var peaks []int64
	first := int64(-1)
	for {
		data, at, err := reader.NextAudio()
		if err != nil {
			return nil, err
		}
		if data == nil {
			break
		}
		if first < 0 {
			first = at
		}
		for i := 0; i+1 < len(data); i += 2 {
			v := int(int16(uint16(data[i]) | uint16(data[i+1])<<8))
			if i/2%channels == 0 { // follow a 1000 Hz tone on the first channel
				angle := 2 * math.Pi * 1000 * float64(phase) / float64(rate)
				sine += float64(v) * math.Sin(angle)
				cosine += float64(v) * math.Cos(angle)
				all += float64(v) * float64(v)
				phase++
			}
			energy += float64(v) * float64(v)
			if v < 0 {
				v = -v
			}
			f.Peak = max(f.Peak, v)
			if v > 6000 {
				when := at + int64(i/2/channels)*1e7/int64(rate)
				if len(f.Clicks) == 0 || when-f.Clicks[len(f.Clicks)-1] > 1e6 {
					f.Clicks = append(f.Clicks, when)
					peaks = append(peaks, when)
					loudest = 0
				}
				if v > loudest {
					loudest, peaks[len(peaks)-1] = v, when
				}
			}
			samples++
		}
	}
	f.SoundLength = float64(samples/channels) / float64(rate)
	f.SoundStart = ms(first)
	f.Level = math.Sqrt(energy / float64(max(samples, 1)))
	if all > 0 {
		f.Tone = 2 * (sine*sine + cosine*cosine) / float64(phase) / all
	}
	if timed && len(origins) > 0 {
		// Each click was made exactly on a whole second. Where the clip
		// begins is known from the pictures, so where each click should be
		// heard in the file is known too.
		sort.Slice(origins, func(i, j int) bool { return origins[i] < origins[j] })
		origin := origins[len(origins)/2]
		f.Spread = ms(origins[len(origins)-1] - origins[0])
		for _, peak := range peaks {
			second := (peak + origin + 5e6) / 1e7 * 1e7
			f.Offsets = append(f.Offsets, ms(peak+origin-second))
		}
	}
	// Pair each time the lamp came on with the nearest click.
	for _, lit := range lampOn {
		best := int64(math.MaxInt64)
		for _, click := range f.Clicks {
			if d := lit - click; abs(d) < abs(best) {
				best = d
			}
		}
		if best != math.MaxInt64 {
			f.Gaps = append(f.Gaps, ms(best))
		}
	}
	return f, nil
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// verify prints what examine finds in a clip. It is a development aid.
func verify() {
	f, err := examine(text("file", "probe.mp4"), float64(option("scale", 100))/100, option("pointers", 0) == 1, option("clock", 0) == 1)
	if err != nil {
		fail(err)
	}
	fmt.Printf("file states a length of %.3f s\n", f.Stated)
	fmt.Printf("decoded %d pictures, %dx%d coded\n", f.Pictures, f.Width, f.Height)
	if !f.Card {
		fmt.Printf("the test card was not found in any picture (%d could not be read)\n", f.Unread)
	} else {
		fmt.Printf("card frames %d to %d: %d missing, %d repeated, %d out of order, %d pictures unreadable\n", f.First, f.Last, f.Missing, f.Repeated, f.Reverse, f.Unread)
		fmt.Printf("colour bars: largest error %.1f of 255; moving square: largest error %.1f\n", f.Bars, f.Square)
		if f.SpotNote != "" {
			fmt.Printf("mouse pointers: largest error %.1f of 255 (%s)\n", f.Spots, f.SpotNote)
		}
	}
	fmt.Printf("time between pictures: shortest %.2f ms, median %.2f ms, longest %.2f ms; last at %.3f s\n", f.Shortest, f.Median, f.Longest, f.End)
	if !f.Sound {
		fmt.Println("no sound track")
		return
	}
	fmt.Printf("sound: %d Hz, %d channels, %.3f s, starts at %.1f ms, peak %d of 32767, average level %.1f\n", f.Rate, f.Channels, f.SoundLength, f.SoundStart, f.Peak, f.Level)
	if f.Tone > 0 {
		fmt.Printf("share of the sound that is a 1000 Hz tone: %.0f%%\n", 100*f.Tone)
	}
	if len(f.Clicks) > 0 {
		line := "clicks heard at, in seconds:"
		for _, click := range f.Clicks {
			line += fmt.Sprintf(" %.4f", float64(click)/1e7)
		}
		fmt.Println(line)
	}
	if len(f.Offsets) > 0 {
		fmt.Printf("sound later than it should be, in ms: %.1f; middle %+.1f; pictures disagree about the start by %.1f ms\n", f.Offsets, middle(f.Offsets), f.Spread)
	} else if len(f.Gaps) > 0 {
		fmt.Printf("picture minus sound, in ms: %.1f; middle %+.1f\n", f.Gaps, middle(f.Gaps))
	}
}
