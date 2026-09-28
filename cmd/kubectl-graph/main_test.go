package main

import (
	"strings"
	"testing"
)

func TestRenderMermaidGroupsEdgesByDestination(t *testing.T) {
	g := newGraph()
	service := g.addNode("Service", "kube-system", "cilium-envoy")
	daemonSet := g.addNode("DaemonSet", "kube-system", "cilium-envoy")
	podA := g.addNode("Pod", "kube-system", "cilium-envoy-a")
	podB := g.addNode("Pod", "kube-system", "cilium-envoy-b")

	g.addEdge(service, podA)
	g.addEdge(daemonSet, podA)
	g.addEdge(service, podB)
	g.addEdge(daemonSet, podB)

	output := renderMermaid(g)
	lines := strings.Split(output, "\n")
	edgesByPod := map[string][]string{
		podA: {"  " + daemonSet + " --> " + podA, "  " + service + " -.-> " + podA},
		podB: {"  " + daemonSet + " --> " + podB, "  " + service + " -.-> " + podB},
	}

	for pod, expected := range edgesByPod {
		first, last := -1, -1
		for i, line := range lines {
			if strings.HasSuffix(line, " "+pod) {
				if first == -1 {
					first = i
				}
				last = i
			}
		}
		if first == -1 || last-first != len(expected)-1 {
			t.Fatalf("edges to %s are not grouped:\n%s", pod, output)
		}
		for _, line := range expected {
			if !strings.Contains(output, line) {
				t.Errorf("missing edge %q in Mermaid output", line)
			}
		}
	}
}