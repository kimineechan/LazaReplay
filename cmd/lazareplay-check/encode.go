//go:build windows

package main

import (
	"fmt"
	"time"

	"lazareplay/internal/clock"
	"lazareplay/internal/dx"
	"lazareplay/internal/h264"
	"lazareplay/internal/mf"
	"lazareplay/internal/testcard"
)

// encoders lists the encoders to try for a device: its own hardware ones,
// then the software one. With software=1 only the latter.
func encoders(device *dx.Device) []*mf.Codec {
	var list []*mf.Codec
	if option("software", 0) == 0 {
		hardware, err := mf.VideoEncoders(device.Adapter.ID(), device.Adapter.VendorID)
		if err != nil {
			fmt.Println("hardware encoders:", err)
		}
		list = append(list, hardware...)
	}
	software, err := mf.VideoEncoders(nil, 0)
	if err != nil {
		fmt.Println("software encoders:", err)
	}
	return append(list, software...)
}

// encode feeds generated frames through conversion and the encoder and
// prints what comes out.
func encode() {
	device, _ := primary()
	const width, height, rate = 1920, 1080, 60
	frames := option("frames", 120)
	pixels := make([]byte, width*height*4)
	for i := 0; i < len(pixels); i += 4 {
		pixels[i], pixels[i+1], pixels[i+2], pixels[i+3] = 40, 40, 40, 255
	}
	testcard.Paint(pixels, width*4, 0, true)
	source, err := device.NewTexture(width, height, dx.FormatBGRA, dx.UsageDefault, dx.BindRenderTarget|dx.BindShaderResource, 0, pixels, width*4)
	if err != nil {
		fail(err)
	}
	converter, err := device.NewConverter(width, height, width, height, rate)
	if err != nil {
		fail(err)
	}
	var targets []*dx.Target
	for i := 0; i < 6; i++ {
		t, err := converter.NewTarget()
		if err != nil {
			fail(err)
		}
		targets = append(targets, t)
	}
	config := mf.Config{Width: width, Height: height, Rate: rate, Bitrate: 10_000_000, Peak: 15_000_000, KeyInterval: 2 * rate}
	var encoder *mf.Encoder
	for _, codec := range encoders(device) {
		if encoder, err = codec.Open(device.Object, config); err == nil {
			break
		}
		fmt.Println("skipping:", err)
	}
	if encoder == nil {
		fail(fmt.Errorf("no encoder could be started"))
	}
	defer encoder.Close()
	fmt.Printf("encoder: %s (hardware=%v), header from type: %d bytes\n", encoder.Name, encoder.Hardware, len(encoder.Header()))

	var outputs, total, keys, dropped, busy, described int
	var submitTime, collectTime []int64
	var lastTime int64 = -1
	ordered := true
	take := func(data []byte, at int64) {
		outputs++
		total += len(data)
		if at <= lastTime {
			ordered = false
		}
		lastTime = at
		_, picture := h264.Convert(nil, data)
		if picture.Key {
			keys++
		}
		if described < 3 || (picture.SPS != nil && described < 6) {
			described++
			kinds := ""
			h264.Split(data, func(unit []byte) { kinds += fmt.Sprintf(" %d(%d)", unit[0]&0x1F, len(unit)) })
			fmt.Printf("  output at %6.1f ms, %6d bytes, units:%s\n", ms(at), len(data), kinds)
			if picture.SPS != nil {
				if s, err := h264.ParseSPS(picture.SPS); err != nil {
					fmt.Println("  parameter set:", err)
				} else {
					fmt.Printf("  parameter set: profile %d level %d, %dx%d, %d reference frames, colour described=%v (primaries %d transfer %d matrix %d full=%v)\n",
						s.Profile, s.Level, s.Width, s.Height, s.ReferenceFrames, s.ColourDescribed, s.Primaries, s.Transfer, s.Matrix, s.FullRange)
				}
			}
		}
	}
	interval := int64(clock.Second / rate)
	ticker := clock.NewTicker(interval)
	defer ticker.Close()
	next := 0
	for i := 0; i < frames; i++ {
		ticker.Wait()
		testcard.Paint(pixels, width*4, i, false)
		device.Update(source, pixels, width*4)
		began := clock.Now()
		if err := encoder.Collect(take); err != nil {
			fail(err)
		}
		collectTime = append(collectTime, clock.Now()-began)
		began = clock.Now()
		ok, err := encoder.Accepting()
		if err != nil {
			fail(err)
		}
		var target *dx.Target
		for range targets {
			next = (next + 1) % len(targets)
			if targets[next].Idle() {
				target = targets[next]
				break
			}
		}
		switch {
		case !ok:
			dropped++
		case target == nil:
			busy++
		default:
			if err := converter.Convert(source, target); err != nil {
				fail(err)
			}
			if err := encoder.Submit(target.Texture, int64(i)*interval, interval, i%config.KeyInterval == 0); err != nil {
				fail(err)
			}
		}
		device.Flush()
		submitTime = append(submitTime, clock.Now()-began)
	}
	for wait := 0; wait < 50 && outputs+dropped+busy < frames; wait++ {
		time.Sleep(10 * time.Millisecond)
		if err := encoder.Collect(take); err != nil {
			fail(err)
		}
	}
	fmt.Printf("%d frames in: %d out, %d keyframes, %d refused by the encoder, %d with no free texture, in order=%v\n", frames, outputs, keys, dropped, busy, ordered)
	fmt.Printf("%d bytes, %.2f Mbit/s\n", total, float64(total)*8*rate/float64(max(outputs, 1))/1e6)
	fmt.Printf("processor time per frame: submit median %.3f ms worst %.3f ms, collect median %.3f ms worst %.3f ms\n",
		ms(median(submitTime)), ms(largest(submitTime)), ms(median(collectTime)), ms(largest(collectTime)))
}
