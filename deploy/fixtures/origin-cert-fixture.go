// This program creates disposable test certificates only. Never use its keys in production.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	dir := os.Args[1]
	serial := int64(0)
	issue := func(name string, ca *x509.Certificate, signer *ecdsa.PrivateKey, usage x509.ExtKeyUsage, expires time.Time) (*x509.Certificate, *ecdsa.PrivateKey) {
		serial++
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		must(err)
		template := &x509.Certificate{
			SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "Disposable FreedomPost test " + name},
			NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: expires,
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage},
		}
		if ca == nil {
			template.IsCA = true
			template.BasicConstraintsValid = true
			template.KeyUsage |= x509.KeyUsageCertSign
			template.ExtKeyUsage = nil
			ca, signer = template, key
		} else if usage == x509.ExtKeyUsageServerAuth {
			template.DNSNames = []string{"admin.test", "public.test", "origin.test"}
		}
		der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, signer)
		must(err)
		must(os.WriteFile(filepath.Join(dir, name+".crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		must(err)
		must(os.WriteFile(filepath.Join(dir, name+".key"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600))
		cert, err := x509.ParseCertificate(der)
		must(err)
		return cert, key
	}
	validUntil := time.Now().Add(30 * 24 * time.Hour)
	ca, caKey := issue("ca", nil, nil, 0, validUntil)
	wrongCA, wrongKey := issue("wrong-ca", nil, nil, 0, validUntil)
	issue("server", ca, caKey, x509.ExtKeyUsageServerAuth, validUntil)
	issue("aop", ca, caKey, x509.ExtKeyUsageClientAuth, validUntil)
	issue("health", ca, caKey, x509.ExtKeyUsageClientAuth, validUntil)
	issue("wrong-client", wrongCA, wrongKey, x509.ExtKeyUsageClientAuth, validUntil)
	issue("expired", ca, caKey, x509.ExtKeyUsageClientAuth, time.Now().Add(-time.Hour))
	issue("soon", ca, caKey, x509.ExtKeyUsageClientAuth, time.Now().Add(24*time.Hour))
}
