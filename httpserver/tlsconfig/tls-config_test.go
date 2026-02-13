package tlsconfig

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_NoTLSConfigured(t *testing.T) {
	tlsConfig, watchFn, err := Load("", "", "")
	require.NoError(t, err)
	assert.Nil(t, tlsConfig)
	assert.Nil(t, watchFn)
}

func TestLoad_PartialPEMConfigDisablesTLS(t *testing.T) {
	validCertPEM, validKeyPEM := generateECDSACertPairPEM(t)

	testCases := []struct {
		name    string
		certPEM string
		keyPEM  string
	}{
		{
			name:    "missing cert",
			certPEM: "",
			keyPEM:  validKeyPEM,
		},
		{
			name:    "missing key",
			certPEM: validCertPEM,
			keyPEM:  "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tlsConfig, watchFn, err := Load("", tc.certPEM, tc.keyPEM)
			require.NoError(t, err)
			assert.Nil(t, tlsConfig)
			assert.Nil(t, watchFn)
		})
	}
}

func TestLoad_InvalidPEMReturnsError(t *testing.T) {
	tlsConfig, watchFn, err := Load("", "invalid cert", "invalid key")
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to parse TLS certificate or key")
	assert.Nil(t, tlsConfig)
	assert.Nil(t, watchFn)
}

func TestLoad_ValidPEM(t *testing.T) {
	certPEM, keyPEM := generateECDSACertPairPEM(t)

	tlsConfig, watchFn, err := Load("", certPEM, keyPEM)
	require.NoError(t, err)
	require.NotNil(t, tlsConfig)
	assert.Equal(t, uint16(minTLSVersion), tlsConfig.MinVersion)
	assert.Nil(t, watchFn)
	require.Len(t, tlsConfig.Certificates, 1)
	assert.NotEmpty(t, tlsConfig.Certificates[0].Certificate)
}

func TestLoad_ValidPEMTakesPrecedenceOverPath(t *testing.T) {
	// Create an invalid pair on disk; PEM values should still be used.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, tlsCertFile), []byte("invalid cert"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, tlsKeyFile), []byte("invalid key"), 0o600))

	certPEM, keyPEM := generateECDSACertPairPEM(t)
	tlsConfig, watchFn, err := Load(dir, certPEM, keyPEM)
	require.NoError(t, err)
	require.NotNil(t, tlsConfig)
	assert.Nil(t, watchFn)
	require.Len(t, tlsConfig.Certificates, 1)
}

func TestLoad_FromPath_MissingFilesDisablesTLS(t *testing.T) {
	testCases := []struct {
		name        string
		writeCert   bool
		writeKey    bool
		expectTLS   bool
		expectError bool
	}{
		{
			name:      "both files missing",
			writeCert: false,
			writeKey:  false,
		},
		{
			name:      "missing key",
			writeCert: true,
			writeKey:  false,
		},
		{
			name:      "missing cert",
			writeCert: false,
			writeKey:  true,
		},
	}

	certPEM, keyPEM := generateECDSACertPairPEM(t)

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()

			if tc.writeCert {
				require.NoError(t, os.WriteFile(filepath.Join(dir, tlsCertFile), []byte(certPEM), 0o600))
			}
			if tc.writeKey {
				require.NoError(t, os.WriteFile(filepath.Join(dir, tlsKeyFile), []byte(keyPEM), 0o600))
			}

			tlsConfig, watchFn, err := Load(dir, "", "")
			require.NoError(t, err)
			assert.Nil(t, tlsConfig)
			assert.Nil(t, watchFn)
		})
	}
}

func TestLoad_FromPath_InvalidFilesReturnsError(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, tlsCertFile), []byte("invalid cert"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, tlsKeyFile), []byte("invalid key"), 0o600))

	tlsConfig, watchFn, err := Load(dir, "", "")
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to load TLS certificates from path")
	assert.Nil(t, tlsConfig)
	assert.Nil(t, watchFn)
}

func TestLoad_FromPath_ValidFiles(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := generateECDSACertPairPEM(t)

	require.NoError(t, os.WriteFile(filepath.Join(dir, tlsCertFile), []byte(certPEM), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, tlsKeyFile), []byte(keyPEM), 0o600))

	tlsConfig, watchFn, err := Load(dir, "", "")
	require.NoError(t, err)
	require.NotNil(t, tlsConfig)
	require.NotNil(t, watchFn)
	assert.Equal(t, uint16(minTLSVersion), tlsConfig.MinVersion)
	assert.Nil(t, tlsConfig.Certificates)
	require.NotNil(t, tlsConfig.GetCertificate)

	cert, certErr := tlsConfig.GetCertificate(nil)
	require.NoError(t, certErr)
	require.NotNil(t, cert)
	assert.NotEmpty(t, cert.Certificate)

	loadedCert, loadErr := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	require.NoError(t, loadErr)
	require.NotEmpty(t, loadedCert.Certificate)
	assert.Equal(t, loadedCert.Certificate[0], cert.Certificate[0])
}

func TestLoad_FromPath_WatchReloadsCertificates(t *testing.T) {
	dir := t.TempDir()
	initialCertPEM, initialKeyPEM := generateECDSACertPairPEM(t)
	require.NoError(t, writeCertPairToDisk(dir, initialCertPEM, initialKeyPEM))

	tlsConfig, watchFn, err := Load(dir, "", "")
	require.NoError(t, err)
	require.NotNil(t, tlsConfig)
	require.NotNil(t, watchFn)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	require.NoError(t, watchFn(ctx))

	initialDER := currentLeafDER(t, tlsConfig)

	updatedCertPEM, updatedKeyPEM := generateECDSACertPairPEM(t)
	updatedCert, err := tls.X509KeyPair([]byte(updatedCertPEM), []byte(updatedKeyPEM))
	require.NoError(t, err)
	require.NotEmpty(t, updatedCert.Certificate)

	// Write cert and key separately; one of the intermediate reloads may fail due to mismatch,
	// but a subsequent event after both files are updated should succeed.
	require.NoError(t, os.WriteFile(filepath.Join(dir, tlsCertFile), []byte(updatedCertPEM), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, tlsKeyFile), []byte(updatedKeyPEM), 0o600))

	var reloadedDER []byte
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		current, readErr := currentLeafDERRaw(tlsConfig)
		if !assert.NoError(c, readErr) {
			return
		}
		if !assert.False(c, bytes.Equal(current, initialDER), "certificate should eventually reload") {
			return
		}
		reloadedDER = current
	}, 6*time.Second, 50*time.Millisecond)
	assert.Equal(t, updatedCert.Certificate[0], reloadedDER)
}

func TestLoad_FromPath_WatchKeepsPreviousCertOnInvalidUpdate(t *testing.T) {
	dir := t.TempDir()
	initialCertPEM, initialKeyPEM := generateECDSACertPairPEM(t)
	require.NoError(t, writeCertPairToDisk(dir, initialCertPEM, initialKeyPEM))

	tlsConfig, watchFn, err := Load(dir, "", "")
	require.NoError(t, err)
	require.NotNil(t, tlsConfig)
	require.NotNil(t, watchFn)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	require.NoError(t, watchFn(ctx))

	initialDER := currentLeafDER(t, tlsConfig)

	// Trigger reload with invalid data; reload should fail and keep the previous cert in memory.
	require.NoError(t, os.WriteFile(filepath.Join(dir, tlsCertFile), []byte("invalid cert"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, tlsKeyFile), []byte("invalid key"), 0o600))

	assert.Never(t, func() bool {
		current, readErr := currentLeafDERRaw(tlsConfig)
		if readErr != nil {
			return false
		}
		return !bytes.Equal(current, initialDER)
	}, 2*time.Second, 50*time.Millisecond)
}

func writeCertPairToDisk(dir string, certPEM string, keyPEM string) error {
	err := os.WriteFile(filepath.Join(dir, tlsCertFile), []byte(certPEM), 0o600)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, tlsKeyFile), []byte(keyPEM), 0o600)
}

func currentLeafDER(t *testing.T, cfg *tls.Config) []byte {
	t.Helper()

	der, err := currentLeafDERRaw(cfg)
	require.NoError(t, err)

	return der
}

func currentLeafDERRaw(cfg *tls.Config) ([]byte, error) {
	cert, err := cfg.GetCertificate(nil)
	if err != nil {
		return nil, err
	}
	if cert == nil {
		return nil, errors.New("no certificate returned")
	}
	if len(cert.Certificate) == 0 {
		return nil, errors.New("certificate chain is empty")
	}

	return append([]byte(nil), cert.Certificate[0]...), nil
}

func generateECDSACertPairPEM(t *testing.T) (certPEM string, keyPEM string) {
	t.Helper()

	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "localhost",
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &privKey.PublicKey, privKey)
	require.NoError(t, err)

	certBlock := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	require.NotNil(t, certBlock)

	keyBytes, err := x509.MarshalECPrivateKey(privKey)
	require.NoError(t, err)

	keyBlock := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	require.NotNil(t, keyBlock)

	return string(certBlock), string(keyBlock)
}
