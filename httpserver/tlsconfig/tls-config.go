package tlsconfig

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	fskit "github.com/italypaleale/go-kit/fs"
	"github.com/italypaleale/go-kit/fsnotify"
)

const (
	tlsCertFile   = "tls-cert.pem"
	tlsKeyFile    = "tls-key.pem"
	minTLSVersion = tls.VersionTLS12
)

// Load a TLS configuration
// First, checks if tlsKeyPEM and tlsCertPEM are valid TLS certificates
// If not, tries to load from a path on disk files named "tls-cert.pem" and "tls-key.pem"
func Load(tlsPath string, tlsCertPEM string, tlsKeyPEM string) (tlsConfig *tls.Config, watchFn CertWatchFn, err error) {
	tlsConfig = &tls.Config{
		MinVersion: minTLSVersion,
	}

	// Let's set the server cert and key now
	// First, check if we have actual keys
	tlsCert := tlsCertPEM
	tlsKey := tlsKeyPEM

	// If we don't have actual keys, then we need to load from file and reload when the files change
	if tlsCert == "" && tlsKey == "" {
		if tlsPath == "" {
			// No config file loaded, so don't attempt to load TLS certs
			return nil, nil, nil
		}

		var provider *tlsCertProvider
		provider, err = newTLSCertProvider(tlsPath)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to load TLS certificates from path '%s': %w", tlsPath, err)
		}

		// If newTLSCertProvider returns nil, there are no TLS certificates, so disable TLS
		if provider == nil {
			return nil, nil, nil
		}

		slog.Debug("Loaded TLS certificates from disk", "path", tlsPath)

		tlsConfig.GetCertificate = provider.GetCertificateFn()

		return tlsConfig, provider.Watch, nil
	}

	// Assume the values from the config file are PEM-encoded certs and key
	if tlsCert == "" || tlsKey == "" {
		// If tlsCert and/or tlsKey is empty, do not use TLS
		return nil, nil, nil
	}

	cert, err := tls.X509KeyPair([]byte(tlsCert), []byte(tlsKey))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse TLS certificate or key: %w", err)
	}
	tlsConfig.Certificates = []tls.Certificate{cert}

	slog.Debug("Loaded TLS certificates from PEM values")

	return tlsConfig, nil, nil
}

type CertWatchFn = func(ctx context.Context) error

type tlsCertProvider struct {
	lock    sync.RWMutex
	tlsCert *tls.Certificate
	path    string
	cert    string
	key     string
}

// Creates a new tlsCertProvider object
// If we cannot find a TLS certificates, the returned object will be nil
func newTLSCertProvider(path string) (*tlsCertProvider, error) {
	var exists bool

	// Check if the certificate and key exist
	cert := filepath.Join(path, tlsCertFile)
	if exists, _ = fskit.FileExists(cert); !exists {
		//nolint:nilnil
		return nil, nil
	}
	key := filepath.Join(path, tlsKeyFile)
	if exists, _ = fskit.FileExists(key); !exists {
		//nolint:nilnil
		return nil, nil
	}

	// Load the certificates initially
	tlsCert, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		return nil, fmt.Errorf("failed to read key pair: %w", err)
	}

	return &tlsCertProvider{
		tlsCert: &tlsCert,
		path:    path,
		cert:    cert,
		key:     key,
	}, nil
}

// GetCertificateFn returns a function that can be used as the GetCertificate property in a tls.Config object.
func (p *tlsCertProvider) GetCertificateFn() func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return func(clientHello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		p.lock.RLock()
		defer p.lock.RUnlock()

		return p.tlsCert, nil
	}
}

// Reload the certificate from disk.
func (p *tlsCertProvider) Reload() error {
	tlsCert, err := tls.LoadX509KeyPair(p.cert, p.key)
	if err != nil {
		return fmt.Errorf("failed to read key pair: %w", err)
	}

	p.SetTLSCert(&tlsCert)

	return nil
}

// SetTLSCert updates the TLS certificate object.
func (p *tlsCertProvider) SetTLSCert(tlsCert *tls.Certificate) {
	p.lock.Lock()
	p.tlsCert = tlsCert
	p.lock.Unlock()
}

// Watch starts watching (in background) for changes to the TLS certificate and key on disk, and triggers a reload when that happens.
func (p *tlsCertProvider) Watch(ctx context.Context) error {
	watcher, err := fsnotify.WatchFolder(ctx, p.path)
	if err != nil {
		return fmt.Errorf("failed to start watching for changes on disk: %w", err)
	}

	// Start the background watcher
	go func() {
		var reloadErr error
		for {
			select {
			case <-watcher:
				// Reload
				slog.InfoContext(ctx, "Found changes in folder containing TLS certificates; will reload certificates")
				reloadErr = p.Reload()
				if reloadErr != nil {
					// Log errors only
					slog.ErrorContext(ctx, "Failed to load updated TLS certificates from disk", slog.Any("error", reloadErr))
					continue
				}
				slog.InfoContext(ctx, "TLS certificates have been reloaded")

			case <-ctx.Done():
				// Stop on context cancellation
				return
			}
		}
	}()

	return nil
}
