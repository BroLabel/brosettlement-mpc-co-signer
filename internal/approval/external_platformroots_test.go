//go:build go1.27

package approval

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Run in a fresh process so the standard library root pool has not been cached.
func TestExternalPlatformRootProbe(t *testing.T) {
	if endpoint := os.Getenv("APPROVAL_PLATFORM_ROOT_PROBE"); endpoint != "" {
		c := testConfig(t)
		c.Mode = "external"
		c.Endpoint = endpoint
		c.BearerToken = "synthetic"
		external, err := NewExternal(c, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = external.Evaluate(context.Background(), pendingItem(t, "probe"))
		wantSuccess := os.Getenv("APPROVAL_PLATFORM_EXPECT_SUCCESS") == "1"
		if (err == nil) != wantSuccess {
			t.Fatalf("default verified TLS: success=%t want=%t err=%v", err == nil, wantSuccess, err)
		}
		// Inspect the unmodified default TLS verifier too, so failures have the expected reason.
		response, rawErr := http.Get(endpoint)
		if response != nil {
			response.Body.Close()
		}
		expected := os.Getenv("APPROVAL_PLATFORM_EXPECT_ERROR")
		if expected != "" && (rawErr == nil || !strings.Contains(rawErr.Error(), expected)) {
			t.Fatalf("TLS failure reason: %v; want %s", rawErr, expected)
		}
		return
	}
	for _, name := range []string{"trusted", "wrong-ca", "wrong-host"} {
		t.Run(name, func(t *testing.T) {
			pub, key, _ := ed25519.GenerateKey(rand.Reader)
			cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "owned-test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"wrong.example"}}
			if name != "wrong-host" {
				cert.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
			}
			der, err := x509.CreateCertificate(rand.Reader, cert, cert, pub, key)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				item := pendingItem(t, "probe")
				fmt.Fprintf(w, `{"version":1,"orgId":"org-1","requestId":"probe","authorizationHash":"%s","status":"PENDING","reason":null,"decisionReference":null}`, item.AuthorizationHash)
			}))
			server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
			server.StartTLS()
			defer server.Close()
			root := filepath.Join(t.TempDir(), "root.pem")
			if name == "wrong-ca" {
				root = filepath.Join(t.TempDir(), "absent.pem")
			} else if err = os.WriteFile(root, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestExternalPlatformRootProbe$")
			cmd.Env = append(os.Environ(), "APPROVAL_PLATFORM_ROOT_PROBE="+server.URL, "SSL_CERT_FILE="+root, "SSL_CERT_DIR="+t.TempDir(), "GODEBUG=x509sslcertoverrideplatform=1")
			if name == "trusted" {
				cmd.Env = append(cmd.Env, "APPROVAL_PLATFORM_EXPECT_SUCCESS=1")
			}
			if name == "wrong-ca" {
				cmd.Env = append(cmd.Env, "APPROVAL_PLATFORM_EXPECT_ERROR=unknown authority")
			}
			if name == "wrong-host" {
				cmd.Env = append(cmd.Env, "APPROVAL_PLATFORM_EXPECT_ERROR=doesn't contain any IP SANs")
			}
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, output)
			}
		})
	}
}
