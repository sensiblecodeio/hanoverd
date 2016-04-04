package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	cfg, err := InitializeTLS("127.0.0.1")
	if err != nil {
		log.Fatalf("Error in InitializeTLS: %v", err)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		hostname, _ := os.Hostname()
		fmt.Fprintln(w, hostname)

		time.Sleep(50 * time.Millisecond)
	})

	server := http.Server{TLSConfig: cfg}
	server.ErrorLog = log.New(ioutil.Discard, "", 0)

	go func() { log.Fatal(server.ListenAndServeTLS("", "")) }()
	log.Fatal(http.ListenAndServe(":http", nil))
}

// InitializeTLS ...
func InitializeTLS(internalIPStr string) (*tls.Config, error) {
	internalIP := net.ParseIP(internalIPStr)
	if internalIP == nil {
		return nil, fmt.Errorf("Failed to parse IP %q", internalIPStr)
	}

	// This curve choice is fairly arbitrary and can be changed at a later
	// date without too many consequences. It was chosen because @pwaller
	// had seen it used elsewhere, so it at least has some significant
	// use in the wild.
	//
	// Other considerations to take into account: performance, simplicity.
	// In Mar 2016, P256 is the only one with an assembly implementation,
	// so it is considerably faster than the other curves.
	curve := elliptic.P256()

	key, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		return nil, err
	}

	c := &x509.Certificate{
		// "the serial number must be unique for each certificate
		//  issued by a specific CA (as mentioned in RFC 2459)."
		// Since every server start is its own CA, this can just be 0.
		SerialNumber: big.NewInt(0),

		Subject: pkix.Name{
			OrganizationalUnit: []string{"Test"},
			CommonName:         "TLSPrivateAddress",
		},

		NotBefore: time.Now().Add(-24 * time.Hour), // 1 day ago, in case of clock drift.
		NotAfter:  time.Now().AddDate(2, 0, 0),     // 2 years

		BasicConstraintsValid: true,
		IsCA:           true, // We are an authority for ourselves.
		MaxPathLenZero: true, // We're a self signed cert.

		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageClientAuth,
			x509.ExtKeyUsageServerAuth,
		},

		IPAddresses: []net.IP{internalIP},
	}

	// Generate a self-signed certificate (c passed twice).
	certData, err := x509.CreateCertificate(rand.Reader, c, c, key.Public(), key)
	if err != nil {
		return nil, err
	}

	cert, err := asTLSCertificate(certData, key)
	if err != nil {
		return nil, err
	}

	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},

		// Certificate verification happens elsewhere!
		// ClientAuth: tls.RequireAnyClientCert,
		ClientAuth: tls.RequestClientCert,
	}

	return cfg, nil
}

func asTLSCertificate(
	certData []byte,
	key *ecdsa.PrivateKey,
) (tls.Certificate, error) {
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certData,
	})

	keyData, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "ECDSA PRIVATE KEY",
		Bytes: keyData,
	})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, err
	}

	cert.Leaf, err = x509.ParseCertificate(certData)
	if err != nil {
		return tls.Certificate{}, err
	}

	return cert, nil
}
