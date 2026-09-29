package main

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
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

func TestPodsForReplicaSetOnlyReturnsOwnedPods(t *testing.T) {
	ctx := context.Background()
	namespace := "apps"
	labels := map[string]string{"app": "demo"}
	replicaSet := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-rs", Namespace: namespace, UID: types.UID("demo-rs-uid")},
		Spec: appsv1.ReplicaSetSpec{
			Replicas: pointerTo(int32(2)),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}},
		},
		Status: appsv1.ReplicaSetStatus{Replicas: 2, ReadyReplicas: 1},
	}
	ownedPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "demo-rs-owned", Namespace: namespace, Labels: labels,
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: replicaSet.Name, UID: replicaSet.UID}},
		},
	}
	otherPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "demo-rs-other", Namespace: namespace, Labels: labels,
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "other-rs", UID: types.UID("other-rs-uid")}},
		},
	}
	clientset := fake.NewSimpleClientset(replicaSet, ownedPod, otherPod)
	pods, err := podsForReplicaSet(ctx, clientset, replicaSet)
	if err != nil {
		t.Fatalf("podsForReplicaSet returned error: %v", err)
	}
	if len(pods) != 1 || pods[0].Name != ownedPod.Name {
		t.Fatalf("podsForReplicaSet returned %v, want only %q", podNames(pods), ownedPod.Name)
	}
}

func podNames(pods []corev1.Pod) []string {
	names := make([]string, 0, len(pods))
	for _, pod := range pods {
		names = append(names, pod.Name)
	}
	return names
}

func pointerTo[T any](value T) *T {
	return &value
}
