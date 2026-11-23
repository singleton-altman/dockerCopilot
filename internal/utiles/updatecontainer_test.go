package utiles

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
)

type fakeContainerAPI struct {
	inspect func(id string) (types.ContainerJSON, error)
	calls   int
}

func (f *fakeContainerAPI) ContainerInspect(_ context.Context, id string) (types.ContainerJSON, error) {
	f.calls++
	return f.inspect(id)
}

func runningContainer() types.ContainerJSON {
	return types.ContainerJSON{ContainerJSONBase: &types.ContainerJSONBase{
		State: &types.ContainerState{Status: "running", Running: true},
	}}
}

func stoppedContainer(status string) types.ContainerJSON {
	return types.ContainerJSON{ContainerJSONBase: &types.ContainerJSONBase{
		State: &types.ContainerState{Status: status, Running: false},
	}}
}

func TestAwaitContainerHealthyRunning(t *testing.T) {
	fake := &fakeContainerAPI{inspect: func(id string) (types.ContainerJSON, error) {
		if id != "new123" {
			t.Fatalf("unexpected id: %s", id)
		}
		return runningContainer(), nil
	}}
	if err := awaitContainerHealthy(context.Background(), fake, "new123", 400*time.Millisecond, 100*time.Millisecond); err != nil {
		t.Fatalf("expected healthy, got %v", err)
	}
	if fake.calls < 3 {
		t.Fatalf("expected multiple polls, got %d", fake.calls)
	}
}

func TestAwaitContainerHealthyExited(t *testing.T) {
	fake := &fakeContainerAPI{inspect: func(id string) (types.ContainerJSON, error) {
		return stoppedContainer("exited"), nil
	}}
	err := awaitContainerHealthy(context.Background(), fake, "new123", 2*time.Second, 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected failure for exited container")
	}
	if fake.calls != 1 {
		t.Fatalf("should fail fast, got %d calls", fake.calls)
	}
}

func TestAwaitContainerHealthyTransientInspectError(t *testing.T) {
	calls := 0
	fake := &fakeContainerAPI{inspect: func(id string) (types.ContainerJSON, error) {
		calls++
		if calls == 1 {
			return types.ContainerJSON{}, errors.New("daemon busy")
		}
		return runningContainer(), nil
	}}
	if err := awaitContainerHealthy(context.Background(), fake, "new123", 400*time.Millisecond, 50*time.Millisecond); err != nil {
		t.Fatalf("transient inspect error should not fail: %v", err)
	}
}

func TestAwaitContainerHealthyPersistentInspectError(t *testing.T) {
	fake := &fakeContainerAPI{inspect: func(id string) (types.ContainerJSON, error) {
		return types.ContainerJSON{}, errors.New("daemon down")
	}}
	err := awaitContainerHealthy(context.Background(), fake, "new123", 300*time.Millisecond, 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected failure when container state never confirmed")
	}
}
