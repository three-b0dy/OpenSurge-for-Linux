package main

import "testing"

func TestParseSetupArgsReplaceCertificate(t *testing.T) {
	options, err := parseSetupArgs([]string{"replace-certificate", "--cert", "/tmp/cert.pem", "--key", "/tmp/key.pem"})
	if err != nil {
		t.Fatal(err)
	}
	if options.command != "replace-certificate" || options.certPath != "/tmp/cert.pem" || options.keyPath != "/tmp/key.pem" {
		t.Fatalf("parsed options = %#v", options)
	}
}

func TestParseSetupArgsRequiresCertificatePair(t *testing.T) {
	if _, err := parseSetupArgs([]string{"replace-certificate", "--cert", "/tmp/cert.pem"}); err == nil {
		t.Fatal("parseSetupArgs() accepted a missing private key")
	}
}

func TestManagedTLSPathRejectsOutsidePrivateKeyLocation(t *testing.T) {
	if _, err := managedTLSPath("/tmp/opensurge-key.pem"); err == nil {
		t.Fatal("managedTLSPath() accepted a private key outside the managed directory")
	}
}
