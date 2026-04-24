package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vikranthBala/cartographer/internal/classifier"
	"github.com/vikranthBala/cartographer/internal/collector"
	"github.com/vikranthBala/cartographer/internal/graph"
	"github.com/vikranthBala/cartographer/internal/resolver"
	"github.com/vikranthBala/cartographer/internal/tui"
)

func main() {
	// Shut down cleanly on Ctrl-C or SIGTERM.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		cancel()
	}()

	// ── Pipeline ──────────────────────────────────────────────────────────────

	conns := make(chan collector.Conn)
	resolved := make(chan collector.EnrichedConn)
	classified := make(chan collector.EnrichedConn)

	// Stage 1: collect.
	c := new(collector.LsofCollector)
	go func() {
		if err := c.Stream(ctx, conns); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "collector error: %v\n", err)
		}
		close(conns)
	}()

	// Stage 2: resolve.
	r := new(resolver.Resolver)
	go r.Resolve(ctx, conns, resolved)

	// Stage 3: classify.
	cls, err := classifier.NewClassifier()
	if err != nil {
		log.Fatalf("classifier init: %v", err)
	}
	go cls.Classify(ctx, resolved, classified)

	// Stage 4: store — owns the live node graph.
	store := graph.NewStore(30 * time.Second)
	go store.Run(ctx, classified)

	// Stage 5: tui — blocks until the user quits.
	if err := tui.Run(store); err != nil {
		log.Fatalf("tui error: %v", err)
	}
}
