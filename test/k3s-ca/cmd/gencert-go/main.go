// Command gencert-go replicates the device certificate that
// device-controller (internal/controller/plugin_encryption_key_gen,
// generateRSAKeyAndCert, main@6c718b6) puts into new Device CRs, so the k3s
// custom-CA harness can test it without a cluster.
//
//	gencert-go -out DIR [-ski sha1|sha256trunc] [-fixed]
//
// writes DIR/rsa_private.pem (PKCS#1) and DIR/rsa_public.pem (certificate).
//
// -ski selects how the Subject Key Identifier is derived. Go < 1.25 used the
// SHA-1 of the public key bit string (RFC 5280 4.2.1.2 method 1); Go >= 1.25
// uses a truncated SHA-256. The replica sets it explicitly so both variants
// can be produced by any toolchain.
//
// -fixed produces what the fixed generator must make (WP5): KeyUsage
// DigitalSignature|KeyEncipherment|CertSign|CRLSign, IsCA, random 128-bit
// serial, NotAfter +20y, no ExtKeyUsage.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"flag"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// Generate returns the PEM encoded private key and certificate.
func Generate(skiMode string, fixed bool) (keyPEM, certPEM []byte, err error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	// subjectPublicKey BIT STRING contents (PKCS#1 RSAPublicKey)
	var spki struct {
		Algorithm pkix.AlgorithmIdentifier
		PublicKey asn1.BitString
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, nil, err
	}
	if _, err := asn1.Unmarshal(pubDER, &spki); err != nil {
		return nil, nil, err
	}
	var skiBytes []byte
	switch skiMode {
	case "sha1":
		h := sha1.Sum(spki.PublicKey.Bytes)
		skiBytes = h[:]
	case "sha256trunc":
		h := sha256.Sum256(spki.PublicKey.Bytes)
		skiBytes = h[:20]
	default:
		return nil, nil, fmt.Errorf("unknown -ski %q (want sha1 or sha256trunc)", skiMode)
	}

	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Country:            []string{"US"},
			Province:           []string{"Texas"},
			Locality:           []string{"Houston"},
			Organization:       []string{"Teknoir LLC"},
			OrganizationalUnit: []string{},
			CommonName:         "teknoir.ai",
		},
		NotBefore:             now,
		NotAfter:              now.AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		SubjectKeyId:          skiBytes,
	}
	if fixed {
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			return nil, nil, err
		}
		tpl.SerialNumber = serial
		tpl.NotAfter = now.AddDate(20, 0, 0)
		tpl.KeyUsage |= x509.KeyUsageCertSign | x509.KeyUsageCRLSign
		tpl.ExtKeyUsage = nil
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return keyPEM, certPEM, nil
}

func main() {
	out := flag.String("out", "", "output directory (required)")
	ski := flag.String("ski", "sha1", "subject key id derivation: sha1 or sha256trunc")
	fixed := flag.Bool("fixed", false, "produce the fixed (k3s >= v1.34.12 compatible) certificate")
	flag.Parse()
	if *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	k, c, err := Generate(*ski, *fixed)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gencert-go:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "gencert-go:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join(*out, "rsa_private.pem"), k, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "gencert-go:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join(*out, "rsa_public.pem"), c, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "gencert-go:", err)
		os.Exit(1)
	}
}
