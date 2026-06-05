package dashboard

import "sync/atomic"

// connCount is an atomic gauge for in-flight SSE connections. Reads + writes
// are constant-time and safe to call from any goroutine.
type connCount struct{ n atomic.Int64 }

func (c *connCount) inc()     { c.n.Add(1) }
func (c *connCount) dec()     { c.n.Add(-1) }
func (c *connCount) get() int { return int(c.n.Load()) }
