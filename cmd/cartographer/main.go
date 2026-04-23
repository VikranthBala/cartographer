package main

import (
	"context"
	"fmt"
	"time"

	"github.com/vikranthBala/cartographer/internal/collector"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := new(collector.LsofCollector)

	conns := make(chan collector.Conn)

	go func() {
		if err := c.Stream(ctx, conns); err != nil {
			fmt.Printf("stream error: %v\n", err)
		}
		close(conns)
	}()

	// Stop after some time (so it doesn't run forever)
	go func() {
		time.Sleep(10 * time.Second)
		cancel()
	}()

	for conn := range conns {
		if conn.IsListen() {
			continue
		}

		fmt.Printf("%s %s:%d -> %s:%d [%s]\n",
			conn.Protocol,
			conn.LocalAddr, conn.LocalPort,
			conn.RemoteAddr, conn.RemotePort,
			conn.State,
		)
	}
}
