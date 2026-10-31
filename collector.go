package main

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type counters struct{ received, untrusted, oversized, rateDropped, ignored, parsed, queueDropped, written, storageDropped atomic.Int64 }

func (c *counters) snapshot() map[string]int64 {
	return map[string]int64{"received": c.received.Load(), "untrusted": c.untrusted.Load(), "oversized": c.oversized.Load(), "rate_dropped": c.rateDropped.Load(), "ignored": c.ignored.Load(), "parsed": c.parsed.Load(), "queue_dropped": c.queueDropped.Load(), "written": c.written.Load(), "storage_dropped": c.storageDropped.Load()}
}

type Collector struct {
	store                     *Store
	settings                  *atomic.Pointer[Settings]
	counters                  counters
	started                   float64
	lastReceived, lastWritten atomic.Int64
	errMu                     sync.RWMutex
	lastError                 string
	receiverFailed            atomic.Bool
	receiverDone              chan struct{}
	queueMu                   sync.Mutex
	queue                     [8192]*Event
	head, count               int
	flush                     chan struct{}
	maintenance               chan struct{}
	conn                      *net.UDPConn
	wg                        sync.WaitGroup
	tokens                    float64
	tokenTime                 time.Time
	demo                      bool
}

func newCollector(store *Store, settings *atomic.Pointer[Settings], demo bool) *Collector {
	return &Collector{store: store, settings: settings, started: unixNow(), flush: make(chan struct{}, 1), maintenance: make(chan struct{}, 1), tokens: float64(settings.Load().IngestPerSecond), tokenTime: time.Now(), demo: demo}
}
func (c *Collector) setError(value string) { c.errMu.Lock(); c.lastError = value; c.errMu.Unlock() }
func (c *Collector) getError() string {
	if c.receiverFailed.Load() {
		return "receiver_failed"
	}
	c.errMu.RLock()
	defer c.errMu.RUnlock()
	return c.lastError
}
func (c *Collector) queueLength() int { c.queueMu.Lock(); defer c.queueMu.Unlock(); return c.count }
func (c *Collector) accept(data []byte, sender netip.Addr) {
	c.counters.received.Add(1)
	settings := c.settings.Load()
	trusted := false
	sender = sender.Unmap()
	for _, prefix := range settings.networks {
		if prefix.Contains(sender) {
			trusted = true
			break
		}
	}
	if !trusted {
		c.counters.untrusted.Add(1)
		return
	}
	if len(data) > 4096 {
		c.counters.oversized.Add(1)
		return
	}
	now := time.Now()
	rate := float64(settings.IngestPerSecond)
	c.tokens += now.Sub(c.tokenTime).Seconds() * rate
	if c.tokens > rate {
		c.tokens = rate
	}
	c.tokenTime = now
	if c.tokens < 1 {
		c.counters.rateDropped.Add(1)
		return
	}
	c.tokens--
	event := parseMessage(data, sender.String(), settings.WANMap, float64(now.UnixNano())/1e9)
	if event == nil {
		c.counters.ignored.Add(1)
		return
	}
	c.lastReceived.Store(now.UnixNano())
	c.queueMu.Lock()
	if c.count >= settings.QueueSize {
		c.queueMu.Unlock()
		c.counters.queueDropped.Add(1)
		return
	}
	c.queue[(c.head+c.count)%len(c.queue)] = event
	c.count++
	ready := c.count >= 250
	c.queueMu.Unlock()
	c.counters.parsed.Add(1)
	if ready {
		select {
		case c.flush <- struct{}{}:
		default:
		}
	}
}

func (c *Collector) pop() []Event {
	c.queueMu.Lock()
	defer c.queueMu.Unlock()
	n := min(c.count, 250)
	batch := make([]Event, 0, n)
	for range n {
		batch = append(batch, *c.queue[c.head])
		c.queue[c.head] = nil
		c.head = (c.head + 1) % len(c.queue)
		c.count--
	}
	return batch
}

func (c *Collector) start(ctx context.Context, host string, port int) error {
	address, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	c.conn, err = net.ListenUDP("udp4", address)
	if err != nil {
		return err
	}
	_ = c.conn.SetReadBuffer(1024 * 1024)
	c.receiverDone = make(chan struct{})
	c.wg.Add(2)
	go func() {
		defer c.wg.Done()
		defer close(c.receiverDone)
		buffer := make([]byte, 4097)
		for {
			n, peer, err := c.conn.ReadFromUDPAddrPort(buffer)
			if err != nil {
				if ctx.Err() == nil {
					c.receiverFailed.Store(true)
				}
				return
			}
			c.accept(buffer[:n], peer.Addr())
		}
	}()
	go c.writer(ctx)
	return nil
}

func (c *Collector) writeBatch(ctx context.Context, batch []Event) {
	if len(batch) == 0 {
		return
	}
	if !c.store.diskOK() {
		c.counters.storageDropped.Add(int64(len(batch)))
		c.setError("disk_low")
		return
	}
	if err := c.store.append(ctx, batch); err != nil {
		c.counters.storageDropped.Add(int64(len(batch)))
		c.setError("storage_failed")
		return
	}
	c.counters.written.Add(int64(len(batch)))
	c.lastWritten.Store(time.Now().UnixNano())
	c.setError("")
}

func (c *Collector) writer(ctx context.Context) {
	defer c.wg.Done()
	flush := time.NewTicker(250 * time.Millisecond)
	defer flush.Stop()
	maintenance := time.NewTicker(30 * time.Second)
	defer maintenance.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = c.conn.Close()
			<-c.receiverDone
			drain, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			for c.queueLength() > 0 && drain.Err() == nil {
				c.writeBatch(drain, c.pop())
			}
			return
		case <-flush.C:
			c.writeBatch(ctx, c.pop())
		case <-c.flush:
			c.writeBatch(ctx, c.pop())
			if c.queueLength() >= 250 {
				select {
				case c.flush <- struct{}{}:
				default:
				}
			}
		case <-maintenance.C:
			if err := c.store.maintenance(ctx); err != nil {
				c.setError("maintenance_failed")
			}
		case <-c.maintenance:
			if err := c.store.maintenance(ctx); err != nil {
				c.setError("maintenance_failed")
			}
		}
	}
}

func (c *Collector) stop() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
	c.wg.Wait()
}
func (c *Collector) status() map[string]any {
	s := c.settings.Load()
	return map[string]any{"counters": c.counters.snapshot(), "started_at": c.started, "last_received": nullableTime(c.lastReceived.Load()), "last_written": nullableTime(c.lastWritten.Load()), "queue": c.queueLength(), "error": c.getError(), "db_bytes": c.store.diskUsage(), "pruned": c.store.pruned.Load(), "sampling": s.Sampling, "demo": c.demo, "scan_ports": s.ScanPorts, "scan_window": s.ScanWindow, "retention_days": s.RetentionDays, "max_db_mb": s.MaxDBMB, "sources_configured": s.AllowedSenders != ""}
}
func nullableTime(nanos int64) any {
	if nanos == 0 {
		return nil
	}
	return float64(nanos) / 1e9
}
