package easyshift

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"
)

// HTTPFileServer is the real FileServer implementation backed by net/http.
type HTTPFileServer struct {
	rootDir string
	port    int
	host    string
	server  *http.Server
}

// NewHTTPFileServer returns a FileServer rooted at rootDir, listening on :port.
// host is used to construct BaseURL() (callers usually pass the host's primary IP).
func NewHTTPFileServer(rootDir, host string, port int) (*HTTPFileServer, error) {
	if err := os.MkdirAll(rootDir, 0o700); err != nil {
		return nil, fmt.Errorf("create HTTP root: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir(rootDir)))
	return &HTTPFileServer{
		rootDir: rootDir,
		port:    port,
		host:    host,
		server: &http.Server{
			Addr:    fmt.Sprintf(":%d", port),
			Handler: mux,
		},
	}, nil
}

// Start begins serving in a background goroutine.
func (s *HTTPFileServer) Start(_ context.Context) error {
	logrus.Infof("starting HTTP file server on %s (root: %s)", s.server.Addr, s.rootDir)
	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logrus.Errorf("HTTP server: %v", err)
		}
	}()
	return nil
}

// Stop shuts down the server.
func (s *HTTPFileServer) Stop(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

// RootDir returns the directory being served.
func (s *HTTPFileServer) RootDir() string { return s.rootDir }

// BaseURL returns the URL prefix VMs should use when fetching files.
func (s *HTTPFileServer) BaseURL() string {
	return fmt.Sprintf("http://%s:%d", s.host, s.port)
}

// EnsureClusterDir creates a subdirectory under the server root for a cluster.
func (s *HTTPFileServer) EnsureClusterDir(clusterName string) (string, error) {
	dir := filepath.Join(s.rootDir, clusterName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}
