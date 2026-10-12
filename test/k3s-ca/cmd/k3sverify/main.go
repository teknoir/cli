// Command k3sverify predicts whether k3s >= v1.34.12 / v1.35.9 / v1.36.5 /
// v1.37.1 accepts the CA bundles of a custom-CA data dir, and checks that a
// re-issued Teknoir root CA has the properties we promise.
//
// It is a replica of the CA bundle handling in k3s pkg/clientaccess/token.go
// (v1.36.5): appendCertsFromPEM prunes every certificate without the
// KeyUsageCertSign bit before building the trust pool, hashCA picks the root
// of a multi-certificate bundle. Those two functions are
//
//	Copyright The K3s Authors.
//	Licensed under the Apache License, Version 2.0
//	https://github.com/k3s-io/k3s/blob/v1.36.5%2Bk3s1/pkg/clientaccess/token.go
//
// and are reproduced here, with the helper dependencies replaced by the Go
// standard library, for testing purposes only.
package main

import (
	"bytes"
	"crypto"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// ParseCertsPEM parses every CERTIFICATE block of a PEM bundle. It fails if
// there is none (like certutil.ParseCertsPEM).
func ParseCertsPEM(b []byte) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	for len(b) > 0 {
		var blk *pem.Block
		blk, b = pem.Decode(b)
		if blk == nil {
			break
		}
		if blk.Type != "CERTIFICATE" || len(blk.Headers) != 0 {
			continue
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			return nil, err
		}
		certs = append(certs, c)
	}
	if len(certs) == 0 {
		return nil, errors.New("data does not contain any valid RSA or ECDSA certificates")
	}
	return certs, nil
}

// HashCA is k3s hashCA: hex SHA-256 of the raw bytes for a single-cert bundle,
// of the DER of the chain root for a multi-cert bundle.
func HashCA(b []byte) (string, error) {
	certs, err := ParseCertsPEM(b)
	if err != nil {
		return "", err
	}

	if len(certs) > 1 {
		roots := x509.NewCertPool()
		intermediates := x509.NewCertPool()
		for i, cert := range certs {
			if i > 0 && cert.KeyUsage&x509.KeyUsageCertSign != 0 {
				if len(cert.AuthorityKeyId) == 0 || bytes.Equal(cert.AuthorityKeyId, cert.SubjectKeyId) {
					roots.AddCert(cert)
				} else {
					intermediates.AddCert(cert)
				}
			}
		}
		if chains, err := certs[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates}); err == nil {
			chain := chains[0]
			b = chain[len(chain)-1].Raw
		}
	}

	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:]), nil
}

// AppendCertsFromPEM is k3s appendCertsFromPEM: it adds root and intermediate
// certs to a cert pool. With a multi-cert bundle every cert without
// KeyUsageCertSign is pruned first (the CVE fix this harness is about).
func AppendCertsFromPEM(pool *x509.CertPool, cacerts []byte, caHash string) {
	certs, err := ParseCertsPEM(cacerts)
	if err != nil {
		return
	}

	// legacy behavior: if there's a single cert in the bundle, hash the raw pem bytes
	if len(certs) == 1 {
		digest := sha256.Sum256(cacerts)
		if caHash == "" || caHash == hex.EncodeToString(digest[:]) {
			pool.AddCert(certs[0])
		}
		return
	}

	// prune all certs that do not have CertSign key usage
	certs = slices.DeleteFunc(certs, func(cert *x509.Certificate) bool { return cert.KeyUsage&x509.KeyUsageCertSign == 0 })

	// pass 1: add trusted roots
	for _, cert := range certs {
		if len(cert.AuthorityKeyId) == 0 || bytes.Equal(cert.AuthorityKeyId, cert.SubjectKeyId) {
			digest := sha256.Sum256(cert.Raw)
			if caHash == "" || caHash == hex.EncodeToString(digest[:]) {
				pool.AddCert(cert)
			}
		}
	}

	// pass 2: add intermediates issued by a trusted root
	for _, cert := range certs {
		if len(cert.AuthorityKeyId) != 0 && !bytes.Equal(cert.AuthorityKeyId, cert.SubjectKeyId) {
			if _, err := cert.Verify(x509.VerifyOptions{Roots: pool}); err == nil {
				pool.AddCert(cert)
			}
		}
	}
}

// BundleReport is the result of CheckBundle.
type BundleReport struct {
	Name     string
	Certs    int
	Hash     string
	Problems []string
}

// OK reports whether the bundle would be accepted.
func (r BundleReport) OK() bool { return len(r.Problems) == 0 }

func cn(c *x509.Certificate) string {
	if c.Subject.CommonName != "" {
		return c.Subject.CommonName
	}
	return c.Subject.String()
}

// CheckBundle runs the k3s client checks against a leaf-first CA bundle:
//   - RawIssuer/RawSubject byte equality along the chain, last cert self-issued;
//   - AKI/SKI root detection of the last cert (k3s: root iff no AKI or AKI==SKI);
//   - the first cert verifies through the pruned pool, both with an empty CA hash
//     (raw token, what the Teknoir templates use) and with the K10 hash.
func CheckBundle(name string, pemBytes []byte, now time.Time) BundleReport {
	r := BundleReport{Name: name}
	certs, err := ParseCertsPEM(pemBytes)
	if err != nil {
		r.Problems = append(r.Problems, "parse: "+err.Error())
		return r
	}
	r.Certs = len(certs)
	r.Hash, _ = HashCA(pemBytes)

	for i := 0; i < len(certs)-1; i++ {
		if !bytes.Equal(certs[i].RawIssuer, certs[i+1].RawSubject) {
			r.Problems = append(r.Problems, fmt.Sprintf("RawIssuer of cert %d (%s) != RawSubject of cert %d (%s)", i, cn(certs[i]), i+1, cn(certs[i+1])))
		}
	}
	last := certs[len(certs)-1]
	if !bytes.Equal(last.RawIssuer, last.RawSubject) {
		r.Problems = append(r.Problems, fmt.Sprintf("last cert (%s) is not self-issued", cn(last)))
	}
	if len(last.AuthorityKeyId) != 0 && !bytes.Equal(last.AuthorityKeyId, last.SubjectKeyId) {
		r.Problems = append(r.Problems, fmt.Sprintf("root (%s) has AKI != SKI: k3s would treat it as an intermediate", cn(last)))
	}
	if len(certs) > 1 {
		for i, c := range certs {
			if c.KeyUsage&x509.KeyUsageCertSign == 0 {
				r.Problems = append(r.Problems, fmt.Sprintf("cert %d (%s) lacks keyCertSign (KU=%#x): pruned by k3s", i, cn(c), int(c.KeyUsage)))
			}
		}
	}

	for _, mode := range []struct{ label, hash string }{{"raw", ""}, {"hash", r.Hash}} {
		pool := x509.NewCertPool()
		AppendCertsFromPEM(pool, pemBytes, mode.hash)
		if _, err := certs[0].Verify(x509.VerifyOptions{Roots: pool, CurrentTime: now}); err != nil {
			r.Problems = append(r.Problems, fmt.Sprintf("verify[%s] through k3s pool: %v", mode.label, err))
		}
	}
	return r
}

var (
	oidBasicConstraints = asn1.ObjectIdentifier{2, 5, 29, 19}
	oidKeyUsage         = asn1.ObjectIdentifier{2, 5, 29, 15}
)

func extCritical(c *x509.Certificate, oid asn1.ObjectIdentifier) (present, critical bool) {
	for _, e := range c.Extensions {
		if e.Id.Equal(oid) {
			return true, e.Critical
		}
	}
	return false, false
}

// CheckRoot checks the properties of a re-issued Teknoir root against the
// original device certificate and key. key may be nil. wantDays is the
// expected validity (7300 for the re-issued root), with slack of 3 days.
func CheckRoot(root, orig *x509.Certificate, key crypto.Signer, now time.Time, wantDays int) []string {
	var p []string
	add := func(f string, a ...any) { p = append(p, fmt.Sprintf(f, a...)) }

	seen := map[string]bool{}
	for _, e := range root.Extensions {
		if seen[e.Id.String()] {
			add("duplicate extension %s", e.Id)
		}
		seen[e.Id.String()] = true
	}
	if !root.IsCA || !root.BasicConstraintsValid {
		add("root is not CA:TRUE")
	}
	if present, crit := extCritical(root, oidBasicConstraints); !present || !crit {
		add("basicConstraints missing or not critical")
	}
	if present, crit := extCritical(root, oidKeyUsage); !present || !crit {
		add("keyUsage missing or not critical")
	}
	if root.KeyUsage&x509.KeyUsageCertSign == 0 {
		add("keyUsage lacks keyCertSign")
	}
	if root.KeyUsage&x509.KeyUsageCRLSign == 0 {
		add("keyUsage lacks cRLSign")
	}
	if len(root.ExtKeyUsage) != 0 || len(root.UnknownExtKeyUsage) != 0 {
		add("root has an extendedKeyUsage")
	}
	if len(root.AuthorityKeyId) != 0 && !bytes.Equal(root.AuthorityKeyId, root.SubjectKeyId) {
		add("AKI != SKI")
	}
	if !bytes.Equal(root.RawIssuer, root.RawSubject) {
		add("root is not self-issued (RawIssuer != RawSubject)")
	}
	if len(root.SubjectKeyId) == 0 {
		add("root has no SKI")
	}
	if err := root.CheckSignature(root.SignatureAlgorithm, root.RawTBSCertificate, root.Signature); err != nil {
		add("self-signature invalid: %v", err)
	}
	if now.Before(root.NotBefore.Add(-time.Hour)) || now.After(root.NotAfter) {
		add("root is not valid now (%s .. %s)", root.NotBefore.Format(time.RFC3339), root.NotAfter.Format(time.RFC3339))
	}
	if wantDays > 0 {
		want := now.Add(time.Duration(wantDays) * 24 * time.Hour)
		if d := root.NotAfter.Sub(want); d < -72*time.Hour || d > 72*time.Hour {
			add("notAfter %s is not ~now+%dd", root.NotAfter.Format(time.RFC3339), wantDays)
		}
	}
	if key != nil && !key.Public().(interface{ Equal(crypto.PublicKey) bool }).Equal(root.PublicKey) {
		add("root public key does not match the device private key")
	}
	if orig != nil {
		if !bytes.Equal(root.RawSubject, orig.RawSubject) {
			add("subject DER differs from the original certificate")
		}
		if len(orig.SubjectKeyId) != 0 && !bytes.Equal(root.SubjectKeyId, orig.SubjectKeyId) {
			add("SKI differs from the original certificate")
		}
		// two different certificates with the same issuer and serial break RFC 5280 4.1.2.2
		// (NSS: SEC_ERROR_REUSED_ISSUER_AND_SERIAL); OpenSSL 1.1.1 `x509 -signkey` ignores -set_serial
		if bytes.Equal(root.RawIssuer, orig.RawIssuer) && root.SerialNumber.Cmp(orig.SerialNumber) == 0 && !bytes.Equal(root.Raw, orig.Raw) {
			add("serial number equals the original certificate's (same issuer and serial, different certificate)")
		}
		if !orig.PublicKey.(interface{ Equal(crypto.PublicKey) bool }).Equal(root.PublicKey) {
			add("public key differs from the original certificate")
		}
	}
	return p
}

// ParsePrivateKey parses a PEM RSA (PKCS#1 or PKCS#8) or EC private key.
func ParsePrivateKey(b []byte) (crypto.Signer, error) {
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, errors.New("no PEM block in key")
	}
	if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS8PrivateKey(blk.Bytes); err == nil {
		if s, ok := k.(crypto.Signer); ok {
			return s, nil
		}
	}
	if k, err := x509.ParseECPrivateKey(blk.Bytes); err == nil {
		return k, nil
	}
	return nil, errors.New("unsupported private key format")
}

func parseOne(path string) (*x509.Certificate, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cs, err := ParseCertsPEM(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cs[len(cs)-1], nil
}

var bundleFiles = []string{"server-ca.crt", "client-ca.crt", "request-header-ca.crt", "etcd/peer-ca.crt", "etcd/server-ca.crt", "intermediate-ca.crt"}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("k3sverify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	orig := fs.String("orig", "", "original device certificate (rsa_public.pem); enables the root checks")
	keyF := fs.String("key", "", "device private key (rsa_private.pem) to check the root public key against")
	days := fs.Int("days", 7300, "expected root validity in days (0 disables the check)")
	rootF := fs.String("root", "", "root certificate (default <tls_dir>/root-ca.pem)")
	nowF := fs.String("now", "", "verification time, RFC 3339 (default now)")
	quiet := fs.Bool("q", false, "only print problems")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: k3sverify [-orig rsa_public.pem] [-key rsa_private.pem] [-days N] [-root F] [-now T] [-q] <tls_dir>")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	dir := fs.Arg(0)
	now := time.Now()
	if *nowF != "" {
		t, err := time.Parse(time.RFC3339, *nowF)
		if err != nil {
			fmt.Fprintln(stderr, "bad -now:", err)
			return 2
		}
		now = t
	}

	failed := false
	say := func(format string, a ...any) {
		if !*quiet {
			fmt.Fprintf(stdout, format+"\n", a...)
		}
	}
	bad := func(format string, a ...any) {
		failed = true
		fmt.Fprintf(stdout, "FAIL "+format+"\n", a...)
	}

	found := 0
	for _, f := range bundleFiles {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			if f == "server-ca.crt" {
				bad("%s: %v", f, err)
			}
			continue
		}
		found++
		r := CheckBundle(f, b, now)
		if r.OK() {
			say("ok   %s certs=%d ca-hash=%s", f, r.Certs, r.Hash)
		} else {
			for _, pr := range r.Problems {
				bad("%s: %s", f, pr)
			}
		}
	}
	if found == 0 {
		bad("no CA bundles found in %s", dir)
	}

	if *orig != "" || *keyF != "" {
		rootPath := *rootF
		if rootPath == "" {
			rootPath = filepath.Join(dir, "root-ca.pem")
		}
		root, err := parseOne(rootPath)
		if err != nil {
			bad("root: %v", err)
		} else {
			var oc *x509.Certificate
			if *orig != "" {
				if oc, err = parseOne(*orig); err != nil {
					bad("orig: %v", err)
				}
			}
			var key crypto.Signer
			if *keyF != "" {
				kb, err := os.ReadFile(*keyF)
				if err == nil {
					key, err = ParsePrivateKey(kb)
				}
				if err != nil {
					bad("key: %v", err) // never print key material, only the error
				}
			}
			if probs := CheckRoot(root, oc, key, now, *days); len(probs) > 0 {
				for _, pr := range probs {
					bad("root: %s", pr)
				}
			} else {
				say("ok   root %s ski=%s notAfter=%s", filepath.Base(rootPath), strings.ToUpper(hex.EncodeToString(root.SubjectKeyId)), root.NotAfter.Format("2006-01-02"))
			}
			// The bundles must carry exactly this root.
			if b, err := os.ReadFile(filepath.Join(dir, "server-ca.crt")); err == nil {
				if cs, err := ParseCertsPEM(b); err == nil && !bytes.Equal(cs[len(cs)-1].Raw, root.Raw) {
					bad("server-ca.crt: last cert is not %s", filepath.Base(rootPath))
				}
			}
		}
	}

	if failed {
		return 1
	}
	say("PASS")
	return 0
}
