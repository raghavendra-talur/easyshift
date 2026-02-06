package easyshift

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"
)

// HTTPServer handles serving installation files
type HTTPServer struct {
	server  *http.Server
	RootDir string
}

// NewHTTPServer creates a new HTTP server
func NewHTTPServer() *HTTPServer {
	rootDir := filepath.Join(GetConfig().ConfigDir, "http")
	if err := os.MkdirAll(rootDir, 0700); err != nil {
		logrus.Fatalf("Failed to create HTTP root directory: %v", err)
	}

	hs := &HTTPServer{
		RootDir: rootDir,
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir(rootDir)))

	hs.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", GetConfig().WebPort),
		Handler: mux,
	}

	return hs
}

// Start starts the HTTP server
func (hs *HTTPServer) Start() error {
	logrus.Infof("Starting HTTP server on port %d", GetConfig().WebPort)
	go func() {
		if err := hs.server.ListenAndServe(); err != http.ErrServerClosed {
			logrus.Errorf("HTTP server error: %v", err)
		}
	}()
	return nil
}

// Stop stops the HTTP server
func (hs *HTTPServer) Stop() error {
	logrus.Info("Stopping HTTP server")
	return hs.server.Shutdown(context.Background())
}

// CreateClusterDir creates a directory for cluster files
func (hs *HTTPServer) CreateClusterDir(clusterName string) error {
	dir := filepath.Join(hs.RootDir, clusterName)
	return os.MkdirAll(dir, 0700)
}

// DeleteClusterDir deletes a cluster's directory
func (hs *HTTPServer) DeleteClusterDir(clusterName string) error {
	dir := filepath.Join(hs.RootDir, clusterName)
	return os.RemoveAll(dir)
}
