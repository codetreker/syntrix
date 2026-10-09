package health

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

func TestBootstrapMode(t *testing.T) {
	t.Parallel()
	if BootstrapFromNow != "from_now" {
		t.Errorf("BootstrapFromNow = %q, want 'from_now'", BootstrapFromNow)
	}
	if BootstrapFromBeginning != "from_beginning" {
		t.Errorf("BootstrapFromBeginning = %q, want 'from_beginning'", BootstrapFromBeginning)
	}
}

type mockCheckpointForBootstrap struct {
	token bson.Raw
	err   error
}

func (m *mockCheckpointForBootstrap) LoadCheckpoint() (bson.Raw, error) {
	return m.token, m.err
}

func TestBootstrap_FirstRun(t *testing.T) {
	t.Parallel()
	mock := &mockCheckpointForBootstrap{token: nil}
	b := NewBootstrap(BootstrapOptions{
		Mode:       BootstrapFromNow,
		Checkpoint: mock,
	})

	isFirst, err := b.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !isFirst {
		t.Error("Run() should return true for first run")
	}
	if !b.IsFirstRun() {
		t.Error("IsFirstRun() should be true")
	}
	if !b.IsBootstrapped() {
		t.Error("IsBootstrapped() should be true")
	}
}

func TestBootstrap_Resume(t *testing.T) {
	t.Parallel()
	mock := &mockCheckpointForBootstrap{token: bson.Raw{1, 2, 3}}
	b := NewBootstrap(BootstrapOptions{
		Mode:       BootstrapFromNow,
		Checkpoint: mock,
	})

	isFirst, err := b.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if isFirst {
		t.Error("Run() should return false when checkpoint exists")
	}
	if b.IsFirstRun() {
		t.Error("IsFirstRun() should be false")
	}
}

func TestBootstrap_DefaultMode(t *testing.T) {
	t.Parallel()
	b := NewBootstrap(BootstrapOptions{})

	if b.Mode() != BootstrapFromNow {
		t.Errorf("Mode() = %q, want 'from_now'", b.Mode())
	}
}

func TestBootstrap_NilCheckpoint(t *testing.T) {
	t.Parallel()
	b := NewBootstrap(BootstrapOptions{
		Checkpoint: nil,
	})

	isFirst, err := b.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !isFirst {
		t.Error("Run() should return true with nil checkpoint")
	}
}

func TestBootstrap_RunTwice(t *testing.T) {
	t.Parallel()
	mock := &mockCheckpointForBootstrap{token: nil}
	b := NewBootstrap(BootstrapOptions{
		Checkpoint: mock,
	})

	// First run
	isFirst1, _ := b.Run(context.Background())

	// Second run should return same result without calling checkpoint again
	mock.token = bson.Raw{1, 2, 3} // Change the token
	isFirst2, _ := b.Run(context.Background())

	if isFirst1 != isFirst2 {
		t.Error("Run() should return same result on second call")
	}
}
