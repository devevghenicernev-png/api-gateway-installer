package serve

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/devevghenicernev-png/apigw/internal/cluster"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/paths"
)

// runClusterNode boots a Raft member when config.Cluster.NodeID is set. The
// FSM serializes the apigw config as JSON: every Propose() sends the full
// post-mutation config; followers replay it onto disk.
//
// This is the minimum viable replication: small config (<100 KB even with
// hundreds of APIs), proposed only on mutations (handful per day), no need
// for fancy diff/CRDT machinery. Per-host runtime state (release dirs,
// secrets, certs) stays local — Raft only replicates declarative config.
//
// Failure modes:
//   - Bind fails (port in use): logs WARN, returns nil; the dashboard keeps
//     running standalone. Operators check the log.
//   - Raft init fails: same — degrade to standalone, surface via alert.
//   - Leader is lost mid-write: the in-flight Save returns ErrNotLeader;
//     the caller decides whether to forward (current code path: log + retry
//     after election).
func runClusterNode(ctx context.Context, cc config.Cluster, logger *slog.Logger) (*cluster.Node, error) {
	if cc.NodeID == "" {
		return nil, nil
	}
	dataDir := cc.DataDir
	if dataDir == "" {
		dataDir = filepath.Join(paths.StateDir(), "raft")
	}
	heartbeat := time.Duration(cc.HeartbeatMS) * time.Millisecond
	if heartbeat == 0 {
		heartbeat = time.Second
	}
	peers := make([]cluster.Peer, len(cc.Peers))
	for i, p := range cc.Peers {
		peers[i] = cluster.Peer{NodeID: p.NodeID, Address: p.Address}
	}
	applier := newConfigFSM(logger)
	node, err := cluster.New(cluster.Config{
		NodeID:           cc.NodeID,
		BindAddr:         cc.BindAddr,
		DataDir:          dataDir,
		Bootstrap:        cc.Bootstrap,
		Peers:            peers,
		HeartbeatTimeout: heartbeat,
	}, applier)
	if err != nil {
		return nil, err
	}
	go watchLeadership(ctx, node, logger)
	return node, nil
}

// configFSM is the cluster.FSMApplier that turns a committed proposal back
// into bytes on disk. The proposal payload is the full Config JSON; we
// unmarshal, copy onto the live config struct, and Save() it.
type configFSM struct{ logger *slog.Logger }

func newConfigFSM(logger *slog.Logger) *configFSM { return &configFSM{logger: logger} }

func (f *configFSM) Apply(payload []byte) error {
	var c config.Config
	if err := json.Unmarshal(payload, &c); err != nil {
		return err
	}
	// Land on the canonical config path; flock + atomic rename inside Save()
	// keeps single-host invariants intact.
	live, err := reloadConfig()
	if err != nil {
		return err
	}
	c.SetPath(live.Path())
	if err := c.Save(); err != nil {
		f.logger.Warn("cluster apply: save failed", "err", err)
		return err
	}
	return nil
}

func (f *configFSM) Snapshot() ([]byte, error) {
	live, err := reloadConfig()
	if err != nil {
		return nil, err
	}
	return json.Marshal(live)
}

func (f *configFSM) Restore(payload []byte) error {
	return f.Apply(payload)
}

// watchLeadership logs leadership transitions so operators can correlate
// alerts (the cluster.peer_lost case) with quorum state.
func watchLeadership(ctx context.Context, node *cluster.Node, logger *slog.Logger) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	lastLeader := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			leader := node.LeaderAddr()
			if leader != lastLeader {
				logger.Info("cluster leadership changed",
					slog.String("leader", leader),
					slog.Bool("is_leader", node.IsLeader()))
				lastLeader = leader
			}
		}
	}
}
