package control

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"math/big"
	"net"
	"testing"
	"time"
)

// echConfig assembles an ECHConfig for an X25519 public key.
//
// The layout is from RFC 9460: version, length, then config_id, kem_id, the
// public key, the cipher suites, the maximum name length, the public name and
// an empty extension list.
func echConfig(t *testing.T, configID byte, publicKey []byte, publicName string) []byte {
	t.Helper()
	body := []byte{configID}
	body = binary.BigEndian.AppendUint16(body, 0x0020) // DHKEM(X25519, HKDF-SHA256)
	body = binary.BigEndian.AppendUint16(body, uint16(len(publicKey)))
	body = append(body, publicKey...)
	body = binary.BigEndian.AppendUint16(body, 4)      // one cipher suite
	body = binary.BigEndian.AppendUint16(body, 0x0001) // HKDF-SHA256
	body = binary.BigEndian.AppendUint16(body, 0x0001) // AES-128-GCM
	body = append(body, 0)                             // maximum_name_length
	body = append(body, byte(len(publicName)))
	body = append(body, publicName...)
	body = binary.BigEndian.AppendUint16(body, 0) // no extensions

	config := binary.BigEndian.AppendUint16(nil, 0xfe0d)
	config = binary.BigEndian.AppendUint16(config, uint16(len(body)))
	return append(config, body...)
}

func echConfigList(configs ...[]byte) []byte {
	var body []byte
	for _, c := range configs {
		body = append(body, c...)
	}
	return append(binary.BigEndian.AppendUint16(nil, uint16(len(body))), body...)
}

func newECHKey(t *testing.T, configID byte, publicName string) tls.EncryptedClientHelloKey {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return tls.EncryptedClientHelloKey{
		Config:      echConfig(t, configID, key.PublicKey().Bytes(), publicName),
		PrivateKey:  key.Bytes(),
		SendAsRetry: true,
	}
}

func serverCert(t *testing.T, name string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// echServer accepts TLS connections holding only the given ECH key.
func echServer(t *testing.T, cert tls.Certificate, key tls.EncryptedClientHelloKey) net.Listener {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates:             []tls.Certificate{cert},
		EncryptedClientHelloKeys: []tls.EncryptedClientHelloKey{key},
		MinVersion:               tls.VersionTLS13,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_ = conn.(*tls.Conn).HandshakeContext(context.Background())
				conn.Close()
			}()
		}
	}()
	return ln
}

// A rotated ECH key is the normal case, not an error: the server refuses the
// stale configuration and returns its current one, and the client is expected
// to come back with it. Without the retry the client stops working every time
// the operator rotates.
func TestDialTLSRecoversFromAStaleECHConfig(t *testing.T) {
	const name = "control.example"
	cert, pool := serverCert(t, name)
	live := newECHKey(t, 1, name)
	ln := echServer(t, cert, live)

	// What the client believes, and what the server actually holds, differ.
	stale := newECHKey(t, 2, name)

	d := &echDialer{
		tlsConfig: &tls.Config{
			ServerName: name,
			RootCAs:    pool,
			MinVersion: tls.VersionTLS13,
			NextProtos: []string{"http/1.1"},
		},
		dial:   (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		config: echConfigList(stale.Config),
	}

	conn, err := d.dialTLS(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("the stale config should have been recovered from: %v", err)
	}
	defer conn.Close()

	if state := conn.(*tls.Conn).ConnectionState(); !state.ECHAccepted {
		t.Error("the retried handshake completed without ECH being accepted")
	}
	// The recovered configuration must be remembered, so later dials do not
	// repeat the failed round trip.
	if got := d.current(); len(got) == 0 || string(got) == string(echConfigList(stale.Config)) {
		t.Error("the server's retry configuration was not adopted for later dials")
	}
}

func TestDialTLSReportsFailuresThatAreNotARotation(t *testing.T) {
	d := &echDialer{
		tlsConfig: &tls.Config{ServerName: "control.example"},
		dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, net.ErrClosed
		},
	}
	if _, err := d.dialTLS(context.Background(), "tcp", "192.0.2.1:443"); err == nil {
		t.Fatal("a dial failure must be reported, not retried into success")
	}
}
