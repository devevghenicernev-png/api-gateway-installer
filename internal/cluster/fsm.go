package cluster

import (
	"io"

	"github.com/hashicorp/raft"
)

// fsm bridges Raft's log replay to apigw's FSMApplier interface. We keep
// it tiny — most decisions live in the caller's applier.
type fsm struct {
	applier FSMApplier
}

func (f *fsm) Apply(log *raft.Log) interface{} {
	if err := f.applier.Apply(log.Data); err != nil {
		return err
	}
	return nil
}

func (f *fsm) Snapshot() (raft.FSMSnapshot, error) {
	payload, err := f.applier.Snapshot()
	if err != nil {
		return nil, err
	}
	return &snapshot{data: payload}, nil
}

func (f *fsm) Restore(rc io.ReadCloser) error {
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		return err
	}
	return f.applier.Restore(body)
}

// snapshot persists FSM state to the Raft log compactor.
type snapshot struct{ data []byte }

func (s *snapshot) Persist(sink raft.SnapshotSink) error {
	if _, err := sink.Write(s.data); err != nil {
		_ = sink.Cancel()
		return err
	}
	return sink.Close()
}

func (s *snapshot) Release() {}
