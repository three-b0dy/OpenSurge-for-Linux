package controlapi

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultTLSCertFile = "/etc/opensurge/tls/cert.pem"
	DefaultTLSKeyFile  = "/etc/opensurge/tls/key.pem"
	tlsDirectoryMode   = 0o750
	tlsFileMode        = 0o640
	tlsRSAKeyBits      = 3072
	tlsValidity        = 3650 * 24 * time.Hour
)

// EnsureSelfSigned creates a server certificate for the supplied management
// addresses. It validates the complete pair before changing either target.
func EnsureSelfSigned(certPath, keyPath string, ips []net.IP, now time.Time) error {
	if strings.TrimSpace(certPath) == "" || strings.TrimSpace(keyPath) == "" {
		return errors.New("TLS certificate and key paths are required")
	}
	if len(ips) == 0 {
		return errors.New("at least one management IP is required for the certificate SAN")
	}
	normalizedIPs := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		if ip == nil || ip.To4() == nil {
			return errors.New("management certificate SANs must be IPv4 addresses")
		}
		normalizedIPs = append(normalizedIPs, append(net.IP(nil), ip.To4()...))
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	notBefore := now.Add(-5 * time.Minute).Truncate(time.Second)
	notAfter := now.Add(tlsValidity + time.Second).Truncate(time.Second)
	key, err := rsa.GenerateKey(rand.Reader, tlsRSAKeyBits)
	if err != nil {
		return fmt.Errorf("generate management TLS key: %w", err)
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return fmt.Errorf("generate management TLS serial: %w", err)
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "OpenSurge Control Plane"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           normalizedIPs,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create management TLS certificate: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if _, err := validateCertificatePairPEM(certPEM, keyPEM, now); err != nil {
		return fmt.Errorf("validate generated management TLS pair: %w", err)
	}
	return writeTLSFiles(certPath, keyPath, certPEM, keyPEM)
}

// ValidateKeyPair validates PEM parsing, certificate validity, SAN presence,
// and public-key matching without modifying either file.
func ValidateKeyPair(certPath, keyPath string) error {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("read TLS certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("read TLS private key: %w", err)
	}
	_, err = validateCertificatePairPEM(certPEM, keyPEM, time.Now().UTC())
	return err
}

// ValidateKeyPairForHost additionally requires the certificate SAN to cover
// the configured management host.
func ValidateKeyPairForHost(certPath, keyPath, host string) error {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("read TLS certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("read TLS private key: %w", err)
	}
	_, err = ValidateCertificatePairForHost(certPEM, keyPEM, host)
	return err
}

// ValidateCertificatePairForHost validates replacement bytes before they are
// installed and checks the exact listener host SAN.
func ValidateCertificatePairForHost(certPEM, keyPEM []byte, host string) (*x509.Certificate, error) {
	cert, err := validateCertificatePairPEM(certPEM, keyPEM, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if err := cert.VerifyHostname(strings.TrimSpace(host)); err != nil {
		return nil, fmt.Errorf("certificate SAN does not cover management listener %q: %w", host, err)
	}
	return cert, nil
}

// ReplaceCertificate validates a complete replacement pair before atomically
// writing the managed certificate and key files. Invalid input is rejected
// without touching the existing pair.
func ReplaceCertificate(certPath, keyPath string, certPEM, keyPEM []byte) error {
	if _, err := validateCertificatePairPEM(certPEM, keyPEM, time.Now().UTC()); err != nil {
		return fmt.Errorf("validate replacement TLS pair: %w", err)
	}
	return writeTLSFiles(certPath, keyPath, certPEM, keyPEM)
}

func validateCertificatePairPEM(certPEM, keyPEM []byte, now time.Time) (*x509.Certificate, error) {
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		return nil, errors.New("certificate PEM block is missing")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	if len(cert.IPAddresses) == 0 && len(cert.DNSNames) == 0 {
		return nil, errors.New("certificate SAN is missing")
	}
	if now.Before(cert.NotBefore) {
		return nil, errors.New("certificate is not yet valid")
	}
	if !now.Before(cert.NotAfter) {
		return nil, errors.New("certificate has expired")
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	signer, ok := pair.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, errors.New("private key is not a signing key")
	}
	certificatePublic, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("marshal certificate public key: %w", err)
	}
	privatePublic, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return nil, fmt.Errorf("marshal private key public key: %w", err)
	}
	if !bytes.Equal(certificatePublic, privatePublic) {
		return nil, errors.New("certificate and private key public keys do not match")
	}
	return cert, nil
}

func writeTLSFiles(certPath, keyPath string, certPEM, keyPEM []byte) error {
	certPath, err := filepath.Abs(certPath)
	if err != nil {
		return fmt.Errorf("resolve TLS certificate path: %w", err)
	}
	keyPath, err = filepath.Abs(keyPath)
	if err != nil {
		return fmt.Errorf("resolve TLS key path: %w", err)
	}
	if certPath == keyPath {
		return errors.New("TLS certificate and key paths must differ")
	}
	if err := ensureTLSDirectory(filepath.Dir(certPath)); err != nil {
		return err
	}
	if err := ensureTLSDirectory(filepath.Dir(keyPath)); err != nil {
		return err
	}
	for _, path := range []string{certPath, keyPath} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect TLS target %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("TLS target %s must not be a symbolic link", path)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("TLS target %s must be a regular file", path)
		}
	}
	certTemp, err := writeTLSTemp(filepath.Dir(certPath), certPEM)
	if err != nil {
		return fmt.Errorf("stage TLS certificate: %w", err)
	}
	defer os.Remove(certTemp)
	keyTemp, err := writeTLSTemp(filepath.Dir(keyPath), keyPEM)
	if err != nil {
		return fmt.Errorf("stage TLS private key: %w", err)
	}
	defer os.Remove(keyTemp)
	if err := os.Rename(certTemp, certPath); err != nil {
		return fmt.Errorf("install TLS certificate: %w", err)
	}
	if err := os.Rename(keyTemp, keyPath); err != nil {
		return fmt.Errorf("install TLS private key: %w", err)
	}
	return nil
}

func ensureTLSDirectory(path string) error {
	if err := os.MkdirAll(path, tlsDirectoryMode); err != nil {
		return fmt.Errorf("create managed TLS directory: %w", err)
	}
	if err := os.Chmod(path, tlsDirectoryMode); err != nil {
		return fmt.Errorf("restrict managed TLS directory: %w", err)
	}
	return nil
}

func writeTLSTemp(dir string, data []byte) (string, error) {
	tmp, err := os.CreateTemp(dir, ".opensurge-tls-*")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(path)
		}
	}()
	if err := tmp.Chmod(tlsFileMode); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	remove = false
	return path, nil
}

func (s *Server) tlsConfigForServe() (*tls.Config, error) {
	if err := ValidateKeyPairForHost(s.tlsCertFile, s.tlsKeyFile, s.managementHost); err != nil {
		return nil, fmt.Errorf("validate management TLS pair: %w", err)
	}
	certificate, err := tls.LoadX509KeyPair(s.tlsCertFile, s.tlsKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load management TLS pair: %w", err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}, nil
}
