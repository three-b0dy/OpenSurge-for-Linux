package controlapi

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/three-b0dy/OpenSurge-for-Linux/internal/config"
)

func TestEnsureSelfSignedUsesTenYearRSA3072Certificate(t *testing.T) {
	certPath, keyPath := tempTLSPaths(t)
	now := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	listenerIP := net.ParseIP("192.168.50.1")
	if err := EnsureSelfSigned(certPath, keyPath, []net.IP{listenerIP}, now); err != nil {
		t.Fatal(err)
	}

	cert := loadCertificate(t, certPath)
	if cert.PublicKeyAlgorithm != x509.RSA {
		t.Fatalf("public key algorithm = %v", cert.PublicKeyAlgorithm)
	}
	if cert.PublicKey.(*rsa.PublicKey).Size() != 3072/8 {
		t.Fatalf("RSA key size = %d bits", cert.PublicKey.(*rsa.PublicKey).Size()*8)
	}
	if cert.NotAfter.Sub(now) < 3650*24*time.Hour {
		t.Fatalf("certificate validity = %s", cert.NotAfter.Sub(now))
	}
	if !cert.NotBefore.Before(now) || !cert.NotAfter.After(now) {
		t.Fatalf("certificate validity window = %s to %s", cert.NotBefore, cert.NotAfter)
	}
	if len(cert.IPAddresses) != 1 || !cert.IPAddresses[0].Equal(listenerIP) {
		t.Fatalf("certificate IP SANs = %v", cert.IPAddresses)
	}
	assertFileMode(t, certPath, 0o640)
	assertFileMode(t, keyPath, 0o640)
}

func TestValidateKeyPairRejectsMismatchedPrivateKey(t *testing.T) {
	certPath, keyPath := tempTLSPaths(t)
	if err := EnsureSelfSigned(certPath, keyPath, []net.IP{net.ParseIP("192.168.50.1")}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	otherKey, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		t.Fatal(err)
	}
	otherKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(otherKey)})
	if err := os.WriteFile(keyPath, otherKeyPEM, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := ValidateKeyPair(certPath, keyPath); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("ValidateKeyPair() error = %v", err)
	}
}

func TestReplaceCertificateRejectsInvalidPairWithoutChangingFiles(t *testing.T) {
	certPath, keyPath := tempTLSPaths(t)
	if err := EnsureSelfSigned(certPath, keyPath, []net.IP{net.ParseIP("192.168.50.1")}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	originalCert, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	originalKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ReplaceCertificate(certPath, keyPath, []byte("not a certificate"), []byte("not a key")); err == nil {
		t.Fatal("ReplaceCertificate() accepted invalid PEM")
	}
	if current, _ := os.ReadFile(certPath); string(current) != string(originalCert) {
		t.Fatal("invalid replacement changed the certificate")
	}
	if current, _ := os.ReadFile(keyPath); string(current) != string(originalKey) {
		t.Fatal("invalid replacement changed the private key")
	}
}

func TestReplaceCertificateWritesValidatedPair(t *testing.T) {
	originalCert, originalKey := tempTLSPaths(t)
	replacementCert, replacementKey := tempTLSPaths(t)
	if err := EnsureSelfSigned(originalCert, originalKey, []net.IP{net.ParseIP("192.168.50.1")}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSelfSigned(replacementCert, replacementKey, []net.IP{net.ParseIP("192.168.50.2")}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	certPEM, _ := os.ReadFile(replacementCert)
	keyPEM, _ := os.ReadFile(replacementKey)
	if err := ReplaceCertificate(originalCert, originalKey, certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
	if err := ValidateKeyPair(originalCert, originalKey); err != nil {
		t.Fatalf("replaced pair is invalid: %v", err)
	}
	if cert := loadCertificate(t, originalCert); len(cert.IPAddresses) != 1 || !cert.IPAddresses[0].Equal(net.ParseIP("192.168.50.2")) {
		t.Fatalf("replacement SANs = %v", cert.IPAddresses)
	}
}

func TestNewRejectsWildcardAndLoopbackManagementListeners(t *testing.T) {
	for _, address := range []string{"0.0.0.0:61767", "127.0.0.1:61767"} {
		_, err := New(Options{ConfigPath: filepath.Join(t.TempDir(), "config.yaml"), Addr: address, StoreDir: t.TempDir()})
		if err == nil || !strings.Contains(err.Error(), "management listener") {
			t.Fatalf("New(%q) error = %v", address, err)
		}
	}
}

func TestNewUsesConfiguredManagementListenWhenAddressIsOmitted(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	cfg := config.Default()
	cfg.Management.Listen = "192.168.50.1:61767"
	if err := os.WriteFile(configPath, []byte(config.Render(cfg)), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{ConfigPath: configPath, StoreDir: filepath.Join(dir, "store")})
	if err != nil {
		t.Fatal(err)
	}
	if server.URL() != "https://192.168.50.1:61767" {
		t.Fatalf("server URL = %q", server.URL())
	}
}

func TestManagementSecurityHeadersRequireExactHostAndAdvertiseHTTPS(t *testing.T) {
	server, err := New(Options{ConfigPath: filepath.Join(t.TempDir(), "config.yaml"), Addr: "192.168.50.1:61767", StoreDir: t.TempDir(), Static: http.NotFoundHandler()})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "https://192.168.50.1:61767/api/v1/auth/status", nil)
	request.Host = "192.168.50.1:61767"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("valid management host status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Strict-Transport-Security") == "" || response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("security headers = %#v", response.Header())
	}

	request.Host = "192.168.50.2:61767"
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "invalid_host") {
		t.Fatalf("wrong management host status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestTLSConfigForServeLoadsValidatedManagementCertificate(t *testing.T) {
	certPath, keyPath := tempTLSPaths(t)
	if err := EnsureSelfSigned(certPath, keyPath, []net.IP{net.ParseIP("192.168.50.1")}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{
		ConfigPath:  filepath.Join(t.TempDir(), "config.yaml"),
		Addr:        "192.168.50.1:61767",
		StoreDir:    t.TempDir(),
		TLSCertFile: certPath,
		TLSKeyFile:  keyPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err := server.tlsConfigForServe()
	if err != nil {
		t.Fatal(err)
	}
	if config.MinVersion != tls.VersionTLS12 || len(config.Certificates) != 1 {
		t.Fatalf("TLS config = %#v", config)
	}
}

func TestValidateCertificateForHostRequiresListenerSAN(t *testing.T) {
	certPath, keyPath := tempTLSPaths(t)
	if err := EnsureSelfSigned(certPath, keyPath, []net.IP{net.ParseIP("192.168.50.2")}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := ValidateKeyPairForHost(certPath, keyPath, "192.168.50.1"); err == nil || !strings.Contains(err.Error(), "SAN") {
		t.Fatalf("ValidateKeyPairForHost() error = %v", err)
	}
}

func tempTLSPaths(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
}

func loadCertificate(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("certificate PEM missing")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s mode = %o, want %o", path, info.Mode().Perm(), want)
	}
}
