package resolver

import (
	"context"
	"net"
	"sync"

	"github.com/vikranthBala/cartographer/internal/collector"
)

// todo: Add support to invalidate, and clear cache
type Resolver struct {
	cache sync.Map
}

func (r *Resolver) lookup(addr string) string {

	// first look in the cache
	v, ok := r.cache.Load(addr)
	if ok {
		return v.(string)
	}

	names, err := net.LookupAddr(addr)
	if err != nil {
		return ""
	}
	r.cache.Store(addr, names[0])
	return names[0]
}

func (r *Resolver) Resolve(ctx context.Context, in <-chan collector.Conn, out chan<- collector.EnrichedConn) {
	sem := make(chan struct{}, 20) // 20 concurrent lookups
	for conn := range in {
		select {
		case <-ctx.Done():
			return
		default:
		}
		sem <- struct{}{}
		go func(c collector.Conn) {
			defer func() { <-sem }()
			ec := collector.EnrichedConn{
				Conn: c,
			}
			if c.RemoteAddr != nil {
				ec.RemoteHost = r.lookup(c.RemoteAddr.String())
			}
			select {
			case out <- ec:
			case <-ctx.Done():
			}
		}(conn)
	}
	// drain semaphore before closing
	for i := 0; i < cap(sem); i++ {
		sem <- struct{}{}
	}
	close(out)
}
