package localrouter_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/localrouter"
	"github.com/BroLabel/brosettlement-mpc-core/protocol"
)

const (
	testSession  = "dkg-session-1"
	platformID   = "mpc-signer"
	primaryID    = "co-signer-primary"
	recoveryID   = "co-signer-recovery"
	testProtocol = "ECDSA"
)

func TestRouterDeliversLocalFramesAndSendsOnlyPlatformFramesToNetwork(t *testing.T) {
	network := newScriptedTransport()
	router, err := localrouter.New(network, localrouter.Config{
		SessionID:       testSession,
		PlatformPartyID: platformID,
		PrimaryPartyID:  primaryID,
		RecoveryPartyID: recoveryID,
		Stage:           "dkg",
		Protocol:        testProtocol,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	router.Start(ctx)
	t.Cleanup(router.Close)

	primary, err := router.Transport(primaryID)
	if err != nil {
		t.Fatalf("primary Transport() error = %v", err)
	}
	recovery, err := router.Transport(recoveryID)
	if err != nil {
		t.Fatalf("recovery Transport() error = %v", err)
	}

	local := validFrame(primaryID, recoveryID, 1, 1, "msg-local")
	if err := primary.SendFrame(ctx, local); err != nil {
		t.Fatalf("local SendFrame() error = %v", err)
	}
	if got := recvWithTimeout(t, recovery); !sameFrame(got, local) {
		t.Fatalf("local frame = %+v, want %+v", got, local)
	}
	network.assertNoSent(t)

	toPlatform := validFrame(recoveryID, platformID, 1, 1, "msg-platform")
	if err := recovery.SendFrame(ctx, toPlatform); err != nil {
		t.Fatalf("platform SendFrame() error = %v", err)
	}
	if got := network.recvSent(t); !sameFrame(got, toPlatform) {
		t.Fatalf("network frame = %+v, want %+v", got, toPlatform)
	}

	broadcast := validFrame(primaryID, "", 2, 2, "msg-broadcast")
	broadcast.Broadcast = true
	if err := primary.SendFrame(ctx, broadcast); err != nil {
		t.Fatalf("broadcast SendFrame() error = %v", err)
	}
	if got := recvWithTimeout(t, recovery); !sameFrame(got, broadcast) {
		t.Fatalf("local broadcast = %+v, want %+v", got, broadcast)
	}
	if got := network.recvSent(t); !sameFrame(got, broadcast) {
		t.Fatalf("network broadcast = %+v, want %+v", got, broadcast)
	}
}

func TestRouterRejectsSpoofedWrongRoundAndDuplicateLocalFrames(t *testing.T) {
	network := newScriptedTransport()
	router, err := localrouter.New(network, localrouter.Config{
		SessionID:       testSession,
		PlatformPartyID: platformID,
		PrimaryPartyID:  primaryID,
		RecoveryPartyID: recoveryID,
		Stage:           "dkg",
		Protocol:        testProtocol,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Close)
	primary, err := router.Transport(primaryID)
	if err != nil {
		t.Fatal(err)
	}

	spoofed := validFrame(recoveryID, primaryID, 1, 1, "msg-spoofed")
	if err := primary.SendFrame(context.Background(), spoofed); !errors.Is(err, localrouter.ErrInvalidFrame) {
		t.Fatalf("spoofed SendFrame() error = %v, want ErrInvalidFrame", err)
	}

	wrongRound := validFrame(primaryID, recoveryID, 2, 0, "msg-round")
	if err := primary.SendFrame(context.Background(), wrongRound); !errors.Is(err, localrouter.ErrInvalidFrame) {
		t.Fatalf("wrong-round SendFrame() error = %v, want ErrInvalidFrame", err)
	}

	frame := validFrame(primaryID, recoveryID, 3, 1, "msg-duplicate")
	if err := primary.SendFrame(context.Background(), frame); err != nil {
		t.Fatalf("first SendFrame() error = %v", err)
	}
	if err := primary.SendFrame(context.Background(), frame); !errors.Is(err, localrouter.ErrDuplicateFrame) {
		t.Fatalf("duplicate SendFrame() error = %v, want ErrDuplicateFrame", err)
	}

	conflicting := frame
	conflicting.ToParty = platformID
	conflicting.Payload = []byte("conflicting-payload")
	sum := sha256.Sum256(conflicting.Payload)
	conflicting.PayloadHash = hex.EncodeToString(sum[:8])
	if err := primary.SendFrame(context.Background(), conflicting); !errors.Is(err, localrouter.ErrFrameConflict) {
		t.Fatalf("conflicting SendFrame() error = %v, want ErrFrameConflict", err)
	}

	outOfOrder := validFrame(primaryID, recoveryID, 2, 1, "msg-out-of-order")
	if err := primary.SendFrame(context.Background(), outOfOrder); !errors.Is(err, localrouter.ErrInvalidFrame) {
		t.Fatalf("out-of-order SendFrame() error = %v, want ErrInvalidFrame", err)
	}
}

func TestRouterRejectsFramesOutsideImmutablePartyContext(t *testing.T) {
	network := newScriptedTransport()
	router, err := localrouter.New(network, localrouter.Config{
		SessionID:       testSession,
		PlatformPartyID: platformID,
		PrimaryPartyID:  primaryID,
		RecoveryPartyID: recoveryID,
		Stage:           "dkg",
		Protocol:        testProtocol,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Close)
	primary, err := router.Transport(primaryID)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		edit func(*protocol.Frame)
	}{
		{name: "session", edit: func(frame *protocol.Frame) { frame.SessionID = "other-session" }},
		{name: "sender", edit: func(frame *protocol.Frame) { frame.FromParty = recoveryID }},
		{name: "recipient", edit: func(frame *protocol.Frame) { frame.ToParty = "unknown-party" }},
		{name: "round", edit: func(frame *protocol.Frame) { frame.Round = 0 }},
		{name: "sequence", edit: func(frame *protocol.Frame) { frame.Seq = 0 }},
		{name: "payload hash", edit: func(frame *protocol.Frame) { frame.PayloadHash = "wrong" }},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frame := validFrame(primaryID, recoveryID, uint64(index+1), 1, "msg-"+test.name)
			test.edit(&frame)
			if err := primary.SendFrame(context.Background(), frame); !errors.Is(err, localrouter.ErrInvalidFrame) {
				t.Fatalf("SendFrame() error = %v, want ErrInvalidFrame", err)
			}
		})
	}
}

func TestRouterValidatesNetworkFramesBeforePartyDelivery(t *testing.T) {
	network := newScriptedTransport()
	router, err := localrouter.New(network, localrouter.Config{
		SessionID:       testSession,
		PlatformPartyID: platformID,
		PrimaryPartyID:  primaryID,
		RecoveryPartyID: recoveryID,
		Stage:           "dkg",
		Protocol:        testProtocol,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	router.Start(ctx)
	t.Cleanup(router.Close)

	primary, err := router.Transport(primaryID)
	if err != nil {
		t.Fatal(err)
	}
	spoofed := validFrame(recoveryID, primaryID, 1, 1, "msg-network-spoof")
	network.pushInbound(t, spoofed)

	recvCtx, recvCancel := context.WithTimeout(context.Background(), time.Second)
	defer recvCancel()
	if _, err := primary.RecvFrame(recvCtx); !errors.Is(err, localrouter.ErrInvalidFrame) {
		t.Fatalf("RecvFrame() error = %v, want ErrInvalidFrame", err)
	}
}

func TestRouterRecvFailsClosedAfterTerminalFailureWithBufferedFrame(t *testing.T) {
	network := newScriptedTransport()
	router, err := localrouter.New(network, localrouter.Config{
		SessionID:       testSession,
		PlatformPartyID: platformID,
		PrimaryPartyID:  primaryID,
		RecoveryPartyID: recoveryID,
		Stage:           "dkg",
		Protocol:        testProtocol,
	})
	if err != nil {
		t.Fatal(err)
	}
	router.Start(context.Background())
	t.Cleanup(router.Close)

	primary, err := router.Transport(primaryID)
	if err != nil {
		t.Fatal(err)
	}
	network.pushInbound(t, validFrame(platformID, primaryID, 1, 1, "msg-buffered"))
	network.pushInbound(t, validFrame(recoveryID, primaryID, 2, 1, "msg-terminal-spoof"))

	select {
	case <-router.Done():
	case <-time.After(time.Second):
		t.Fatal("router did not publish terminal network validation failure")
	}
	if err := router.Err(); !errors.Is(err, localrouter.ErrInvalidFrame) {
		t.Fatalf("router Err() = %v, want ErrInvalidFrame", err)
	}

	for attempt := 0; attempt < 32; attempt++ {
		if _, err := primary.RecvFrame(context.Background()); !errors.Is(err, localrouter.ErrInvalidFrame) {
			t.Fatalf("RecvFrame() attempt %d error = %v, want terminal ErrInvalidFrame without draining buffered input", attempt, err)
		}
	}
}

func TestRouterKeepsPartyCancellationIndependent(t *testing.T) {
	network := newScriptedTransport()
	router, err := localrouter.New(network, localrouter.Config{
		SessionID:       testSession,
		PlatformPartyID: platformID,
		PrimaryPartyID:  primaryID,
		RecoveryPartyID: recoveryID,
		Stage:           "dkg",
		Protocol:        testProtocol,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Close)
	primary, _ := router.Transport(primaryID)
	recovery, _ := router.Transport(recoveryID)

	primaryCtx, cancelPrimary := context.WithCancel(context.Background())
	cancelPrimary()
	if _, err := primary.RecvFrame(primaryCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("primary RecvFrame() error = %v, want context.Canceled", err)
	}

	frame := validFrame(primaryID, recoveryID, 1, 1, "msg-after-cancel")
	if err := primary.SendFrame(context.Background(), frame); err != nil {
		t.Fatalf("SendFrame() after independent receive cancellation error = %v", err)
	}
	if got := recvWithTimeout(t, recovery); !sameFrame(got, frame) {
		t.Fatalf("recovery frame = %+v, want %+v", got, frame)
	}
}

func TestRouterCloseCancelsNetworkReceiver(t *testing.T) {
	network := &cancelObservingTransport{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
	router, err := localrouter.New(network, localrouter.Config{
		SessionID:       testSession,
		PlatformPartyID: platformID,
		PrimaryPartyID:  primaryID,
		RecoveryPartyID: recoveryID,
		Stage:           "dkg",
		Protocol:        testProtocol,
	})
	if err != nil {
		t.Fatal(err)
	}
	router.Start(context.Background())
	select {
	case <-network.started:
	case <-time.After(time.Second):
		t.Fatal("network receiver did not start")
	}

	router.Close()
	select {
	case <-network.canceled:
	case <-time.After(time.Second):
		t.Fatal("router close left the network receiver running")
	}
}

func TestRouterFinishJoinsReceiverAndRejectsLateNetworkInput(t *testing.T) {
	network := newFinishTrackingTransport()
	router, err := localrouter.New(network, localrouter.Config{
		SessionID:       testSession,
		PlatformPartyID: platformID,
		PrimaryPartyID:  primaryID,
		RecoveryPartyID: recoveryID,
		Stage:           "dkg",
		Protocol:        testProtocol,
	})
	if err != nil {
		t.Fatal(err)
	}
	router.Start(context.Background())
	waitForRouterSignal(t, network.started, "network receiver did not start")

	if err := router.Finish(); err != nil {
		t.Fatalf("Finish() error = %v, want normal shutdown", err)
	}
	select {
	case <-network.exited:
	default:
		t.Fatal("Finish returned before the network receiver exited")
	}

	network.pushLate(validFrame(platformID, primaryID, 1, 1, "late-after-finish"))
	select {
	case <-network.secondReceive:
		t.Fatal("network receiver remained active after Finish")
	case <-time.After(20 * time.Millisecond):
	}

	primary, err := router.Transport(primaryID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := primary.RecvFrame(context.Background()); !errors.Is(err, localrouter.ErrRouterClosed) {
		t.Fatalf("RecvFrame() after Finish error = %v, want ErrRouterClosed", err)
	}
}

func validFrame(from, to string, seq uint64, round uint32, messageID string) protocol.Frame {
	payload := []byte(messageID + "-payload")
	sum := sha256.Sum256(payload)
	return protocol.Frame{
		SessionID:   testSession,
		Stage:       "dkg",
		MessageID:   messageID,
		Seq:         seq,
		Round:       round,
		Protocol:    testProtocol,
		MessageType: "KGRound1Message",
		PayloadHash: hex.EncodeToString(sum[:8]),
		FromParty:   from,
		ToParty:     to,
		Payload:     payload,
	}
}

func recvWithTimeout(t *testing.T, transport interface {
	RecvFrame(context.Context) (protocol.Frame, error)
}) protocol.Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	frame, err := transport.RecvFrame(ctx)
	if err != nil {
		t.Fatalf("RecvFrame() error = %v", err)
	}
	return frame
}

func sameFrame(got, want protocol.Frame) bool {
	return got.SessionID == want.SessionID &&
		got.Stage == want.Stage &&
		got.MessageID == want.MessageID &&
		got.Seq == want.Seq &&
		got.Round == want.Round &&
		got.RoundHint == want.RoundHint &&
		got.Broadcast == want.Broadcast &&
		got.Protocol == want.Protocol &&
		got.MessageType == want.MessageType &&
		got.PayloadHash == want.PayloadHash &&
		got.FromParty == want.FromParty &&
		got.ToParty == want.ToParty &&
		string(got.Payload) == string(want.Payload)
}

type scriptedTransport struct {
	mu      sync.Mutex
	sent    chan protocol.Frame
	inbound chan protocol.Frame
}

type cancelObservingTransport struct {
	started  chan struct{}
	canceled chan struct{}
}

func (*cancelObservingTransport) SendFrame(context.Context, protocol.Frame) error { return nil }

func (t *cancelObservingTransport) RecvFrame(ctx context.Context) (protocol.Frame, error) {
	close(t.started)
	<-ctx.Done()
	close(t.canceled)
	return protocol.Frame{}, ctx.Err()
}

type finishTrackingTransport struct {
	started       chan struct{}
	exited        chan struct{}
	secondReceive chan struct{}
	inbound       chan protocol.Frame

	mu    sync.Mutex
	calls int
}

func newFinishTrackingTransport() *finishTrackingTransport {
	return &finishTrackingTransport{
		started:       make(chan struct{}),
		exited:        make(chan struct{}),
		secondReceive: make(chan struct{}),
		inbound:       make(chan protocol.Frame, 1),
	}
}

func (*finishTrackingTransport) SendFrame(context.Context, protocol.Frame) error { return nil }

func (t *finishTrackingTransport) RecvFrame(ctx context.Context) (protocol.Frame, error) {
	t.mu.Lock()
	t.calls++
	call := t.calls
	t.mu.Unlock()
	if call == 1 {
		close(t.started)
		defer close(t.exited)
	} else {
		close(t.secondReceive)
	}
	select {
	case frame := <-t.inbound:
		return frame, nil
	case <-ctx.Done():
		return protocol.Frame{}, ctx.Err()
	}
}

func (t *finishTrackingTransport) pushLate(frame protocol.Frame) {
	t.inbound <- frame
}

func waitForRouterSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}

func newScriptedTransport() *scriptedTransport {
	return &scriptedTransport{
		sent:    make(chan protocol.Frame, 8),
		inbound: make(chan protocol.Frame, 8),
	}
}

func (t *scriptedTransport) SendFrame(ctx context.Context, frame protocol.Frame) error {
	frame.Payload = append([]byte(nil), frame.Payload...)
	select {
	case t.sent <- frame:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *scriptedTransport) RecvFrame(ctx context.Context) (protocol.Frame, error) {
	select {
	case frame := <-t.inbound:
		return frame, nil
	case <-ctx.Done():
		return protocol.Frame{}, ctx.Err()
	}
}

func (t *scriptedTransport) pushInbound(tester *testing.T, frame protocol.Frame) {
	tester.Helper()
	select {
	case t.inbound <- frame:
	case <-time.After(time.Second):
		tester.Fatal("timed out pushing network frame")
	}
}

func (t *scriptedTransport) recvSent(tester *testing.T) protocol.Frame {
	tester.Helper()
	select {
	case frame := <-t.sent:
		return frame
	case <-time.After(time.Second):
		tester.Fatal("timed out waiting for network frame")
		return protocol.Frame{}
	}
}

func (t *scriptedTransport) assertNoSent(tester *testing.T) {
	tester.Helper()
	select {
	case frame := <-t.sent:
		tester.Fatalf("unexpected network frame: %+v", frame)
	case <-time.After(20 * time.Millisecond):
	}
}
