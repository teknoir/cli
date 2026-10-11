package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type issued struct {
	cert *x509.Certificate
	der  []byte
	key  *rsa.PrivateKey
}

func (i issued) pem() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: i.der})
}

func ski(k *rsa.PublicKey) []byte {
	s := sha1.Sum(x509.MarshalPKCS1PublicKey(k))
	return s[:]
}

// mk creates a certificate. parent==nil makes it self-signed.
func mk(t *testing.T, cn string, ku x509.KeyUsage, eku []x509.ExtKeyUsage, parent *issued, key *rsa.PrivateKey) issued {
	t.Helper()
	if key == nil {
		var err error
		if key, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			t.Fatal(err)
		}
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              ku,
		ExtKeyUsage:           eku,
		BasicConstraintsValid: true,
		IsCA:                  true,
		SubjectKeyId:          ski(&key.PublicKey),
	}
	signer, par := key, tpl
	if parent != nil {
		signer, par = parent.key, parent.cert
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, par, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return issued{c, der, key}
}

const ca = x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment

func bundle(leaf, inter, root issued) []byte {
	return bytes.Join([][]byte{leaf.pem(), inter.pem(), root.pem()}, nil)
}

// The three root flavours that matter: what Backstage makes (no keyUsage),
// what device-controller makes (KU without keyCertSign) and a proper CA.
func TestRootFlavours(t *testing.T) {
	cases := []struct {
		name string
		ku   x509.KeyUsage
		eku  []x509.ExtKeyUsage
		ok   bool
		want string
	}{
		{"no-KU (Backstage openssl req -x509)", 0, nil, false, "lacks keyCertSign"},
		{"KU without CertSign (device-controller)", x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, false, "lacks keyCertSign"},
		{"CertSign", ca, nil, true, ""},
		{"CertSign only", x509.KeyUsageCertSign, nil, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := mk(t, "device", tc.ku, tc.eku, nil, nil)
			inter := mk(t, "inter", ca, nil, &root, nil)
			leaf := mk(t, "leaf", ca, nil, &inter, nil)
			r := CheckBundle("server-ca.crt", bundle(leaf, inter, root), time.Now())
			if r.OK() != tc.ok {
				t.Fatalf("OK=%v want %v; problems: %v", r.OK(), tc.ok, r.Problems)
			}
			if !tc.ok {
				joined := strings.Join(r.Problems, "\n")
				if !strings.Contains(joined, tc.want) {
					t.Errorf("problems %q lack %q", joined, tc.want)
				}
				// the actual k3s failure: leaf CA cannot be verified
				if !strings.Contains(joined, "verify[raw]") || !strings.Contains(joined, "verify[hash]") {
					t.Errorf("expected both verify modes to fail: %q", joined)
				}
			}
		})
	}
}

// With a single-cert bundle k3s keeps the legacy behaviour (no pruning).
func TestSingleCertBundleIsNotPruned(t *testing.T) {
	root := mk(t, "device", 0, nil, nil, nil)
	r := CheckBundle("x", root.pem(), time.Now())
	if !r.OK() {
		t.Fatalf("single-cert bundle should be accepted: %v", r.Problems)
	}
}

func TestAKIRootDetection(t *testing.T) {
	root := mk(t, "device", ca, nil, nil, nil)
	inter := mk(t, "inter", ca, nil, &root, nil)
	leaf := mk(t, "leaf", ca, nil, &inter, nil)

	// A root with AKI != SKI is an intermediate for k3s: nothing anchors the chain.
	// Go ignores template.AuthorityKeyId on self-signed certs, so add the extension by hand.
	akiVal, err := asn1.Marshal(struct {
		ID []byte `asn1:"optional,tag:0"`
	}{bytes.Repeat([]byte{9}, 20)})
	if err != nil {
		t.Fatal(err)
	}
	mod := *root.cert
	mod.Raw = nil
	mod.ExtraExtensions = []pkix.Extension{{Id: asn1.ObjectIdentifier{2, 5, 29, 35}, Value: akiVal}}
	der2, err := x509.CreateCertificate(rand.Reader, &mod, &mod, &root.key.PublicKey, root.key)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := x509.ParseCertificate(der2)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(c2.AuthorityKeyId, c2.SubjectKeyId) {
		t.Fatal("test setup: AKI == SKI")
	}
	bad := issued{c2, der2, root.key}
	r := CheckBundle("x", bundle(leaf, inter, bad), time.Now())
	if r.OK() || !strings.Contains(strings.Join(r.Problems, "\n"), "AKI != SKI") {
		t.Fatalf("expected AKI!=SKI problem, got %v", r.Problems)
	}
}

func TestChainLinkageMismatch(t *testing.T) {
	root := mk(t, "device", ca, nil, nil, nil)
	inter := mk(t, "inter", ca, nil, &root, nil)
	leaf := mk(t, "leaf", ca, nil, &inter, nil)
	otherRoot := mk(t, "another-device", ca, nil, nil, nil)
	r := CheckBundle("x", bundle(leaf, inter, otherRoot), time.Now())
	if r.OK() || !strings.Contains(strings.Join(r.Problems, "\n"), "RawIssuer") {
		t.Fatalf("expected RawIssuer problem, got %v", r.Problems)
	}
}

func TestExpiredRootIsRejected(t *testing.T) {
	root := mk(t, "device", ca, nil, nil, nil)
	inter := mk(t, "inter", ca, nil, &root, nil)
	leaf := mk(t, "leaf", ca, nil, &inter, nil)
	r := CheckBundle("x", bundle(leaf, inter, root), time.Now().AddDate(30, 0, 0))
	if r.OK() {
		t.Fatal("expired chain must be rejected")
	}
}

func TestHashCAUsesRootOfMultiCertBundle(t *testing.T) {
	root := mk(t, "device", ca, nil, nil, nil)
	inter := mk(t, "inter", ca, nil, &root, nil)
	leaf := mk(t, "leaf", ca, nil, &inter, nil)
	h, err := HashCA(bundle(leaf, inter, root))
	if err != nil {
		t.Fatal(err)
	}
	only, _ := HashCA(root.pem()) // single cert: hash of the PEM bytes, not the DER
	if h == only {
		t.Error("multi-cert hash must be over the root DER, not the PEM text")
	}
	h3, _ := HashCA(append(inter.pem(), root.pem()...))
	if h != h3 {
		t.Error("hash must be independent of the leaf position: both bundles end in the same root")
	}
}

func TestCheckRoot(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	orig := mk(t, "teknoir.ai", x509.KeyUsageDigitalSignature, nil, nil, key)
	now := time.Now()

	good := &x509.Certificate{
		SerialNumber: big.NewInt(42), Subject: orig.cert.Subject, NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(0, 0, 7300),
		KeyUsage: ca, BasicConstraintsValid: true, IsCA: true, SubjectKeyId: orig.cert.SubjectKeyId,
	}
	der, err := x509.CreateCertificate(rand.Reader, good, good, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := x509.ParseCertificate(der)
	if p := CheckRoot(root, orig.cert, key, now, 7300); len(p) != 0 {
		t.Fatalf("good root rejected: %v", p)
	}
	// the original (no CertSign) must fail
	if p := CheckRoot(orig.cert, orig.cert, key, now, 0); len(p) == 0 {
		t.Fatal("original root must fail")
	}
	// wrong key
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	if p := CheckRoot(root, orig.cert, other, now, 7300); !strings.Contains(strings.Join(p, "\n"), "does not match") {
		t.Fatalf("key mismatch not reported: %v", p)
	}
	// wrong validity
	if p := CheckRoot(root, orig.cert, key, now, 3650); !strings.Contains(strings.Join(p, "\n"), "notAfter") {
		t.Fatalf("validity not checked: %v", p)
	}
	// EKU present
	eku := *good
	eku.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	der, _ = x509.CreateCertificate(rand.Reader, &eku, &eku, &key.PublicKey, key)
	rootEKU, _ := x509.ParseCertificate(der)
	if p := CheckRoot(rootEKU, orig.cert, key, now, 7300); !strings.Contains(strings.Join(p, "\n"), "extendedKeyUsage") {
		t.Fatalf("EKU not reported: %v", p)
	}
	// subject differs
	sub := *good
	sub.Subject = pkix.Name{CommonName: "other"}
	der, _ = x509.CreateCertificate(rand.Reader, &sub, &sub, &key.PublicKey, key)
	rootSub, _ := x509.ParseCertificate(der)
	if p := CheckRoot(rootSub, orig.cert, key, now, 7300); !strings.Contains(strings.Join(p, "\n"), "subject DER") {
		t.Fatalf("subject change not reported: %v", p)
	}
}

func TestRunOnTLSDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "etcd"), 0o755); err != nil {
		t.Fatal(err)
	}
	mkdir := func(ku x509.KeyUsage) string {
		d := t.TempDir()
		_ = os.MkdirAll(filepath.Join(d, "etcd"), 0o755)
		root := mk(t, "device", ku, nil, nil, nil)
		inter := mk(t, "inter", ca, nil, &root, nil)
		for _, f := range []string{"server-ca.crt", "client-ca.crt", "etcd/peer-ca.crt"} {
			leaf := mk(t, f, ca, nil, &inter, nil)
			if err := os.WriteFile(filepath.Join(d, f), bundle(leaf, inter, root), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(d, "root-ca.pem"), root.pem(), 0o644); err != nil {
			t.Fatal(err)
		}
		return d
	}
	var out, errb bytes.Buffer
	if rc := run([]string{mkdir(ca)}, &out, &errb); rc != 0 {
		t.Fatalf("proper chain rc=%d out=%s err=%s", rc, out.String(), errb.String())
	}
	out.Reset()
	if rc := run([]string{mkdir(0)}, &out, &errb); rc != 1 || !strings.Contains(out.String(), "FAIL") {
		t.Fatalf("no-KU chain rc=%d out=%s", rc, out.String())
	}
	if rc := run([]string{"-bogus"}, &out, &errb); rc != 2 {
		t.Fatalf("bad flag rc=%d", rc)
	}
}
