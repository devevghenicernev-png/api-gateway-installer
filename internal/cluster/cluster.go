// Package cluster implements optional active-active HA via hashicorp/raft.
//
// Single-host installs (the default) ignore this package entirely. Operators
// who configure `cluster.peers` switch on Raft: one node is leader, all
// nodes serve traffic (read-only on followers + leader-only writes).
//
// State synchronized: apigw config (APIs, Deploys, TLS settings, RBAC,
// audit chain tip, approvals). Per-host state (release directories,
// secrets) stays local — Raft only manages the declarative config.
//
// Why Raft not Postgres-as-source-of-truth: Raft means no external
// dependency. Postgres is an *option* for huge clusters (>5 peers) where
// snapshot replay gets expensive, but bbolt+Raft handles 3-7 peers
// trivially.
package cluster

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
)

// Config controls the cluster wiring.
type Config struct {
	NodeID    string        // unique among peers
	BindAddr  string        // host:port the Raft transport listens on
	DataDir   string        // <StateDir>/raft/
	Peers     []Peer        // bootstrap peers; first run only
	Bootstrap bool          // first node in a fresh cluster
	HeartbeatTimeout time.Duration
}

// Peer is one cluster member.
type Peer struct {
	NodeID  string
	Address string
}

// Node bundles the raft.Raft + the FSM that applies committed log entries
// to local state.
type Node struct {
	cfg  Config
	raft *raft.Raft
	fsm  *fsm

	mu     sync.RWMutex
	closer []func() error
}

// FSMApplier is what the caller plugs in — receives every committed config
// change. Typical impl: deserialize, apply to in-memory config, persist
// snapshot, and emit events.Hub messages.
type FSMApplier interface {
	Apply(payload []byte) error
	Snapshot() ([]byte, error)
	Restore(payload []byte) error
}

// New constructs a Raft node. Caller supplies the FSMApplier that decides
// how committed entries land on disk.
func New(cfg Config, applier FSMApplier) (*Node, error) {
	if cfg.NodeID == "" {
		return nil, errors.New("cluster: NodeID required")
	}
	if cfg.DataDir == "" {
		return nil, errors.New("cluster: DataDir required")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	if cfg.HeartbeatTimeout == 0 {
		cfg.HeartbeatTimeout = time.Second
	}

	raftCfg := raft.DefaultConfig()
	raftCfg.LocalID = raft.ServerID(cfg.NodeID)
	raftCfg.HeartbeatTimeout = cfg.HeartbeatTimeout
	raftCfg.ElectionTimeout = 2 * cfg.HeartbeatTimeout
	raftCfg.LeaderLeaseTimeout = cfg.HeartbeatTimeout

	addr, err := net.ResolveTCPAddr("tcp", cfg.BindAddr)
	if err != nil {
		return nil, fmt.Errorf("cluster: resolve bind addr: %w", err)
	}
	tport, err := raft.NewTCPTransport(cfg.BindAddr, addr, 3, 10*time.Second, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("cluster: TCP transport: %w", err)
	}

	logStore, err := raftboltdb.NewBoltStore(filepath.Join(cfg.DataDir, "log.db"))
	if err != nil {
		return nil, fmt.Errorf("cluster: log store: %w", err)
	}
	stableStore, err := raftboltdb.NewBoltStore(filepath.Join(cfg.DataDir, "stable.db"))
	if err != nil {
		return nil, fmt.Errorf("cluster: stable store: %w", err)
	}
	snapStore, err := raft.NewFileSnapshotStore(cfg.DataDir, 3, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("cluster: snapshot store: %w", err)
	}

	fsmImpl := &fsm{applier: applier}
	r, err := raft.NewRaft(raftCfg, fsmImpl, logStore, stableStore, snapStore, tport)
	if err != nil {
		return nil, fmt.Errorf("cluster: raft init: %w", err)
	}

	if cfg.Bootstrap {
		servers := []raft.Server{{ID: raftCfg.LocalID, Address: tport.LocalAddr()}}
		for _, p := range cfg.Peers {
			if p.NodeID == cfg.NodeID {
				continue
			}
			servers = append(servers, raft.Server{
				ID:      raft.ServerID(p.NodeID),
				Address: raft.ServerAddress(p.Address),
			})
		}
		bootstrap := raft.Configuration{Servers: servers}
		if err := r.BootstrapCluster(bootstrap).Error(); err != nil && err != raft.ErrCantBootstrap {
			return nil, fmt.Errorf("cluster: bootstrap: %w", err)
		}
	}

	return &Node{cfg: cfg, raft: r, fsm: fsmImpl}, nil
}

// IsLeader reports whether this node currently holds leadership.
// Followers should reject local mutations and forward to LeaderAddr().
func (n *Node) IsLeader() bool { return n.raft.State() == raft.Leader }

// LeaderAddr returns the current leader's transport address, or empty
// while an election is in progress.
func (n *Node) LeaderAddr() string { return string(n.raft.Leader()) }

// Propose submits a config change to the cluster. Only the leader can
// commit; followers return ErrNotLeader so the caller can forward.
func (n *Node) Propose(payload []byte, timeout time.Duration) error {
	if !n.IsLeader() {
		return ErrNotLeader
	}
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	f := n.raft.Apply(payload, timeout)
	if err := f.Error(); err != nil {
		return fmt.Errorf("cluster apply: %w", err)
	}
	return nil
}

// AddPeer is leader-only; brings a new node into the cluster.
func (n *Node) AddPeer(nodeID, addr string) error {
	if !n.IsLeader() {
		return ErrNotLeader
	}
	f := n.raft.AddVoter(raft.ServerID(nodeID), raft.ServerAddress(addr), 0, 5*time.Second)
	return f.Error()
}

// RemovePeer is leader-only.
func (n *Node) RemovePeer(nodeID string) error {
	if !n.IsLeader() {
		return ErrNotLeader
	}
	f := n.raft.RemoveServer(raft.ServerID(nodeID), 0, 5*time.Second)
	return f.Error()
}

// Shutdown gracefully closes the Raft instance. Followers degrade quickly;
// leader transfers leadership first.
func (n *Node) Shutdown() error {
	if n.IsLeader() {
		_ = n.raft.LeadershipTransfer().Error()
	}
	return n.raft.Shutdown().Error()
}

// ErrNotLeader is returned when a write hits a follower.
var ErrNotLeader = errors.New("cluster: not leader (forward to leader)")
