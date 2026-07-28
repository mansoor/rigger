package api

import "testing"

// Verbatim `docker node ls --format '{{.ID}}|{{.Hostname}}|{{.Status}}|{{.Availability}}|{{.ManagerStatus}}|{{.EngineVersion}}'`
// from a manager. Note there is NO "*" self marker: docker prints that only in its
// default table output and drops it under --format, which is why the local node is
// identified by matching `docker info`'s .Swarm.NodeID instead.
const nodeLS = `l61fwvuvm7f23btlnxm9y1stv|mgr-1|Ready|Active|Leader|29.6.1
k22abc3def4ghi5jkl6mno7pq|mgr-2|Ready|Active|Reachable|29.6.1
z99xyz8uvw7rst6opq5lmn4kj|worker-1|Ready|Active||29.6.1
y88aaa1bbb2ccc3ddd4eee5ff|worker-2|Down|Drain||28.3.0`

const selfID = "l61fwvuvm7f23btlnxm9y1stv"

func TestParseSwarmNodes(t *testing.T) {
	nodes := parseSwarmNodes(nodeLS, selfID)
	if len(nodes) != 4 {
		t.Fatalf("parsed %d nodes, want 4: %+v", len(nodes), nodes)
	}

	leader := nodes[0]
	if !leader.Self {
		t.Error("the node whose id matches docker info's NodeID is the one we connected to")
	}
	for _, n := range nodes[1:] {
		if n.Self {
			t.Errorf("%s must not be marked Self — only one node matches the local id", n.Hostname)
		}
	}
	if leader.Role != "manager" || !leader.Leader {
		t.Errorf("mgr-1 should be the leading manager, got role=%q leader=%v", leader.Role, leader.Leader)
	}

	// Reachable manager: a manager, but not the leader.
	if nodes[1].Role != "manager" || nodes[1].Leader {
		t.Errorf("mgr-2 should be a non-leader manager, got %+v", nodes[1])
	}
	// Blank ManagerStatus means worker.
	if nodes[2].Role != "worker" || nodes[2].Leader {
		t.Errorf("worker-1 should be a plain worker, got %+v", nodes[2])
	}
	// A drained, down node still belongs to the inventory — that's the point.
	if nodes[3].Status != "Down" || nodes[3].Availability != "Drain" {
		t.Errorf("worker-2 state lost: %+v", nodes[3])
	}
	if nodes[3].Version != "28.3.0" {
		t.Errorf("worker-2 version = %q, want 28.3.0", nodes[3].Version)
	}
}

func TestParseSwarmNodesTolerantOfJunk(t *testing.T) {
	const messy = `
Cannot connect to the Docker daemon
l61fwvuvm7f23btlnxm9y1stv *|mgr-1|Ready|Active|Leader|29.6.1

|||||
short|line
`
	nodes := parseSwarmNodes(messy, selfID)
	if len(nodes) != 1 || nodes[0].Hostname != "mgr-1" {
		t.Fatalf("expected only the one well-formed row, got %+v", nodes)
	}
}

func TestParseSwarmNodesEmpty(t *testing.T) {
	if n := parseSwarmNodes("", selfID); len(n) != 0 {
		t.Fatalf("empty output should yield no nodes, got %+v", n)
	}
}
