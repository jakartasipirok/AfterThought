package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jakartasipirok/AfterThought/internal/storage"
	"github.com/jakartasipirok/AfterThought/internal/webapi"
	"go.temporal.io/sdk/client"
)

func serve(db *storage.DB, files *storage.Files, c client.Client, queue, modelName, addr, uiDir string, withWorker bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || (host != "localhost" && !net.ParseIP(host).IsLoopback()) {
		return fmt.Errorf("serve requires a loopback address, for example 127.0.0.1:8080")
	}
	root, err := os.OpenRoot(uiDir)
	if err != nil {
		return fmt.Errorf("open frontend: %w", err)
	}
	defer root.Close()
	if info, err := fs.Stat(root.FS(), "index.html"); err != nil || info.IsDir() {
		return fmt.Errorf("frontend directory must contain index.html")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err = db.Migrate(startup); err != nil {
		return err
	}
	h, err := memoryConfig()
	if err != nil {
		return err
	}
	p, err := newPipeline(startup, db, files)
	if err != nil {
		return err
	}
	p.Temporal = c
	if withWorker {
		w, err := startWorker(startup, db, files, c, queue, modelName)
		if err != nil {
			return err
		}
		defer w.Stop()
		go reapContainers(ctx, p, queue)
	}
	api := &webapi.Server{DB: db, Files: files, Temporal: c, Queue: queue, Hindsight: h, Model: modelName, Pipeline: p}
	mux := http.NewServeMux()
	mux.Handle("/api/", api.Handler())
	mux.Handle("/", staticFrontend(root.FS()))
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 100 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			server.Shutdown(shutdown)
		case <-stopped:
		}
	}()
	fmt.Printf("AfterThought demo: http://%s\n", listener.Addr())
	fmt.Printf("Agent model: %s; worker enabled: %t\n", modelName, withWorker)
	if err = server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Serve only frontend assets, with no directory listings or hidden files.
// The root filesystem also prevents symlinks escaping the frontend directory.
func staticFrontend(root fs.FS) http.Handler {
	files := http.FileServerFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		for _, part := range strings.Split(name, "/") {
			if strings.HasPrefix(part, ".") {
				http.NotFound(w, r)
				return
			}
		}
		info, err := fs.Stat(root, name)
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}
