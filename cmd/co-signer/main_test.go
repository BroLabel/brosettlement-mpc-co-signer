package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/health"
)

func TestProbeArtifactStoresFailsClosedOnFirstUnavailableCapability(t *testing.T) {
	wantErr := errors.New("renameat2 unavailable")
	primary := &stubArtifactCapabilityProber{err: wantErr}
	recovery := &stubArtifactCapabilityProber{}

	err := probeArtifactStores(context.Background(), primary, recovery)
	if !errors.Is(err, wantErr) {
		t.Fatalf("probeArtifactStores() error = %v, want %v", err, wantErr)
	}
	if primary.calls != 1 {
		t.Fatalf("primary probe calls = %d, want 1", primary.calls)
	}
	if recovery.calls != 0 {
		t.Fatalf("recovery probe calls = %d, want 0 after primary failure", recovery.calls)
	}
}

func TestCoSignerStartupPreservesEarlyCapabilityErrorWithoutPanic(t *testing.T) {
	root := t.TempDir()
	primaryTarget := filepath.Join(root, "primary-target")
	if err := os.Mkdir(primaryTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	primaryStore := filepath.Join(root, "primary")
	if err := os.Symlink(primaryTarget, primaryStore); err != nil {
		t.Fatal(err)
	}

	privateKey := base64.StdEncoding.EncodeToString(make([]byte, ed25519.PrivateKeySize))
	command := exec.Command("go", "run", ".")
	command.Env = append(os.Environ(), []string{
		"GOWORK=off",
		"CO_SIGNER_MONOLITH_URL=https://monolith.test",
		"CO_SIGNER_API_KEY_ID=key-1",
		"CO_SIGNER_API_PRIVATE_KEY=" + privateKey,
		"CO_SIGNER_PRIMARY_SHARES_DIR=" + primaryStore,
		"CO_SIGNER_RECOVERY_SHARES_DIR=" + filepath.Join(root, "recovery"),
		"CO_SIGNER_SHARE_ENCRYPTION_KEY=" + base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"CO_SIGNER_SHARE_ENCRYPTION_KEY_ID=keyref-1",
		"CO_SIGNER_FREE_SPACE_THRESHOLD_BYTES=1",
		"CO_SIGNER_PREPARAMS_GENERATION_PARALLELISM=1",
	}...)

	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("co-signer startup succeeded with an invalid primary store")
	}
	if strings.Contains(string(output), "panic:") {
		t.Fatalf("co-signer startup panicked instead of returning the capability error: %s", output)
	}
	if !strings.Contains(string(output), "open signing and provisioning capabilities: initialize primary artifact store:") {
		t.Fatalf("co-signer startup output = %s, want original capability error", output)
	}
}

func TestVerifyMPC2of3RejectsPreviousMPCorePin(t *testing.T) {
	bin := t.TempDir()
	writeCommand := func(name, body string) {
		t.Helper()
		path := filepath.Join(bin, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeCommand("uname", "echo Linux")
	writeCommand("grep", "exit 1")
	writeCommand("go", "echo v0.3.1")
	command := exec.Command("/bin/sh", filepath.Join("..", "..", "scripts", "verify-mpc-2of3.sh"))
	command.Env = append(os.Environ(), "PATH="+bin, "GOWORK=on")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("verify script accepted the previous mpc-core pin")
	}
	if !strings.Contains(string(output), "mpc-core must resolve exactly v0.4.0") {
		t.Fatalf("output = %s, want rejection of v0.3.1", output)
	}
}

func TestArtifactInventoryExportsAggregateOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mpc_key_123e4567-e89b-42d3-a456-426614174000.primary.json"), []byte("B"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".artifact.tmp-1"), []byte("tmp"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".co-signer.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	files, temporary, bytes, _, err := artifactInventory([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 || temporary != 1 || bytes != 1 {
		t.Fatalf("aggregate inventory = files:%d temporary:%d bytes:%d", files, temporary, bytes)
	}
}

func TestNewDKGCoreServiceBindsExplicitPreParamsProfile(t *testing.T) {
	service, controller, err := newDKGCoreService(slog.Default(), nil, nil, 0)
	if err == nil {
		t.Fatal("newDKGCoreService(0) error = nil")
	}
	if service != nil || controller != nil {
		t.Fatal("invalid profile returned a partially initialized service")
	}

	service, controller, err = newDKGCoreService(slog.Default(), nil, nil, 1)
	if err != nil {
		t.Fatalf("newDKGCoreService(1) error = %v", err)
	}
	if service == nil || controller == nil {
		t.Fatal("valid profile did not return one service-bound controller")
	}
}

func TestProbeArtifactStoresFailsClosedOnRecoveryCapability(t *testing.T) {
	wantErr := errors.New("directory fsync unavailable")
	primary := &stubArtifactCapabilityProber{}
	recovery := &stubArtifactCapabilityProber{err: wantErr}

	err := probeArtifactStores(context.Background(), primary, recovery)
	if !errors.Is(err, wantErr) {
		t.Fatalf("probeArtifactStores() error = %v, want %v", err, wantErr)
	}
	if primary.calls != 1 || recovery.calls != 1 {
		t.Fatalf("probe calls = primary:%d recovery:%d, want 1 each", primary.calls, recovery.calls)
	}
}

type stubArtifactCapabilityProber struct {
	calls int
	err   error
}

func (p *stubArtifactCapabilityProber) ProbePublishCapability(context.Context) error {
	p.calls++
	return p.err
}

func TestDKGProvisioningAdmissionHintRequiresPreParamsAndDiskCapacity(t *testing.T) {
	tests := []struct {
		name           string
		preparamsReady bool
		freeBytes      map[string]uint64
		freeErr        map[string]error
		want           bool
		wantDiskCalls  int
	}{
		{
			name:           "ready at threshold",
			preparamsReady: true,
			freeBytes: map[string]uint64{
				"/primary":  100,
				"/recovery": 100,
			},
			want:          true,
			wantDiskCalls: 2,
		},
		{
			name:           "preparams unavailable skips disk probe",
			preparamsReady: false,
			want:           false,
			wantDiskCalls:  0,
		},
		{
			name:           "primary disk low",
			preparamsReady: true,
			freeBytes: map[string]uint64{
				"/primary":  99,
				"/recovery": 100,
			},
			want:          false,
			wantDiskCalls: 1,
		},
		{
			name:           "recovery disk low",
			preparamsReady: true,
			freeBytes: map[string]uint64{
				"/primary":  100,
				"/recovery": 99,
			},
			want:          false,
			wantDiskCalls: 2,
		},
		{
			name:           "disk capability error",
			preparamsReady: true,
			freeBytes: map[string]uint64{
				"/primary": 100,
			},
			freeErr: map[string]error{
				"/recovery": errors.New("statfs unavailable"),
			},
			want:          false,
			wantDiskCalls: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preparams := stubAdmissionHinter{ready: tt.preparamsReady}
			diskCalls := 0
			freeSpace := func(path string) (uint64, error) {
				diskCalls++
				if err := tt.freeErr[path]; err != nil {
					return 0, err
				}
				return tt.freeBytes[path], nil
			}

			got := dkgProvisioningAdmissionHint(
				preparams,
				[]string{"/primary", "/recovery"},
				100,
				freeSpace,
			)
			if got != tt.want {
				t.Fatalf("dkgProvisioningAdmissionHint() = %v, want %v", got, tt.want)
			}
			if diskCalls != tt.wantDiskCalls {
				t.Fatalf("disk calls = %d, want %d", diskCalls, tt.wantDiskCalls)
			}
		})
	}
}

type stubAdmissionHinter struct {
	ready bool
}

func (s stubAdmissionHinter) AdmissionHint() bool {
	return s.ready
}

func TestDrainWorkersConsumesAllSemaphoreSlots(t *testing.T) {
	sem := make(chan struct{}, 2)
	sem <- struct{}{}

	go func() {
		time.Sleep(time.Millisecond)
		<-sem
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := drainWorkers(ctx, sem); err != nil {
		t.Fatalf("drainWorkers() error = %v, want nil", err)
	}
}

func TestApplicationResourcesCloseJoinsStartedBackgroundLoop(t *testing.T) {
	resources := &applicationResources{}
	loopStarted := make(chan struct{})
	loopCanceled := make(chan struct{})
	releaseLoop := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	resources.startBackground(func() {
		close(loopStarted)
		<-ctx.Done()
		close(loopCanceled)
		<-releaseLoop
	})
	<-loopStarted
	cancel()

	closeReturned := make(chan error, 1)
	go func() { closeReturned <- resources.Close() }()
	<-loopCanceled
	select {
	case err := <-closeReturned:
		t.Fatalf("Close() returned before background loop exit: %v", err)
	default:
	}
	close(releaseLoop)
	if err := <-closeReturned; err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestApplicationHealthServerUsesLiveProvisioningPredicate(t *testing.T) {
	readiness := health.NewReadiness()
	readiness.Set(health.Snapshot{
		ProcessReady:       true,
		SigningReady:       true,
		ProvisioningReason: health.ReasonProvisioningUnavailable,
	})
	provisioningReady := false
	resources := &applicationResources{
		signingReady:      func() bool { return true },
		provisioningReady: func() bool { return provisioningReady },
	}
	server := newApplicationHealthServer("127.0.0.1:0", t.TempDir(), readiness, resources)

	provisioningReady = true
	rec := httptest.NewRecorder()
	server.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	var body struct {
		ProcessReady       bool          `json:"processReady"`
		SigningReady       bool          `json:"signingReady"`
		ProvisioningReady  bool          `json:"provisioningReady"`
		ProvisioningReason health.Reason `json:"provisioningReason"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if !body.ProcessReady || !body.SigningReady || !body.ProvisioningReady || body.ProvisioningReason != health.ReasonNone {
		t.Fatalf("application health readiness = %#v, want all ready", body)
	}
}

func TestDecodePrivateKeyHex(t *testing.T) {
	key, err := decodePrivateKey(hex.EncodeToString(make([]byte, 64)))
	if err != nil {
		t.Fatalf("decodePrivateKey() error = %v", err)
	}
	if len(key) != 64 {
		t.Fatalf("decoded key length = %d, want 64", len(key))
	}
}

func TestDecodePrivateKeyBase64(t *testing.T) {
	key, err := decodePrivateKey(base64.StdEncoding.EncodeToString(make([]byte, 64)))
	if err != nil {
		t.Fatalf("decodePrivateKey() error = %v", err)
	}
	if len(key) != 64 {
		t.Fatalf("decoded key length = %d, want 64", len(key))
	}
}

func TestDecodePrivateKeyPEM(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(strings.NewReader(strings.Repeat("a", 64)))
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey() error = %v", err)
	}

	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	key, err := decodePrivateKey(string(pemBytes))
	if err != nil {
		t.Fatalf("decodePrivateKey() error = %v", err)
	}
	if string(key) != string(privateKey) {
		t.Fatal("decoded key does not match original private key")
	}
}

func TestDecodePrivateKeyPEMWithEscapedNewlines(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(strings.NewReader(strings.Repeat("b", 64)))
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey() error = %v", err)
	}

	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	escaped := strings.ReplaceAll(string(pemBytes), "\n", "\\n")

	key, err := decodePrivateKey(escaped)
	if err != nil {
		t.Fatalf("decodePrivateKey() error = %v", err)
	}
	if string(key) != string(privateKey) {
		t.Fatal("decoded key does not match original private key")
	}
}

func TestDecodePrivateKeyPEMRejectsNonEd25519(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey() error = %v", err)
	}

	block := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	if _, err := decodePrivateKey(string(block)); err == nil {
		t.Fatal("decodePrivateKey() error = nil, want non-nil")
	}
}

func TestDecodePrivateKeyInvalid(t *testing.T) {
	if _, err := decodePrivateKey("not-a-key"); err == nil {
		t.Fatal("decodePrivateKey() error = nil, want non-nil")
	}
}
