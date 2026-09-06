package handoff_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/handoff"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

type runtime struct {
	freezeErr, prepareErr, activateErr, probeErr error
	launches                                     atomic.Int32
	active                                       atomic.Bool
	freezeStarted, freezeRelease                 chan struct{}
}

func (r *runtime) Freeze(ctx context.Context, _ domain.MachineHandoff) (string, error) {
	if r.freezeStarted != nil {
		close(r.freezeStarted)
		select {
		case <-r.freezeRelease:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return strings.Repeat("a", 64), r.freezeErr
}
func (r *runtime) Prepare(context.Context, domain.MachineHandoff) error { return r.prepareErr }
func (r *runtime) Activate(context.Context, domain.MachineHandoff) error {
	r.launches.Add(1)
	r.active.Store(true)
	return r.activateErr
}
func (r *runtime) ConfirmActive(context.Context, domain.MachineHandoff) (bool, error) {
	return r.active.Load(), r.probeErr
}

type pair struct {
	a, b   *handoff.Service
	sa, sb *sqlite.Store
	ra, rb *runtime
}

func newPair(t *testing.T) pair {
	t.Helper()
	p := pair{sa: sqlitetest.MustOpen(t), sb: sqlitetest.MustOpen(t), ra: &runtime{}, rb: &runtime{}}
	p.a, p.b = handoff.New(p.sa, p.ra, "A"), handoff.New(p.sb, p.rb, "B")
	return p
}

func (p pair) prepare(t *testing.T) domain.MachineHandoff {
	t.Helper()
	ctx := context.Background()
	if _, err := p.a.Start(ctx, "move", "session", "B"); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := p.a.Freeze(ctx, "move")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.b.Import(ctx, checkpoint); err != nil {
		t.Fatal(err)
	}
	receipt, err := p.b.Prepare(ctx, "move")
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestHandoffOwnershipAndLostAcknowledgements(t *testing.T) {
	p := newPair(t)
	ctx := context.Background()
	receipt := p.prepare(t)
	if _, err := p.b.Activate(ctx, "move", strings.Repeat("0", 64)); !errors.Is(err, domain.ErrMachineHandoffInvalid) {
		t.Fatalf("unauthorized activation: %v", err)
	}
	if p.rb.launches.Load() != 0 {
		t.Fatal("launched before relinquishment")
	}
	token, err := p.a.Relinquish(ctx, "move", receipt)
	if err != nil {
		t.Fatal(err)
	}
	// Lose the reply and restart the coordinator; the same grant is recoverable.
	p.a = handoff.New(p.sa, p.ra, "A")
	again, err := p.a.Relinquish(ctx, "move", receipt)
	if err != nil || again != token {
		t.Fatalf("relinquish retry: %v", err)
	}
	p.rb.activateErr = errors.New("activation reply lost")
	if _, err := p.b.Activate(ctx, "move", token); !errors.Is(err, domain.ErrMachineHandoffUncertain) {
		t.Fatalf("lost activation reply: %v", err)
	}
	p.b = handoff.New(p.sb, p.rb, "B")
	result, err := p.b.Activate(ctx, "move", token)
	if err != nil || result.State != domain.HandoffActive || p.rb.launches.Load() != 1 {
		t.Fatalf("activation replay: %+v %v launches=%d", result, err, p.rb.launches.Load())
	}
	if _, err := p.a.Cancel(ctx, "move"); !errors.Is(err, domain.ErrMachineHandoffConflict) {
		t.Fatalf("cancel relinquished source: %v", err)
	}
	if fenced, err := p.sa.MachineHandoffFencesSession(ctx, "session"); err != nil || !fenced {
		t.Fatalf("source lost permanent fence: %v", err)
	}
	if fenced, err := p.sb.MachineHandoffFencesSession(ctx, "session"); err != nil || fenced {
		t.Fatalf("destination remained fenced: %v", err)
	}
	read, err := p.a.Get(ctx, "move", domain.HandoffSource)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(read)
	if err != nil || read.ActivationSecret != "" || strings.Contains(string(data), token) {
		t.Fatal("grant leaked through read model")
	}
}

func TestHandoffFailuresKeepSourceRecoverable(t *testing.T) {
	for _, stage := range []string{"freeze", "prepare"} {
		t.Run(stage, func(t *testing.T) {
			p := newPair(t)
			ctx := context.Background()
			if _, err := p.a.Start(ctx, "move", "session", "B"); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("injected failure")
			if stage == "freeze" {
				p.ra.freezeErr = failure
			} else {
				p.rb.prepareErr = failure
			}
			checkpoint, err := p.a.Freeze(ctx, "move")
			if stage == "freeze" {
				if !errors.Is(err, failure) {
					t.Fatal(err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := p.b.Import(ctx, checkpoint); err != nil {
					t.Fatal(err)
				}
				if _, err := p.b.Prepare(ctx, "move"); !errors.Is(err, failure) {
					t.Fatal(err)
				}
			}
			if fenced, err := p.sa.MachineHandoffFencesSession(ctx, "session"); err != nil || !fenced {
				t.Fatal("failure opened source fence", err)
			}
			if _, err := p.a.Cancel(ctx, "move"); err != nil {
				t.Fatal(err)
			}
			if fenced, err := p.sa.MachineHandoffFencesSession(ctx, "session"); err != nil || fenced {
				t.Fatal("cancel did not permit explicit recovery", err)
			}
			if p.ra.launches.Load() != 0 || p.rb.launches.Load() != 0 {
				t.Fatal("failure/cancellation launched an agent")
			}
		})
	}
}

func TestHandoffUnknownProbeNeverRelaunches(t *testing.T) {
	p := newPair(t)
	receipt := p.prepare(t)
	ctx := context.Background()
	token, err := p.a.Relinquish(ctx, "move", receipt)
	if err != nil {
		t.Fatal(err)
	}
	p.rb.probeErr = errors.New("probe unavailable")
	for i := 0; i < 3; i++ {
		if _, err := p.b.Activate(ctx, "move", token); !errors.Is(err, domain.ErrMachineHandoffUncertain) {
			t.Fatal(err)
		}
	}
	if p.rb.launches.Load() != 1 {
		t.Fatal("unknown probe caused a second launch")
	}
	if fenced, err := p.sb.MachineHandoffFencesSession(ctx, "session"); err != nil || !fenced {
		t.Fatal("unknown activation lost fence", err)
	}
}

func TestHandoffDuplicateAndMismatchedRequests(t *testing.T) {
	p := newPair(t)
	ctx := context.Background()
	receipt := p.prepare(t)
	if _, err := p.a.Start(ctx, "move", "session", "B"); err != nil {
		t.Fatal("idempotent retry", err)
	}
	for _, request := range []struct {
		id      string
		session domain.SessionID
		dest    string
	}{
		{"move", "another", "B"}, {"move", "session", "C"}, {"second", "session", "B"},
	} {
		if _, err := p.a.Start(ctx, request.id, request.session, request.dest); !errors.Is(err, domain.ErrMachineHandoffConflict) {
			t.Fatalf("conflicting request: %v", err)
		}
	}
	bad := receipt
	bad.CheckpointHash = strings.Repeat("b", 64)
	if token, err := p.a.Relinquish(ctx, "move", bad); token != "" || !errors.Is(err, domain.ErrMachineHandoffConflict) {
		t.Fatalf("unbound receipt: %v", err)
	}
	checkpoint, err := p.a.Get(ctx, "move", domain.HandoffSource)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.ActivationHash = strings.Repeat("c", 64)
	if _, err := p.b.Import(ctx, checkpoint); !errors.Is(err, domain.ErrMachineHandoffConflict) {
		t.Fatalf("replaced activation verifier: %v", err)
	}
}

func TestHandoffConcurrentActivationAcrossCoordinators(t *testing.T) {
	p := newPair(t)
	ctx := context.Background()
	receipt := p.prepare(t)
	token, err := p.a.Relinquish(ctx, "move", receipt)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := handoff.New(p.sb, p.rb, "B")
			_, err := s.Activate(ctx, "move", token)
			if err != nil && !errors.Is(err, domain.ErrMachineHandoffConflict) && !errors.Is(err, domain.ErrMachineHandoffUncertain) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if p.rb.launches.Load() != 1 {
		t.Fatalf("duplicate activation: %d", p.rb.launches.Load())
	}
}

type failAdvance struct {
	ports.MachineHandoffStore
	state domain.HandoffState
}

func (s failAdvance) AdvanceMachineHandoff(ctx context.Context, h domain.MachineHandoff, expected domain.HandoffState, revision int64) (bool, error) {
	if h.State == s.state {
		return false, errors.New("injected write failure")
	}
	return s.MachineHandoffStore.AdvanceMachineHandoff(ctx, h, expected, revision)
}

func TestHandoffDurableWritePrecedesGrantAndLaunch(t *testing.T) {
	p := newPair(t)
	ctx := context.Background()
	receipt := p.prepare(t)
	a := handoff.New(failAdvance{p.sa, domain.HandoffRelinquished}, p.ra, "A")
	if token, err := a.Relinquish(ctx, "move", receipt); err == nil || token != "" {
		t.Fatal("grant escaped failed commit")
	}
	token, err := p.a.Relinquish(ctx, "move", receipt)
	if err != nil {
		t.Fatal(err)
	}
	b := handoff.New(failAdvance{p.sb, domain.HandoffActivating}, p.rb, "B")
	if _, err := b.Activate(ctx, "move", token); err == nil || p.rb.launches.Load() != 0 {
		t.Fatal("launch preceded commit")
	}
}

func TestHandoffCancelWaitsForLocalSnapshot(t *testing.T) {
	p := newPair(t)
	ctx := context.Background()
	p.ra.freezeStarted, p.ra.freezeRelease = make(chan struct{}), make(chan struct{})
	if _, err := p.a.Start(ctx, "move", "session", "B"); err != nil {
		t.Fatal(err)
	}
	frozen, cancelled := make(chan error, 1), make(chan error, 1)
	go func() { _, err := p.a.Freeze(ctx, "move"); frozen <- err }()
	<-p.ra.freezeStarted
	go func() { _, err := p.a.Cancel(ctx, "move"); cancelled <- err }()
	close(p.ra.freezeRelease)
	if err := <-frozen; err != nil {
		t.Fatal(err)
	}
	if err := <-cancelled; err != nil {
		t.Fatal(err)
	}
	h, err := p.a.Get(ctx, "move", domain.HandoffSource)
	if err != nil || h.State != domain.HandoffCancelled {
		t.Fatal(h.State, err)
	}
	if _, err := p.a.Freeze(ctx, "move"); !errors.Is(err, domain.ErrMachineHandoffConflict) {
		t.Fatal("cancelled handoff restarted snapshot", err)
	}
}
