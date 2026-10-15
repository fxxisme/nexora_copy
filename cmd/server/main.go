package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"chatgpt2api-go/internal/app"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	srv, err := app.NewServer(".")
	if err != nil {
		return err
	}
	defer app.CloseSentinelSolvers()
	addr := os.Getenv("CHATGPT2API_ADDR")
	if addr == "" {
		addr = ":3000"
	}
	server := &http.Server{Addr: addr, Handler: srv.Handler()}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("server shutdown: %v", err)
		}
	}()
	log.Printf("chatgpt2api-go listening on %s", addr)
	err = server.ListenAndServe()
	if !errors.Is(err, http.ErrServerClosed) {
		stop()
		<-shutdownDone
		return err
	}
	return nil
}
