//go:build windows

package main

import (
	"lazareplay/internal/clock"
)

// clicker stands in for the sound the computer plays. It is silent except
// for a one-millisecond click at the start of every second, counted from
// the same zero as the lamp on the test card. In a saved clip each click
// should line up with the lamp coming on.
type clicker struct {
	epoch    int64
	start    int64 // time of the first sample
	produced int64 // frames handed out so far
	samples  []int16
}

const clickRate = 48000

func (c *clicker) Format() (rate, channels int) { return clickRate, 2 }

func (c *clicker) Read() ([]int16, int64, error) {
	now := clock.Now()
	if c.start == 0 {
		c.start = now
	}
	due := (now - c.start) * clickRate / clock.Second
	if due-c.produced < 480 {
		return nil, 0, nil
	}
	if c.samples == nil {
		c.samples = make([]int16, 480*2)
	}
	at := c.start + c.produced*clock.Second/clickRate
	for i := 0; i < 480; i++ {
		when := c.start + (c.produced+int64(i))*clock.Second/clickRate - c.epoch
		level := int16(0)
		if when > 0 && when%clock.Second < clock.Second/1000 {
			level = 20000
		}
		c.samples[2*i], c.samples[2*i+1] = level, level
	}
	c.produced += 480
	return c.samples, at, nil
}

func (c *clicker) Close() {}
