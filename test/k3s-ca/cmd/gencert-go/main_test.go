package main

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"testing"
)

func parse(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	b, _ := pem.Decode(certPEM)
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLegacyReplica(t *testing.T) {
	k, c, err := Generate("sha1", false)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(c, []byte("PRIVATE KEY")) || !bytes.Contains(k, []byte("RSA PRIVATE KEY")) {
		t.Fatal("PEM blocks mixed up")
	}
	cert := parse(t, c)
	if cert.KeyUsage != x509.KeyUsageKeyEncipherment|x509.KeyUsageDigitalSignature {
		t.Errorf("KU = %#x", int(cert.KeyUsage))
	}
	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Errorf("EKU = %v", cert.ExtKeyUsage)
	}
	if !cert.IsCA || cert.SerialNumber.Int64() != 1 {
		t.Errorf("IsCA=%v serial=%v", cert.IsCA, cert.SerialNumber)
	}
	if got := cert.NotAfter.Sub(cert.NotBefore).Hours() / 24; got < 364 || got > 367 {
		t.Errorf("validity %v days", got)
	}
}

func TestSKIModes(t *testing.T) {
	var spki struct {
		Raw       asn1.RawContent
		Algorithm asn1.RawValue
		PublicKey asn1.BitString
	}
	for mode, sum := range map[string]func([]byte) []byte{
		"sha1":        func(b []byte) []byte { h := sha1.Sum(b); return h[:] },
		"sha256trunc": func(b []byte) []byte { h := sha256.Sum256(b); return h[:20] },
	} {
		_, c, err := Generate(mode, false)
		if err != nil {
			t.Fatal(err)
		}
		cert := parse(t, c)
		if _, err := asn1.Unmarshal(cert.RawSubjectPublicKeyInfo, &spki); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(cert.SubjectKeyId, sum(spki.PublicKey.Bytes)) {
			t.Errorf("%s: SKI mismatch", mode)
		}
	}
	if _, _, err := Generate("md5", false); err == nil {
		t.Error("unknown ski mode must fail")
	}
}

func TestFixedReplica(t *testing.T) {
	_, c, err := Generate("sha256trunc", true)
	if err != nil {
		t.Fatal(err)
	}
	cert := parse(t, c)
	want := x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	if cert.KeyUsage != want {
		t.Errorf("KU = %#x want %#x", int(cert.KeyUsage), int(want))
	}
	if len(cert.ExtKeyUsage) != 0 {
		t.Errorf("EKU = %v", cert.ExtKeyUsage)
	}
	if cert.SerialNumber.Int64() == 1 || cert.SerialNumber.Sign() <= 0 {
		t.Errorf("serial = %v", cert.SerialNumber)
	}
	if got := cert.NotAfter.Sub(cert.NotBefore).Hours() / 24; got < 7290 {
		t.Errorf("validity %v days", got)
	}
}
