package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	donate "github.com/TokenNotIncluded/donate.lmm.best"
	"github.com/TokenNotIncluded/donate.lmm.best/internal/app"
)

var version = "dev"

func env(key, fallback string) string {
	if s := os.Getenv(key); s != "" {
		return s
	}
	return fallback
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	flags := flag.NewFlagSet("donate", flag.ContinueOnError)
	dataDir := flags.String("data-dir", env("DONATE_DATA_DIR", "./data"), "persistent data directory")
	addr := flags.String("addr", env("DONATE_ADDR", "127.0.0.1:8080"), "HTTP listen address")
	publicURL := flags.String("public-url", env("DONATE_PUBLIC_URL", "http://localhost:8080"), "public HTTPS origin (localhost allows HTTP)")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: donate [--data-dir PATH] [--addr HOST:PORT] [--public-url URL] serve|admin password|admin reset|backup PATH|version")
		flags.PrintDefaults()
	}
	if e := flags.Parse(os.Args[1:]); e != nil {
		return e
	}
	args := flags.Args()
	if len(args) == 0 {
		args = []string{"serve"}
	}
	if args[0] == "version" {
		if len(args) != 1 {
			return fmt.Errorf("usage: donate version")
		}
		fmt.Println(version)
		return nil
	}
	if args[0] != "serve" && args[0] != "admin" && args[0] != "backup" {
		return fmt.Errorf("unknown command %q", args[0])
	}
	if args[0] == "serve" && len(args) != 1 {
		return fmt.Errorf("usage: donate serve")
	}
	if args[0] == "admin" && (len(args) != 2 || (args[1] != "password" && args[1] != "reset")) {
		return fmt.Errorf("usage: donate admin password|reset")
	}
	if args[0] == "backup" && len(args) != 2 {
		return fmt.Errorf("usage: donate backup PATH")
	}
	assets, e := fs.Sub(donate.Assets, "web")
	if e != nil {
		return e
	}
	a, e := app.New(*dataDir, *publicURL, assets)
	if e != nil {
		return e
	}
	defer a.Close()
	switch args[0] {
	case "admin":
		var password string
		if args[1] == "password" {
			password, e = a.Auth.Password()
		} else {
			password, e = a.Auth.Reset()
		}
		if e != nil {
			return e
		}
		fmt.Println(password)
		return nil
	case "backup":
		if e = a.Backup(args[1]); e != nil {
			return e
		}
		fmt.Println("backup saved:", args[1])
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	workerDone := make(chan struct{})
	go func() { a.Run(ctx); close(workerDone) }()
	server := &http.Server{Addr: *addr, Handler: a.Routes(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 * 1024}
	errCh := make(chan error, 1)
	go func() {
		log.Printf("TOKEN %s listening on %s; public origin %s", version, *addr, *publicURL)
		errCh <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
	case e = <-errCh:
		if e != nil && e != http.ErrServerClosed {
			stop()
			<-workerDone
			return e
		}
	}
	stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	e = server.Shutdown(shutdownCtx)
	<-workerDone
	return e
}
