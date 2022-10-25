package sdk

import (
	"go.uber.org/atomic"
)

// a single Synchronizer is shared between audio and video writers
// used for creating PTS
type Synchronizer struct {
	startTime atomic.Int64
	endTime   atomic.Int64
	delay     atomic.Int64
}

func (c *Synchronizer) GetOrSetStartTime(t int64) int64 {
	if c.startTime.CompareAndSwap(0, t) {
		return t
	}

	startTime := c.startTime.Load()
	c.delay.CompareAndSwap(0, t-startTime)
	return startTime
}

func (c *Synchronizer) GetStartTime() int64 {
	return c.startTime.Load()
}

func (c *Synchronizer) SetEndTime(t int64) {
	c.endTime.Store(t)
}

func (c *Synchronizer) GetEndTime() int64 {
	return c.endTime.Load()
}

func (c *Synchronizer) GetDelay() int64 {
	return c.delay.Load()
}
