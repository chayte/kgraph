package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/pflag"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type options struct {
	namespace string
	output    string
}

type node struct {
	Kind    string
	ID      string
	Label   string
	Details string // extra info lines for mermaid/tree, joined with "<br/>"
}

type edge struct {
	From  string
	To    string
	Label string
}

type graph struct {
	nodes map[string]node
	edges map[string]edge
}

func newGraph() *graph {
	return &graph{
		nodes: map[string]node{},
		edges: map[string]edge{},
	}
}

func (g *graph) addNode(kind, namespace, name string) string {
	return g.addNodeWithDetails(kind, namespace, name, "")
}

func (g *graph) addNodeWithDetails(kind, namespace, name, details string) string {
	id := fmt.Sprintf("%s_%s_%s", sanitize(kind), sanitize(namespace), sanitize(name))
	label := fmt.Sprintf("%s\\n%s", kind, name)
	g.nodes[id] = node{Kind: kind, ID: id, Label: label, Details: details}
	return id
}

func renderMermaidNodeLine(n node) string {
	label := n.Label
	if n.Details != "" {
		label = label + "<br/>" + n.Details
	}
	label = strings.ReplaceAll(label, "\"", "#quot;")

	// Keep workload nodes rectangular, use rounded/stadium nodes for networking objects.
	if n.Kind == "Service" || n.Kind == "Ingress" {
		return fmt.Sprintf("  %s([\"%s\"])", n.ID, label)
	}
	return fmt.Sprintf("  %s[\"%s\"]", n.ID, label)
}

func (g *graph) addEdge(from, to string) {
	g.addEdgeWithLabel(from, to, "")
}

func (g *graph) addEdgeWithLabel(from, to, label string) {
	key := from + "->" + to
	if existing, ok := g.edges[key]; ok {
		if label == "" || existing.Label == label {
			return
		}
		if existing.Label == "" {
			existing.Label = label
		} else {
			existing.Label = existing.Label + "; " + label
		}
		g.edges[key] = existing
		return
	}
	g.edges[key] = edge{From: from, To: to, Label: label}
}

func sanitize(v string) string {
	v = strings.ToLower(v)
	v = strings.ReplaceAll(v, "-", "_")
	v = strings.ReplaceAll(v, ".", "_")
	v = strings.ReplaceAll(v, "/", "_")
	if v == "" {
		return "x"
	}
	return v
}

func main() {
	root := pflag.NewFlagSet("kubectl-graph", pflag.ExitOnError)
	rootOpts := options{}
	root.StringVarP(&rootOpts.namespace, "namespace", "n", "default", "namespace")
	root.StringVar(&rootOpts.output, "output", "ascii", "output format: ascii|mermaid|tree|json")
	_ = root.Parse(os.Args[1:])

	args := root.Args()
	if len(args) < 1 {
		printUsage()
		os.Exit(1)
	}

	switch args[0] {
	case "ingress", "ing":
		runIngress(args[1:], rootOpts)
	case "service", "svc":
		runService(args[1:], rootOpts)
	case "deployment", "deploy":
		runDeployment(args[1:], rootOpts)
	case "statefulset", "sts":
		runStatefulSet(args[1:], rootOpts)
	case "job":
		runJob(args[1:], rootOpts)
	case "cronjob", "cron", "cj":
		runCronJob(args[1:], rootOpts)
	case "gateway", "gtw":
		runGateway(args[1:], rootOpts)
	case "httproute":
		runHTTPRoute(args[1:], rootOpts)
	case "tcproute":
		runTCPRoute(args[1:], rootOpts)
	case "udproute":
		runUDPRoute(args[1:], rootOpts)
	case "tlsroute":
		runTLSRoute(args[1:], rootOpts)
	case "grpcroute":
		runGRPCRoute(args[1:], rootOpts)
	case "pod", "pods", "po":
		runPod(args[1:], rootOpts)
	case "pvc":
		runPVC(args[1:], rootOpts)
	case "role":
		runRole(args[1:], rootOpts)
	case "rolebinding", "rb":
		runRoleBinding(args[1:], rootOpts)
	case "clusterrole", "cr":
		runClusterRole(args[1:], rootOpts)
	case "clusterrolebinding", "crb":
		runClusterRoleBinding(args[1:], rootOpts)
	case "completion":
		runCompletion(args[1:])
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", args[0])
		printUsage()
		os.Exit(1)
	}
}

func runPod(args []string, base options) {
	fs := pflag.NewFlagSet("pod", pflag.ExitOnError)
	opts := base
	fs.StringVarP(&opts.namespace, "namespace", "n", base.namespace, "namespace")
	fs.StringVar(&opts.output, "output", base.output, "output format: ascii|mermaid|tree|json")
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "missing pod name")
		fmt.Fprintln(os.Stderr, "usage: kubectl-graph pod <name> [-n namespace] [--output ascii|mermaid|tree|json]")
		os.Exit(1)
	}
	podName := fs.Arg(0)

	clientset, err := buildClientset()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build kubernetes client: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	switch opts.output {
	case "ascii":
		out, err := renderPodASCII(ctx, clientset, opts.namespace, podName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(highlightMissingMarkers(out))
	case "mermaid":
		gr, err := buildPodGraph(ctx, clientset, opts.namespace, podName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(renderMermaid(gr))
	case "tree":
		gr, err := buildPodGraph(ctx, clientset, opts.namespace, podName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(renderTree(gr))
	case "json":
		gr, err := buildPodGraph(ctx, clientset, opts.namespace, podName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(renderGraphJSON(gr), "", "  ")
		fmt.Println(string(data))
	default:
		fmt.Fprintf(os.Stderr, "unsupported output: %s\n", opts.output)
		os.Exit(1)
	}
}

func runPVC(args []string, base options) {
	fs := pflag.NewFlagSet("pvc", pflag.ExitOnError)
	opts := base
	fs.StringVarP(&opts.namespace, "namespace", "n", base.namespace, "namespace")
	fs.StringVar(&opts.output, "output", base.output, "output format: ascii|mermaid|tree|json")
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "missing pvc name")
		fmt.Fprintln(os.Stderr, "usage: kubectl-graph pvc <name> [-n namespace] [--output ascii|mermaid|tree|json]")
		os.Exit(1)
	}
	pvcName := fs.Arg(0)

	clientset, err := buildClientset()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build kubernetes client: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	switch opts.output {
	case "ascii":
		out, err := renderPVCASCII(ctx, clientset, opts.namespace, pvcName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(highlightMissingMarkers(out))
	case "mermaid":
		gr, err := buildPVCGraph(ctx, clientset, opts.namespace, pvcName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(renderMermaid(gr))
	case "tree":
		gr, err := buildPVCGraph(ctx, clientset, opts.namespace, pvcName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(renderTree(gr))
	case "json":
		gr, err := buildPVCGraph(ctx, clientset, opts.namespace, pvcName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(renderGraphJSON(gr), "", "  ")
		fmt.Println(string(data))
	default:
		fmt.Fprintf(os.Stderr, "unsupported output: %s\n", opts.output)
		os.Exit(1)
	}
}

func runStatefulSet(args []string, base options) {
	fs := pflag.NewFlagSet("statefulset", pflag.ExitOnError)
	opts := base
	fs.StringVarP(&opts.namespace, "namespace", "n", base.namespace, "namespace")
	fs.StringVar(&opts.output, "output", base.output, "output format: ascii|mermaid|tree|json")
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "missing statefulset name")
		fmt.Fprintln(os.Stderr, "usage: kubectl-graph statefulset <name> [-n namespace] [--output ascii|mermaid|tree|json]")
		os.Exit(1)
	}
	stsName := fs.Arg(0)

	clientset, err := buildClientset()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build kubernetes client: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	switch opts.output {
	case "ascii":
		out, err := renderStatefulSetASCII(ctx, clientset, opts.namespace, stsName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(highlightMissingMarkers(out))
	case "mermaid":
		gr, err := buildStatefulSetGraph(ctx, clientset, opts.namespace, stsName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(renderMermaid(gr))
	case "tree":
		gr, err := buildStatefulSetGraph(ctx, clientset, opts.namespace, stsName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(renderTree(gr))
	case "json":
		gr, err := buildStatefulSetGraph(ctx, clientset, opts.namespace, stsName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(renderGraphJSON(gr), "", "  ")
		fmt.Println(string(data))
	default:
		fmt.Fprintf(os.Stderr, "unsupported output: %s\n", opts.output)
		os.Exit(1)
	}
}

func runRole(args []string, base options) {
	fs := pflag.NewFlagSet("role", pflag.ExitOnError)
	opts := base
	fs.StringVarP(&opts.namespace, "namespace", "n", base.namespace, "namespace")
	fs.StringVar(&opts.output, "output", base.output, "output format: ascii|mermaid|tree|json")
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "missing role name")
		fmt.Fprintln(os.Stderr, "usage: kubectl-graph role <name> [-n namespace] [--output ascii|mermaid|tree|json]")
		os.Exit(1)
	}
	roleName := fs.Arg(0)

	clientset, err := buildClientset()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build kubernetes client: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	switch opts.output {
	case "ascii":
		out, err := renderRoleASCII(ctx, clientset, opts.namespace, roleName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(highlightMissingMarkers(out))
	case "mermaid", "tree", "json":
		gr, err := buildRoleGraph(ctx, clientset, opts.namespace, roleName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		switch opts.output {
		case "mermaid":
			fmt.Println(renderMermaid(gr))
		case "tree":
			fmt.Println(renderTree(gr))
		case "json":
			data, _ := json.MarshalIndent(renderGraphJSON(gr), "", "  ")
			fmt.Println(string(data))
		}
	default:
		fmt.Fprintf(os.Stderr, "unsupported output: %s\n", opts.output)
		os.Exit(1)
	}
}

func runRoleBinding(args []string, base options) {
	fs := pflag.NewFlagSet("rolebinding", pflag.ExitOnError)
	opts := base
	fs.StringVarP(&opts.namespace, "namespace", "n", base.namespace, "namespace")
	fs.StringVar(&opts.output, "output", base.output, "output format: ascii|mermaid|tree|json")
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "missing rolebinding name")
		fmt.Fprintln(os.Stderr, "usage: kubectl-graph rolebinding <name> [-n namespace] [--output ascii|mermaid|tree|json]")
		os.Exit(1)
	}
	rbName := fs.Arg(0)

	clientset, err := buildClientset()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build kubernetes client: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	switch opts.output {
	case "ascii":
		out, err := renderRoleBindingASCII(ctx, clientset, opts.namespace, rbName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(highlightMissingMarkers(out))
	case "mermaid", "tree", "json":
		gr, err := buildRoleBindingGraph(ctx, clientset, opts.namespace, rbName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		switch opts.output {
		case "mermaid":
			fmt.Println(renderMermaid(gr))
		case "tree":
			fmt.Println(renderTree(gr))
		case "json":
			data, _ := json.MarshalIndent(renderGraphJSON(gr), "", "  ")
			fmt.Println(string(data))
		}
	default:
		fmt.Fprintf(os.Stderr, "unsupported output: %s\n", opts.output)
		os.Exit(1)
	}
}

func runClusterRole(args []string, base options) {
	fs := pflag.NewFlagSet("clusterrole", pflag.ExitOnError)
	opts := base
	fs.StringVar(&opts.output, "output", base.output, "output format: ascii|mermaid|tree|json")
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "missing clusterrole name")
		fmt.Fprintln(os.Stderr, "usage: kubectl-graph clusterrole <name> [--output ascii|mermaid|tree|json]")
		os.Exit(1)
	}
	roleName := fs.Arg(0)

	clientset, err := buildClientset()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build kubernetes client: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	switch opts.output {
	case "ascii":
		out, err := renderClusterRoleASCII(ctx, clientset, roleName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(highlightMissingMarkers(out))
	case "mermaid", "tree", "json":
		gr, err := buildClusterRoleGraph(ctx, clientset, roleName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		switch opts.output {
		case "mermaid":
			fmt.Println(renderMermaid(gr))
		case "tree":
			fmt.Println(renderTree(gr))
		case "json":
			data, _ := json.MarshalIndent(renderGraphJSON(gr), "", "  ")
			fmt.Println(string(data))
		}
	default:
		fmt.Fprintf(os.Stderr, "unsupported output: %s\n", opts.output)
		os.Exit(1)
	}
}

func runClusterRoleBinding(args []string, base options) {
	fs := pflag.NewFlagSet("clusterrolebinding", pflag.ExitOnError)
	opts := base
	fs.StringVar(&opts.output, "output", base.output, "output format: ascii|mermaid|tree|json")
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "missing clusterrolebinding name")
		fmt.Fprintln(os.Stderr, "usage: kubectl-graph clusterrolebinding <name> [--output ascii|mermaid|tree|json]")
		os.Exit(1)
	}
	crbName := fs.Arg(0)

	clientset, err := buildClientset()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build kubernetes client: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	switch opts.output {
	case "ascii":
		out, err := renderClusterRoleBindingASCII(ctx, clientset, crbName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(highlightMissingMarkers(out))
	case "mermaid", "tree", "json":
		gr, err := buildClusterRoleBindingGraph(ctx, clientset, crbName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		switch opts.output {
		case "mermaid":
			fmt.Println(renderMermaid(gr))
		case "tree":
			fmt.Println(renderTree(gr))
		case "json":
			data, _ := json.MarshalIndent(renderGraphJSON(gr), "", "  ")
			fmt.Println(string(data))
		}
	default:
		fmt.Fprintf(os.Stderr, "unsupported output: %s\n", opts.output)
		os.Exit(1)
	}
}

func runIngress(args []string, base options) {
	fs := pflag.NewFlagSet("ingress", pflag.ExitOnError)
	opts := base
	fs.StringVarP(&opts.namespace, "namespace", "n", base.namespace, "namespace")
	fs.StringVar(&opts.output, "output", base.output, "output format: ascii|mermaid|tree|json")
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "missing ingress name")
		fmt.Fprintln(os.Stderr, "usage: kubectl-graph ingress <name> [-n namespace] [--output ascii|mermaid|tree|json]")
		os.Exit(1)
	}
	ingressName := fs.Arg(0)

	clientset, err := buildClientset()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build kubernetes client: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	switch opts.output {
	case "ascii":
		out, err := renderIngressASCII(ctx, clientset, opts.namespace, ingressName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(highlightMissingMarkers(out))
	case "mermaid":
		gr, err := buildIngressGraph(ctx, clientset, opts.namespace, ingressName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(renderMermaid(gr))
	case "tree":
		gr, err := buildIngressGraph(ctx, clientset, opts.namespace, ingressName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(renderTree(gr))
	case "json":
		gr, err := buildIngressGraph(ctx, clientset, opts.namespace, ingressName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(renderGraphJSON(gr), "", "  ")
		fmt.Println(string(data))
	default:
		fmt.Fprintf(os.Stderr, "unsupported output: %s\n", opts.output)
		os.Exit(1)
	}
}

func runService(args []string, base options) {
	fs := pflag.NewFlagSet("service", pflag.ExitOnError)
	opts := base
	fs.StringVarP(&opts.namespace, "namespace", "n", base.namespace, "namespace")
	fs.StringVar(&opts.output, "output", base.output, "output format: ascii|mermaid|tree|json")
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "missing service name")
		fmt.Fprintln(os.Stderr, "usage: kubectl-graph service <name> [-n namespace] [--output ascii|mermaid|tree|json]")
		os.Exit(1)
	}
	serviceName := fs.Arg(0)

	clientset, err := buildClientset()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build kubernetes client: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	switch opts.output {
	case "ascii":
		out, err := renderServiceASCII(ctx, clientset, opts.namespace, serviceName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(highlightMissingMarkers(out))
	case "mermaid":
		gr, err := buildServiceGraph(ctx, clientset, opts.namespace, serviceName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(renderMermaid(gr))
	case "tree":
		gr, err := buildServiceGraph(ctx, clientset, opts.namespace, serviceName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(renderTree(gr))
	case "json":
		gr, err := buildServiceGraph(ctx, clientset, opts.namespace, serviceName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(renderGraphJSON(gr), "", "  ")
		fmt.Println(string(data))
	default:
		fmt.Fprintf(os.Stderr, "unsupported output: %s\n", opts.output)
		os.Exit(1)
	}
}

func runDeployment(args []string, base options) {
	fs := pflag.NewFlagSet("deployment", pflag.ExitOnError)
	opts := base
	fs.StringVarP(&opts.namespace, "namespace", "n", base.namespace, "namespace")
	fs.StringVar(&opts.output, "output", base.output, "output format: ascii|mermaid|tree|json")
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "missing deployment name")
		fmt.Fprintln(os.Stderr, "usage: kubectl-graph deployment <name> [-n namespace] [--output ascii|mermaid|tree|json]")
		os.Exit(1)
	}
	deploymentName := fs.Arg(0)

	clientset, err := buildClientset()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build kubernetes client: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	switch opts.output {
	case "ascii":
		out, err := renderDeploymentASCII(ctx, clientset, opts.namespace, deploymentName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(highlightMissingMarkers(out))
	case "mermaid":
		gr, err := buildDeploymentGraph(ctx, clientset, opts.namespace, deploymentName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(renderMermaid(gr))
	case "tree":
		gr, err := buildDeploymentGraph(ctx, clientset, opts.namespace, deploymentName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(renderTree(gr))
	case "json":
		gr, err := buildDeploymentGraph(ctx, clientset, opts.namespace, deploymentName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(renderGraphJSON(gr), "", "  ")
		fmt.Println(string(data))
	default:
		fmt.Fprintf(os.Stderr, "unsupported output: %s\n", opts.output)
		os.Exit(1)
	}
}

func runJob(args []string, base options) {
	fs := pflag.NewFlagSet("job", pflag.ExitOnError)
	opts := base
	fs.StringVarP(&opts.namespace, "namespace", "n", base.namespace, "namespace")
	fs.StringVar(&opts.output, "output", base.output, "output format: ascii|mermaid|tree|json")
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "missing job name")
		fmt.Fprintln(os.Stderr, "usage: kubectl-graph job <name> [-n namespace] [--output ascii|mermaid|tree|json]")
		os.Exit(1)
	}
	jobName := fs.Arg(0)

	clientset, err := buildClientset()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build kubernetes client: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	switch opts.output {
	case "ascii":
		out, err := renderJobASCII(ctx, clientset, opts.namespace, jobName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(highlightMissingMarkers(out))
	case "mermaid":
		gr, err := buildJobGraph(ctx, clientset, opts.namespace, jobName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(renderMermaid(gr))
	case "tree":
		gr, err := buildJobGraph(ctx, clientset, opts.namespace, jobName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(renderTree(gr))
	case "json":
		gr, err := buildJobGraph(ctx, clientset, opts.namespace, jobName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(renderGraphJSON(gr), "", "  ")
		fmt.Println(string(data))
	default:
		fmt.Fprintf(os.Stderr, "unsupported output: %s\n", opts.output)
		os.Exit(1)
	}
}

func runCronJob(args []string, base options) {
	fs := pflag.NewFlagSet("cronjob", pflag.ExitOnError)
	opts := base
	fs.StringVarP(&opts.namespace, "namespace", "n", base.namespace, "namespace")
	fs.StringVar(&opts.output, "output", base.output, "output format: ascii|mermaid|tree|json")
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "missing cronjob name")
		fmt.Fprintln(os.Stderr, "usage: kubectl-graph cronjob <name> [-n namespace] [--output ascii|mermaid|tree|json]")
		os.Exit(1)
	}
	cronJobName := fs.Arg(0)

	clientset, err := buildClientset()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build kubernetes client: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	switch opts.output {
	case "ascii":
		out, err := renderCronJobASCII(ctx, clientset, opts.namespace, cronJobName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(highlightMissingMarkers(out))
	case "mermaid":
		gr, err := buildCronJobGraph(ctx, clientset, opts.namespace, cronJobName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(renderMermaid(gr))
	case "tree":
		gr, err := buildCronJobGraph(ctx, clientset, opts.namespace, cronJobName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(renderTree(gr))
	case "json":
		gr, err := buildCronJobGraph(ctx, clientset, opts.namespace, cronJobName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(renderGraphJSON(gr), "", "  ")
		fmt.Println(string(data))
	default:
		fmt.Fprintf(os.Stderr, "unsupported output: %s\n", opts.output)
		os.Exit(1)
	}
}

func runGateway(args []string, base options) {
	fs := pflag.NewFlagSet("gateway", pflag.ExitOnError)
	opts := base
	fs.StringVarP(&opts.namespace, "namespace", "n", base.namespace, "namespace")
	fs.StringVar(&opts.output, "output", base.output, "output format: ascii|mermaid|tree|json")
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "missing gateway name")
		fmt.Fprintln(os.Stderr, "usage: kubectl-graph gateway <name> [-n namespace] [--output ascii|mermaid|tree|json]")
		os.Exit(1)
	}
	gatewayName := fs.Arg(0)

	_, err := buildClientset()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build kubernetes client: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	switch opts.output {
	case "ascii":
		out, err := renderGatewayASCII(ctx, opts.namespace, gatewayName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(highlightMissingMarkers(out))
	case "mermaid", "tree", "json":
		gr, err := buildGatewayGraph(ctx, opts.namespace, gatewayName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		switch opts.output {
		case "mermaid":
			fmt.Println(renderMermaid(gr))
		case "tree":
			fmt.Println(renderTree(gr))
		case "json":
			data, _ := json.MarshalIndent(renderGraphJSON(gr), "", "  ")
			fmt.Println(string(data))
		}
	default:
		fmt.Fprintf(os.Stderr, "unsupported output: %s\n", opts.output)
		os.Exit(1)
	}
}

func runHTTPRoute(args []string, base options) { runRoute(args, base, "httproute", "HTTPRoute") }
func runTCPRoute(args []string, base options)  { runRoute(args, base, "tcproute", "TCPRoute") }
func runUDPRoute(args []string, base options)  { runRoute(args, base, "udproute", "UDPRoute") }
func runTLSRoute(args []string, base options)  { runRoute(args, base, "tlsroute", "TLSRoute") }
func runGRPCRoute(args []string, base options) { runRoute(args, base, "grpcroute", "GRPCRoute") }

func runRoute(args []string, base options, shortName, typeName string) {
	fs := pflag.NewFlagSet(shortName, pflag.ExitOnError)
	opts := base
	fs.StringVarP(&opts.namespace, "namespace", "n", base.namespace, "namespace")
	fs.StringVar(&opts.output, "output", base.output, "output format: ascii|mermaid|tree|json")
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "missing %s name\n", strings.ToLower(typeName))
		fmt.Fprintf(os.Stderr, "usage: kubectl-graph %s <name> [-n namespace] [--output ascii|mermaid|tree|json]\n", shortName)
		os.Exit(1)
	}
	routeName := fs.Arg(0)

	ctx := context.Background()
	switch opts.output {
	case "ascii":
		out, err := renderRouteASCII(ctx, opts.namespace, routeName, typeName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(highlightMissingMarkers(out))
	case "mermaid", "tree", "json":
		gr, err := buildRouteGraph(ctx, opts.namespace, routeName, typeName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build graph: %v\n", err)
			os.Exit(1)
		}
		switch opts.output {
		case "mermaid":
			fmt.Println(renderMermaid(gr))
		case "tree":
			fmt.Println(renderTree(gr))
		case "json":
			data, _ := json.MarshalIndent(renderGraphJSON(gr), "", "  ")
			fmt.Println(string(data))
		}
	default:
		fmt.Fprintf(os.Stderr, "unsupported output: %s\n", opts.output)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`kubectl-graph - Kubernetes dependency graph helper

Usage:
	kubectl-graph [-n namespace] [--output ascii|mermaid|tree|json] <command>
	kubectl-graph ingress|ing <name> [-n namespace] [--output ascii|mermaid|tree|json]
	kubectl-graph service|svc <name> [-n namespace] [--output ascii|mermaid|tree|json]
	kubectl-graph deployment <name> [-n namespace] [--output ascii|mermaid|tree|json]
	kubectl-graph deploy <name> [-n namespace] [--output ascii|mermaid|tree|json]
	kubectl-graph statefulset <name> [-n namespace] [--output ascii|mermaid|tree|json]
	kubectl-graph sts <name> [-n namespace] [--output ascii|mermaid|tree|json]
	kubectl-graph job <name> [-n namespace] [--output ascii|mermaid|tree|json]
	kubectl-graph cronjob|cj <name> [-n namespace] [--output ascii|mermaid|tree|json]
	kubectl-graph cron <name> [-n namespace] [--output ascii|mermaid|tree|json]
	kubectl-graph gateway|gtw <name> [-n namespace] [--output ascii|mermaid|tree|json]
	kubectl-graph httproute <name> [-n namespace] [--output ascii|mermaid|tree|json]
	kubectl-graph tcproute <name> [-n namespace] [--output ascii|mermaid|tree|json]
	kubectl-graph udproute <name> [-n namespace] [--output ascii|mermaid|tree|json]
	kubectl-graph tlsroute <name> [-n namespace] [--output ascii|mermaid|tree|json]
	kubectl-graph grpcroute <name> [-n namespace] [--output ascii|mermaid|tree|json]
	kubectl-graph pod|po <name> [-n namespace] [--output ascii|mermaid|tree|json]
	kubectl-graph pvc <name> [-n namespace] [--output ascii|mermaid|tree|json]
	kubectl-graph role <name> [-n namespace] [--output ascii|mermaid|tree|json]
	kubectl-graph rolebinding|rb <name> [-n namespace] [--output ascii|mermaid|tree|json]
	kubectl-graph clusterrole|cr <name> [--output ascii|mermaid|tree|json]
	kubectl-graph clusterrolebinding|crb <name> [--output ascii|mermaid|tree|json]
	kubectl-graph completion <zsh|bash>

Examples:
  kubectl graph ingress web -n prod
	kubectl graph ing web -n prod
	kubectl graph svc web-svc -n prod
	kubectl graph service web-svc -n prod
	kubectl graph deployment web -n prod
	kubectl graph statefulset db -n prod
	kubectl graph job db-migration-001 -n prod
	kubectl graph cronjob nightly-backup -n prod
	kubectl graph pod my-pod -n prod
	kubectl graph pvc data-pvc -n prod
	kubectl graph role admin -n prod
	kubectl graph rolebinding admin-binding -n prod
	kubectl graph clusterrole cluster-admin
	kubectl graph clusterrolebinding cluster-admin-binding
	kubectl graph ingress web -n prod --output ascii
	kubectl graph ingress web -n prod --output tree
	kubectl graph ingress web -n prod --output json
	kubectl-graph completion zsh`)
}

func runCompletion(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "missing shell name")
		fmt.Fprintln(os.Stderr, "usage: kubectl-graph completion <zsh|bash>")
		os.Exit(1)
	}

	switch args[0] {
	case "zsh":
		fmt.Print(zshCompletionScript)
	case "bash":
		fmt.Print(bashCompletionScript)
	default:
		fmt.Fprintf(os.Stderr, "unsupported shell: %s\n", args[0])
		fmt.Fprintln(os.Stderr, "usage: kubectl-graph completion <zsh|bash>")
		os.Exit(1)
	}
}

type ingressBackend struct {
	Host        string
	Path        string
	ServiceName string
	ServicePort string
}

func styleValue(v string) string {
	const ansiBold = "\x1b[1m"
	const ansiReset = "\x1b[0m"
	if v == "" || os.Getenv("NO_COLOR") != "" {
		return v
	}
	if isMissingIndicator(v) {
		return styleMissing(v)
	}
	return ansiBold + v + ansiReset
}

func styleMissing(v string) string {
	if v == "" || os.Getenv("NO_COLOR") != "" {
		return v
	}
	const (
		ansiReset  = "\x1b[0m"
		ansiBold   = "\x1b[1m"
		ansiYellow = "\x1b[33m"
	)
	return ansiBold + ansiYellow + v + ansiReset
}

func isMissingIndicator(v string) bool {
	s := strings.ToLower(strings.TrimSpace(v))
	s = strings.Trim(s, "()")
	return s == "none" || s == "<none>" || s == "<unknown>" || s == "not found"
}

func highlightMissingMarkers(out string) string {
	if os.Getenv("NO_COLOR") != "" {
		return out
	}
	out = strings.ReplaceAll(out, "(not found)", styleMissing("(not found)"))
	out = strings.ReplaceAll(out, ": none\n", ": "+styleMissing("none")+"\n")
	out = strings.ReplaceAll(out, "`-- none\n", "`-- "+styleMissing("none")+"\n")
	out = strings.ReplaceAll(out, "|-- none\n", "|-- "+styleMissing("none")+"\n")
	return out
}

func stylePhase(phase corev1.PodPhase) string {
	if os.Getenv("NO_COLOR") != "" {
		return string(phase)
	}

	const (
		ansiReset  = "\x1b[0m"
		ansiBold   = "\x1b[1m"
		ansiRed    = "\x1b[31m"
		ansiGreen  = "\x1b[32m"
		ansiYellow = "\x1b[33m"
	)

	color := ansiYellow
	switch phase {
	case corev1.PodRunning, corev1.PodSucceeded:
		color = ansiGreen
	case corev1.PodFailed:
		color = ansiRed
	case corev1.PodPending, corev1.PodUnknown:
		color = ansiYellow
	}

	return ansiBold + color + string(phase) + ansiReset
}

func styleType(v string) string {
	return v
}

func styleJoinedValues(values []string) string {
	if len(values) == 0 {
		return ""
	}
	styled := make([]string, 0, len(values))
	for _, value := range values {
		styled = append(styled, styleValue(value))
	}
	return strings.Join(styled, ", ")
}

func formatOwnerValue(kind, name string) string {
	return kind + "/" + name
}

func renderIngressASCII(ctx context.Context, clientset *kubernetes.Clientset, namespace, ingressName string) (string, error) {
	ing, err := clientset.NetworkingV1().Ingresses(namespace).Get(ctx, ingressName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Namespace: %s\n", styleValue(ing.Namespace))
	fmt.Fprintf(&b, "%s: %s\n", styleType("Ingress"), styleValue(ing.Name))

	className := "<none>"
	if ing.Spec.IngressClassName != nil && *ing.Spec.IngressClassName != "" {
		className = *ing.Spec.IngressClassName
	}
	fmt.Fprintf(&b, "|-- Class: %s\n", styleValue(className))

	addresses := ingressAddresses(ing)
	if len(addresses) > 0 {
		fmt.Fprintf(&b, "|-- Addresses: %s\n", styleValue(strings.Join(addresses, ", ")))
	}

	backends := ingressBackends(ing)
	if len(backends) == 0 {
		return "", errors.New("ingress does not reference any service backend")
	}

	fmt.Fprintf(&b, "|-- Routes:\n")
	for i, be := range backends {
		branch := "|--"
		if i == len(backends)-1 {
			branch = "`--"
		}
		fmt.Fprintf(&b, "|   %s host=%s path=%s -> backend=%s:%s\n", branch, styleValue(be.Host), styleValue(be.Path), styleValue(be.ServiceName), styleValue(be.ServicePort))
	}

	serviceNames := uniqueServiceNames(backends)
	fmt.Fprintf(&b, "`-- Services:\n")
	for i, svcName := range serviceNames {
		svcLine, svcChildren := branchMarkers("    ", i == len(serviceNames)-1)
		svc, err := clientset.CoreV1().Services(namespace).Get(ctx, svcName, metav1.GetOptions{})
		if err != nil {
			fmt.Fprintf(&b, "%s%s: %s (not found)\n", svcLine, styleType("Service"), styleValue(svcName))
			continue
		}

		fmt.Fprintf(&b, "%s%s: %s\n", svcLine, styleType("Service"), styleValue(svc.Name))
		fmt.Fprintf(&b, "%s|-- Type: %s\n", svcChildren, styleValue(string(svc.Spec.Type)))
		fmt.Fprintf(&b, "%s|-- ClusterIP: %s\n", svcChildren, styleValue(valueOrNone(svc.Spec.ClusterIP)))
		servicePorts := formatServicePorts(svc.Spec.Ports)
		if len(servicePorts) == 0 {
			fmt.Fprintf(&b, "%s|-- Ports: none\n", svcChildren)
		} else {
			fmt.Fprintf(&b, "%s|-- Ports:\n", svcChildren)
			for pi, portLine := range servicePorts {
				portPrefix, _ := branchMarkers(svcChildren+"|   ", pi == len(servicePorts)-1)
				fmt.Fprintf(&b, "%s%s\n", portPrefix, styleValue(portLine))
			}
		}

		endpointIPs, endpointPorts, err := endpointsForService(ctx, clientset, svc.Namespace, svc.Name)
		if err == nil && len(endpointIPs) > 0 {
			fmt.Fprintf(&b, "%s|-- EndpointIPs: %s\n", svcChildren, styleValue(strings.Join(endpointIPs, ", ")))
		}
		if err == nil && len(endpointPorts) > 0 {
			fmt.Fprintf(&b, "%s|-- EndpointPorts: %s\n", svcChildren, styleValue(strings.Join(endpointPorts, ", ")))
		}

		pods, err := podsForService(ctx, clientset, svc)
		if err != nil || len(pods) == 0 {
			fmt.Fprintf(&b, "%s`-- Pods: none\n", svcChildren)
			continue
		}

		sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
		fmt.Fprintf(&b, "%s`-- Pods:\n", svcChildren)
		for j, pod := range pods {
			podLine, podChildren := branchMarkers(svcChildren+"    ", j == len(pods)-1)
			fmt.Fprintf(&b, "%s%s: %s\n", podLine, styleType("Pod"), styleValue(pod.Name))
			fmt.Fprintf(&b, "%s|-- IP: %s\n", podChildren, styleValue(valueOrNone(pod.Status.PodIP)))
			fmt.Fprintf(&b, "%s|-- Phase: %s\n", podChildren, stylePhase(pod.Status.Phase))

			podPorts := formatPodPorts(&pod)
			if len(podPorts) > 0 {
				fmt.Fprintf(&b, "%s|-- Ports:\n", podChildren)
				for pi, portLine := range podPorts {
					portPrefix, _ := branchMarkers(podChildren+"|   ", pi == len(podPorts)-1)
					fmt.Fprintf(&b, "%s%s\n", portPrefix, styleValue(portLine))
				}
			}

			ownerKind, ownerName := resolveWorkloadOwner(ctx, clientset, &pod)
			if ownerName != "" {
				fmt.Fprintf(&b, "%s`-- Owner: %s/%s\n", podChildren, styleType(ownerKind), styleValue(ownerName))
			}
		}
	}

	return b.String(), nil
}

func renderServiceASCII(ctx context.Context, clientset *kubernetes.Clientset, namespace, serviceName string) (string, error) {
	svc, err := clientset.CoreV1().Services(namespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Namespace: %s\n", styleValue(svc.Namespace))
	fmt.Fprintf(&b, "%s: %s\n", styleType("Service"), styleValue(svc.Name))
	fmt.Fprintf(&b, "|-- Type: %s\n", styleValue(string(svc.Spec.Type)))
	fmt.Fprintf(&b, "|-- ClusterIP: %s\n", styleValue(valueOrNone(svc.Spec.ClusterIP)))
	servicePorts := formatServicePorts(svc.Spec.Ports)
	if len(servicePorts) == 0 {
		fmt.Fprintf(&b, "|-- Ports: none\n")
	} else {
		fmt.Fprintf(&b, "|-- Ports:\n")
		for pi, portLine := range servicePorts {
			portPrefix, _ := branchMarkers("|   ", pi == len(servicePorts)-1)
			fmt.Fprintf(&b, "%s%s\n", portPrefix, styleValue(portLine))
		}
	}

	endpointIPs, endpointPorts, err := endpointsForService(ctx, clientset, svc.Namespace, svc.Name)
	if err == nil && len(endpointIPs) > 0 {
		fmt.Fprintf(&b, "|-- EndpointIPs: %s\n", styleValue(strings.Join(endpointIPs, ", ")))
	}
	if err == nil && len(endpointPorts) > 0 {
		fmt.Fprintf(&b, "|-- EndpointPorts: %s\n", styleValue(strings.Join(endpointPorts, ", ")))
	}

	ingresses, err := ingressesForService(ctx, clientset, svc.Namespace, svc.Name)
	if err == nil {
		fmt.Fprintf(&b, "|-- Ingresses:\n")
		if len(ingresses) == 0 {
			fmt.Fprintf(&b, "|   `-- none\n")
		} else {
			for i, ing := range ingresses {
				branch := "|--"
				if i == len(ingresses)-1 {
					branch = "`--"
				}
				fmt.Fprintf(&b, "|   %s %s\n", branch, styleValue(ing))
			}
		}
	}

	pods, err := podsForService(ctx, clientset, svc)
	fmt.Fprintf(&b, "`-- Pods:\n")
	if err != nil || len(pods) == 0 {
		fmt.Fprintf(&b, "    `-- none\n")
		return b.String(), nil
	}

	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	for i, pod := range pods {
		podLine, podChildren := branchMarkers("    ", i == len(pods)-1)
		fmt.Fprintf(&b, "%s%s: %s\n", podLine, styleType("Pod"), styleValue(pod.Name))
		fmt.Fprintf(&b, "%s|-- IP: %s\n", podChildren, styleValue(valueOrNone(pod.Status.PodIP)))
		fmt.Fprintf(&b, "%s|-- Phase: %s\n", podChildren, stylePhase(pod.Status.Phase))
		podPorts := formatPodPorts(&pod)
		if len(podPorts) > 0 {
			fmt.Fprintf(&b, "%s|-- Ports:\n", podChildren)
			for pi, portLine := range podPorts {
				portPrefix, _ := branchMarkers(podChildren+"|   ", pi == len(podPorts)-1)
				fmt.Fprintf(&b, "%s%s\n", portPrefix, styleValue(portLine))
			}
		}
		ownerKind, ownerName := resolveWorkloadOwner(ctx, clientset, &pod)
		if ownerName != "" {
			fmt.Fprintf(&b, "%s`-- Owner: %s/%s\n", podChildren, styleType(ownerKind), styleValue(ownerName))
		}
	}

	return b.String(), nil
}

func renderDeploymentASCII(ctx context.Context, clientset *kubernetes.Clientset, namespace, deploymentName string) (string, error) {
	dep, err := clientset.AppsV1().Deployments(namespace).Get(ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	selector := metav1.FormatLabelSelector(dep.Spec.Selector)
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return "", err
	}
	services, _ := servicesMatchingSelector(ctx, clientset, namespace, dep.Spec.Template.Labels)

	var b strings.Builder
	fmt.Fprintf(&b, "Namespace: %s\n", styleValue(dep.Namespace))
	fmt.Fprintf(&b, "%s: %s\n", styleType("Deployment"), styleValue(dep.Name))
	fmt.Fprintf(&b, "|-- Replicas:\n")
	fmt.Fprintf(&b, "|   |-- desired: %s\n", styleValue(fmt.Sprintf("%d", valueOrZero(dep.Spec.Replicas))))
	fmt.Fprintf(&b, "|   |-- ready: %s\n", styleValue(fmt.Sprintf("%d", dep.Status.ReadyReplicas)))
	fmt.Fprintf(&b, "|   `-- available: %s\n", styleValue(fmt.Sprintf("%d", dep.Status.AvailableReplicas)))
	fmt.Fprintf(&b, "|-- Selector: %s\n", styleValue(selector))
	fmt.Fprintf(&b, "|-- Services:\n")
	if len(services) == 0 {
		fmt.Fprintf(&b, "|   `-- none\n")
	} else {
		for i, svc := range services {
			line, child := branchMarkers("|   ", i == len(services)-1)
			fmt.Fprintf(&b, "%s%s: %s\n", line, styleType("Service"), styleValue(svc.Name))
			fmt.Fprintf(&b, "%s|-- ClusterIP: %s\n", child, styleValue(valueOrNone(svc.Spec.ClusterIP)))
			svcPorts := formatServicePorts(svc.Spec.Ports)
			if len(svcPorts) == 0 {
				fmt.Fprintf(&b, "%s`-- Ports: none\n", child)
			} else {
				fmt.Fprintf(&b, "%s`-- Ports:\n", child)
				for pi, portLine := range svcPorts {
					portPrefix, _ := branchMarkers(child+"    ", pi == len(svcPorts)-1)
					fmt.Fprintf(&b, "%s%s\n", portPrefix, styleValue(portLine))
				}
			}
		}
	}

	fmt.Fprintf(&b, "`-- Pods:\n")
	if len(pods.Items) == 0 {
		fmt.Fprintf(&b, "    `-- none\n")
		return b.String(), nil
	}

	sort.Slice(pods.Items, func(i, j int) bool { return pods.Items[i].Name < pods.Items[j].Name })
	for i, pod := range pods.Items {
		line, child := branchMarkers("    ", i == len(pods.Items)-1)
		fmt.Fprintf(&b, "%s%s: %s\n", line, styleType("Pod"), styleValue(pod.Name))
		fmt.Fprintf(&b, "%s|-- IP: %s\n", child, styleValue(valueOrNone(pod.Status.PodIP)))
		fmt.Fprintf(&b, "%s|-- Phase: %s\n", child, stylePhase(pod.Status.Phase))
		ports := formatPodPorts(&pod)
		if len(ports) > 0 {
			fmt.Fprintf(&b, "%s`-- Ports:\n", child)
			for pi, portLine := range ports {
				portPrefix, _ := branchMarkers(child+"    ", pi == len(ports)-1)
				fmt.Fprintf(&b, "%s%s\n", portPrefix, styleValue(portLine))
			}
		}
	}

	return b.String(), nil
}

func renderJobASCII(ctx context.Context, clientset *kubernetes.Clientset, namespace, jobName string) (string, error) {
	job, err := clientset.BatchV1().Jobs(namespace).Get(ctx, jobName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	pods, err := podsForJob(ctx, clientset, job)
	if err != nil {
		return "", err
	}
	services, _ := servicesMatchingSelector(ctx, clientset, namespace, job.Spec.Template.Labels)

	var b strings.Builder
	fmt.Fprintf(&b, "Namespace: %s\n", styleValue(job.Namespace))
	fmt.Fprintf(&b, "%s: %s\n", styleType("Job"), styleValue(job.Name))
	if job.Spec.Completions != nil {
		fmt.Fprintf(&b, "|-- Completions target: %s\n", styleValue(fmt.Sprintf("%d", *job.Spec.Completions)))
	}
	fmt.Fprintf(&b, "|-- Status: succeeded=%s active=%s failed=%s\n",
		styleValue(fmt.Sprintf("%d", job.Status.Succeeded)),
		styleValue(fmt.Sprintf("%d", job.Status.Active)),
		styleValue(fmt.Sprintf("%d", job.Status.Failed)),
	)
	fmt.Fprintf(&b, "|-- Selector: %s\n", styleValue(selectorForJob(job)))
	fmt.Fprintf(&b, "|-- Services:\n")
	if len(services) == 0 {
		fmt.Fprintf(&b, "|   `-- none\n")
	} else {
		for i, svc := range services {
			line, child := branchMarkers("|   ", i == len(services)-1)
			fmt.Fprintf(&b, "%s%s: %s\n", line, styleType("Service"), styleValue(svc.Name))
			fmt.Fprintf(&b, "%s|-- ClusterIP: %s\n", child, styleValue(valueOrNone(svc.Spec.ClusterIP)))
			svcPorts := formatServicePorts(svc.Spec.Ports)
			if len(svcPorts) == 0 {
				fmt.Fprintf(&b, "%s`-- Ports: none\n", child)
			} else {
				fmt.Fprintf(&b, "%s`-- Ports:\n", child)
				for pi, portLine := range svcPorts {
					portPrefix, _ := branchMarkers(child+"    ", pi == len(svcPorts)-1)
					fmt.Fprintf(&b, "%s%s\n", portPrefix, styleValue(portLine))
				}
			}
		}
	}

	fmt.Fprintf(&b, "`-- Pods:\n")
	if len(pods) == 0 {
		fmt.Fprintf(&b, "    `-- none\n")
		return b.String(), nil
	}

	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	for i, pod := range pods {
		line, child := branchMarkers("    ", i == len(pods)-1)
		fmt.Fprintf(&b, "%s%s: %s\n", line, styleType("Pod"), styleValue(pod.Name))
		fmt.Fprintf(&b, "%s|-- IP: %s\n", child, styleValue(valueOrNone(pod.Status.PodIP)))
		fmt.Fprintf(&b, "%s|-- Phase: %s\n", child, stylePhase(pod.Status.Phase))
		ports := formatPodPorts(&pod)
		if len(ports) > 0 {
			fmt.Fprintf(&b, "%s`-- Ports:\n", child)
			for pi, portLine := range ports {
				portPrefix, _ := branchMarkers(child+"    ", pi == len(ports)-1)
				fmt.Fprintf(&b, "%s%s\n", portPrefix, styleValue(portLine))
			}
		}
	}

	return b.String(), nil
}

func renderCronJobASCII(ctx context.Context, clientset *kubernetes.Clientset, namespace, cronJobName string) (string, error) {
	cj, err := clientset.BatchV1().CronJobs(namespace).Get(ctx, cronJobName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	jobs, err := jobsForCronJob(ctx, clientset, cj)
	if err != nil {
		return "", err
	}
	services, _ := servicesMatchingSelector(ctx, clientset, namespace, cj.Spec.JobTemplate.Spec.Template.Labels)

	var b strings.Builder
	fmt.Fprintf(&b, "Namespace: %s\n", styleValue(cj.Namespace))
	fmt.Fprintf(&b, "%s: %s\n", styleType("CronJob"), styleValue(cj.Name))
	fmt.Fprintf(&b, "|-- Schedule: %s\n", styleValue(cj.Spec.Schedule))
	fmt.Fprintf(&b, "|-- Suspend: %s\n", styleValue(fmt.Sprintf("%t", cj.Spec.Suspend != nil && *cj.Spec.Suspend)))
	fmt.Fprintf(&b, "|-- Jobs:\n")
	if len(jobs) == 0 {
		fmt.Fprintf(&b, "|   `-- none\n")
	} else {
		for i, job := range jobs {
			line, child := branchMarkers("|   ", i == len(jobs)-1)
			fmt.Fprintf(&b, "%s%s: %s\n", line, styleType("Job"), styleValue(job.Name))
			fmt.Fprintf(&b, "%s|-- Status: succeeded=%s active=%s failed=%s\n",
				child,
				styleValue(fmt.Sprintf("%d", job.Status.Succeeded)),
				styleValue(fmt.Sprintf("%d", job.Status.Active)),
				styleValue(fmt.Sprintf("%d", job.Status.Failed)),
			)
			pods, _ := podsForJob(ctx, clientset, &job)
			if len(pods) == 0 {
				fmt.Fprintf(&b, "%s`-- Pods: none\n", child)
				continue
			}
			sort.Slice(pods, func(a, b int) bool { return pods[a].Name < pods[b].Name })
			fmt.Fprintf(&b, "%s`-- Pods:\n", child)
			for pi, pod := range pods {
				podLine, podChild := branchMarkers(child+"    ", pi == len(pods)-1)
				fmt.Fprintf(&b, "%s%s: %s\n", podLine, styleType("Pod"), styleValue(pod.Name))
				fmt.Fprintf(&b, "%s|-- IP: %s\n", podChild, styleValue(valueOrNone(pod.Status.PodIP)))
				fmt.Fprintf(&b, "%s`-- Phase: %s\n", podChild, stylePhase(pod.Status.Phase))
			}
		}
	}

	fmt.Fprintf(&b, "`-- Services:\n")
	if len(services) == 0 {
		fmt.Fprintf(&b, "    `-- none\n")
	} else {
		for i, svc := range services {
			line, child := branchMarkers("    ", i == len(services)-1)
			fmt.Fprintf(&b, "%s%s: %s\n", line, styleType("Service"), styleValue(svc.Name))
			fmt.Fprintf(&b, "%s|-- ClusterIP: %s\n", child, styleValue(valueOrNone(svc.Spec.ClusterIP)))
			svcPorts := formatServicePorts(svc.Spec.Ports)
			if len(svcPorts) == 0 {
				fmt.Fprintf(&b, "%s`-- Ports: none\n", child)
			} else {
				fmt.Fprintf(&b, "%s`-- Ports:\n", child)
				for pi, portLine := range svcPorts {
					portPrefix, _ := branchMarkers(child+"    ", pi == len(svcPorts)-1)
					fmt.Fprintf(&b, "%s%s\n", portPrefix, styleValue(portLine))
				}
			}
		}
	}

	return b.String(), nil
}

func renderPodASCII(ctx context.Context, clientset *kubernetes.Clientset, namespace, podName string) (string, error) {
	pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Namespace: %s\n", styleValue(pod.Namespace))
	fmt.Fprintf(&b, "%s: %s\n", styleType("Pod"), styleValue(pod.Name))
	fmt.Fprintf(&b, "|-- IP: %s\n", styleValue(valueOrNone(pod.Status.PodIP)))
	fmt.Fprintf(&b, "|-- Phase: %s\n", stylePhase(pod.Status.Phase))
	fmt.Fprintf(&b, "|-- Node: %s\n", styleValue(valueOrNone(pod.Spec.NodeName)))
	
	containers := pod.Spec.Containers
	if len(containers) == 0 {
		fmt.Fprintf(&b, "|-- Containers: none\n")
	} else {
		fmt.Fprintf(&b, "|-- Containers:\n")
		for i, container := range containers {
			line, child := branchMarkers("|   ", i == len(containers)-1)
			fmt.Fprintf(&b, "%s%s: %s\n", line, styleType("Container"), styleValue(container.Name))
			fmt.Fprintf(&b, "%s|-- Image: %s\n", child, styleValue(container.Image))
			ports := formatPodPorts(pod)
			if len(ports) == 0 {
				fmt.Fprintf(&b, "%s`-- Ports: none\n", child)
			} else {
				fmt.Fprintf(&b, "%s`-- Ports:\n", child)
				for pi, portLine := range ports {
					portPrefix, _ := branchMarkers(child+"    ", pi == len(ports)-1)
					fmt.Fprintf(&b, "%s%s\n", portPrefix, styleValue(portLine))
				}
			}
		}
	}
	
	services, _ := servicesMatchingSelector(ctx, clientset, namespace, pod.Labels)
	fmt.Fprintf(&b, "`-- Services:\n")
	if len(services) == 0 {
		fmt.Fprintf(&b, "    `-- none\n")
	} else {
		for i, svc := range services {
			line, child := branchMarkers("    ", i == len(services)-1)
			fmt.Fprintf(&b, "%s%s: %s\n", line, styleType("Service"), styleValue(svc.Name))
			fmt.Fprintf(&b, "%s|-- ClusterIP: %s\n", child, styleValue(valueOrNone(svc.Spec.ClusterIP)))
			svcPorts := formatServicePorts(svc.Spec.Ports)
			if len(svcPorts) == 0 {
				fmt.Fprintf(&b, "%s`-- Ports: none\n", child)
			} else {
				fmt.Fprintf(&b, "%s`-- Ports:\n", child)
				for pi, portLine := range svcPorts {
					portPrefix, _ := branchMarkers(child+"    ", pi == len(svcPorts)-1)
					fmt.Fprintf(&b, "%s%s\n", portPrefix, styleValue(portLine))
				}
			}
		}
	}

	return b.String(), nil
}

func buildPodGraph(ctx context.Context, clientset *kubernetes.Clientset, namespace, podName string) (*graph, error) {
	pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	g := newGraph()
	podDetails := fmt.Sprintf("phase: %s | ip: %s", pod.Status.Phase, valueOrNone(pod.Status.PodIP))
	podNode := g.addNodeWithDetails("Pod", pod.Namespace, pod.Name, podDetails)

	services, _ := servicesMatchingSelector(ctx, clientset, namespace, pod.Labels)
	for _, svc := range services {
		svcNode := g.addNodeWithDetails("Service", svc.Namespace, svc.Name, buildServiceDetails(ctx, clientset, &svc))
		svcPortsLabel := buildServicePortsLabel(svc.Spec.Ports)
		g.addEdgeWithLabel(svcNode, podNode, svcPortsLabel)
		
		ingresses, _ := ingressesForService(ctx, clientset, svc.Namespace, svc.Name)
		for _, ingName := range ingresses {
			ingNode := g.addNode("Ingress", svc.Namespace, ingName)
			ingEdgeLabel := ""
			ingObj, err := clientset.NetworkingV1().Ingresses(svc.Namespace).Get(ctx, ingName, metav1.GetOptions{})
			if err == nil {
				ingNode = g.addNodeWithDetails("Ingress", ingObj.Namespace, ingObj.Name, buildIngressDetails(ingObj))
				ingEdgeLabel = ingressRoutesForServiceLabel(ingObj, svc.Name)
			}
			g.addEdgeWithLabel(ingNode, svcNode, ingEdgeLabel)
		}
	}

	return g, nil
}

func buildServiceGraph(ctx context.Context, clientset *kubernetes.Clientset, namespace, serviceName string) (*graph, error) {
	svc, err := clientset.CoreV1().Services(namespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	g := newGraph()
	svcNode := g.addNodeWithDetails("Service", svc.Namespace, svc.Name, buildServiceDetails(ctx, clientset, svc))
	svcPortsLabel := buildServicePortsLabel(svc.Spec.Ports)

	ingresses, _ := ingressesForService(ctx, clientset, svc.Namespace, svc.Name)
	for _, name := range ingresses {
		ingNode := g.addNode("Ingress", svc.Namespace, name)
		ingEdgeLabel := ""
		ingObj, err := clientset.NetworkingV1().Ingresses(svc.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			ingNode = g.addNodeWithDetails("Ingress", ingObj.Namespace, ingObj.Name, buildIngressDetails(ingObj))
			ingEdgeLabel = ingressRoutesForServiceLabel(ingObj, svc.Name)
		}
		g.addEdgeWithLabel(ingNode, svcNode, ingEdgeLabel)
	}

	pods, err := podsForService(ctx, clientset, svc)
	if err == nil {
		sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
		for _, pod := range pods {
			podNode := g.addNodeWithDetails("Pod", pod.Namespace, pod.Name, buildPodDetails(&pod))
			g.addEdgeWithLabel(svcNode, podNode, svcPortsLabel)
			ownerKind, ownerName := resolveWorkloadOwner(ctx, clientset, &pod)
			if ownerName != "" {
				ownerNode := g.addNode(ownerKind, pod.Namespace, ownerName)
				g.addEdge(ownerNode, podNode)
			}
		}
	}

	return g, nil
}

func buildDeploymentGraph(ctx context.Context, clientset *kubernetes.Clientset, namespace, deploymentName string) (*graph, error) {
	dep, err := clientset.AppsV1().Deployments(namespace).Get(ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	g := newGraph()
	depDetails := fmt.Sprintf("desired: %d | ready: %d | available: %d",
		valueOrZero(dep.Spec.Replicas), dep.Status.ReadyReplicas, dep.Status.AvailableReplicas)
	depNode := g.addNodeWithDetails("Deployment", dep.Namespace, dep.Name, depDetails)

	selector := metav1.FormatLabelSelector(dep.Spec.Selector)
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err == nil {
		sort.Slice(pods.Items, func(i, j int) bool { return pods.Items[i].Name < pods.Items[j].Name })
		for _, pod := range pods.Items {
			podNode := g.addNodeWithDetails("Pod", pod.Namespace, pod.Name, buildPodDetails(&pod))
			g.addEdge(depNode, podNode)
		}
	}

	services, _ := servicesMatchingSelector(ctx, clientset, namespace, dep.Spec.Template.Labels)
	for _, svc := range services {
		svcNode := g.addNodeWithDetails("Service", svc.Namespace, svc.Name, buildServiceDetails(ctx, clientset, &svc))
		svcPortsLabel := buildServicePortsLabel(svc.Spec.Ports)
		for _, pod := range pods.Items {
			podNode := g.addNodeWithDetails("Pod", pod.Namespace, pod.Name, buildPodDetails(&pod))
			g.addEdgeWithLabel(svcNode, podNode, svcPortsLabel)
		}
		ingresses, _ := ingressesForService(ctx, clientset, svc.Namespace, svc.Name)
		for _, ingName := range ingresses {
			ingNode := g.addNode("Ingress", svc.Namespace, ingName)
			ingEdgeLabel := ""
			ingObj, err := clientset.NetworkingV1().Ingresses(svc.Namespace).Get(ctx, ingName, metav1.GetOptions{})
			if err == nil {
				ingNode = g.addNodeWithDetails("Ingress", ingObj.Namespace, ingObj.Name, buildIngressDetails(ingObj))
				ingEdgeLabel = ingressRoutesForServiceLabel(ingObj, svc.Name)
			}
			g.addEdgeWithLabel(ingNode, svcNode, ingEdgeLabel)
		}
	}

	return g, nil
}

func buildJobGraph(ctx context.Context, clientset *kubernetes.Clientset, namespace, jobName string) (*graph, error) {
	job, err := clientset.BatchV1().Jobs(namespace).Get(ctx, jobName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	g := newGraph()
	jobDetails := fmt.Sprintf("succeeded: %d | active: %d | failed: %d", job.Status.Succeeded, job.Status.Active, job.Status.Failed)
	jobNode := g.addNodeWithDetails("Job", job.Namespace, job.Name, jobDetails)

	pods, err := podsForJob(ctx, clientset, job)
	if err == nil {
		sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
		for _, pod := range pods {
			podNode := g.addNodeWithDetails("Pod", pod.Namespace, pod.Name, buildPodDetails(&pod))
			g.addEdge(jobNode, podNode)
		}
	}

	services, _ := servicesMatchingSelector(ctx, clientset, namespace, job.Spec.Template.Labels)
	for _, svc := range services {
		svcNode := g.addNodeWithDetails("Service", svc.Namespace, svc.Name, buildServiceDetails(ctx, clientset, &svc))
		svcPortsLabel := buildServicePortsLabel(svc.Spec.Ports)
		for _, pod := range pods {
			podNode := g.addNodeWithDetails("Pod", pod.Namespace, pod.Name, buildPodDetails(&pod))
			g.addEdgeWithLabel(svcNode, podNode, svcPortsLabel)
		}
		ingresses, _ := ingressesForService(ctx, clientset, svc.Namespace, svc.Name)
		for _, ingName := range ingresses {
			ingNode := g.addNode("Ingress", svc.Namespace, ingName)
			ingEdgeLabel := ""
			ingObj, err := clientset.NetworkingV1().Ingresses(svc.Namespace).Get(ctx, ingName, metav1.GetOptions{})
			if err == nil {
				ingNode = g.addNodeWithDetails("Ingress", ingObj.Namespace, ingObj.Name, buildIngressDetails(ingObj))
				ingEdgeLabel = ingressRoutesForServiceLabel(ingObj, svc.Name)
			}
			g.addEdgeWithLabel(ingNode, svcNode, ingEdgeLabel)
		}
	}

	return g, nil
}

func buildCronJobGraph(ctx context.Context, clientset *kubernetes.Clientset, namespace, cronJobName string) (*graph, error) {
	cj, err := clientset.BatchV1().CronJobs(namespace).Get(ctx, cronJobName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	g := newGraph()
	cjDetails := fmt.Sprintf("schedule: %s", cj.Spec.Schedule)
	cjNode := g.addNodeWithDetails("CronJob", cj.Namespace, cj.Name, cjDetails)

	jobs, err := jobsForCronJob(ctx, clientset, cj)
	if err == nil {
		sort.Slice(jobs, func(i, j int) bool { return jobs[i].Name < jobs[j].Name })
		for _, job := range jobs {
			jobDetails := fmt.Sprintf("succeeded: %d | active: %d | failed: %d", job.Status.Succeeded, job.Status.Active, job.Status.Failed)
			jobNode := g.addNodeWithDetails("Job", job.Namespace, job.Name, jobDetails)
			g.addEdge(cjNode, jobNode)

			pods, _ := podsForJob(ctx, clientset, &job)
			sort.Slice(pods, func(a, b int) bool { return pods[a].Name < pods[b].Name })
			for _, pod := range pods {
				podNode := g.addNodeWithDetails("Pod", pod.Namespace, pod.Name, buildPodDetails(&pod))
				g.addEdge(jobNode, podNode)
			}
		}
	}

	services, _ := servicesMatchingSelector(ctx, clientset, namespace, cj.Spec.JobTemplate.Spec.Template.Labels)
	for _, svc := range services {
		svcNode := g.addNodeWithDetails("Service", svc.Namespace, svc.Name, buildServiceDetails(ctx, clientset, &svc))
		ingresses, _ := ingressesForService(ctx, clientset, svc.Namespace, svc.Name)
		for _, ingName := range ingresses {
			ingNode := g.addNode("Ingress", svc.Namespace, ingName)
			ingEdgeLabel := ""
			ingObj, err := clientset.NetworkingV1().Ingresses(svc.Namespace).Get(ctx, ingName, metav1.GetOptions{})
			if err == nil {
				ingNode = g.addNodeWithDetails("Ingress", ingObj.Namespace, ingObj.Name, buildIngressDetails(ingObj))
				ingEdgeLabel = ingressRoutesForServiceLabel(ingObj, svc.Name)
			}
			g.addEdgeWithLabel(ingNode, svcNode, ingEdgeLabel)
		}
	}

	return g, nil
}

func selectorForJob(job *batchv1.Job) string {
	if job.Spec.Selector != nil {
		selector := metav1.FormatLabelSelector(job.Spec.Selector)
		if selector != "" {
			return selector
		}
	}
	return metav1.FormatLabelSelector(&metav1.LabelSelector{MatchLabels: job.Spec.Template.Labels})
}

func podsForJob(ctx context.Context, clientset *kubernetes.Clientset, job *batchv1.Job) ([]corev1.Pod, error) {
	selector := selectorForJob(job)
	if selector == "" {
		return nil, errors.New("job has no selector")
	}
	pods, err := clientset.CoreV1().Pods(job.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	return pods.Items, nil
}

func jobsForCronJob(ctx context.Context, clientset *kubernetes.Clientset, cronJob *batchv1.CronJob) ([]batchv1.Job, error) {
	jobs, err := clientset.BatchV1().Jobs(cronJob.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	out := make([]batchv1.Job, 0)
	for _, job := range jobs.Items {
		for _, owner := range job.OwnerReferences {
			if owner.Kind == "CronJob" && owner.Name == cronJob.Name {
				out = append(out, job)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func ingressesForService(ctx context.Context, clientset *kubernetes.Clientset, namespace, serviceName string) ([]string, error) {
	list, err := clientset.NetworkingV1().Ingresses(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	seen := map[string]struct{}{}
	for _, ing := range list.Items {
		for _, svcName := range referencedServices(&ing) {
			if svcName == serviceName {
				seen[ing.Name] = struct{}{}
			}
		}
	}

	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

func servicesMatchingSelector(ctx context.Context, clientset *kubernetes.Clientset, namespace string, labels map[string]string) ([]corev1.Service, error) {
	list, err := clientset.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	out := make([]corev1.Service, 0)
	for _, svc := range list.Items {
		if len(svc.Spec.Selector) == 0 {
			continue
		}
		if labelsMatch(svc.Spec.Selector, labels) {
			out = append(out, svc)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func labelsMatch(selector, labels map[string]string) bool {
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func valueOrZero(v *int32) int32 {
	if v == nil {
		return 0
	}
	return *v
}

func ingressBackends(ing *networkingv1.Ingress) []ingressBackend {
	out := make([]ingressBackend, 0)

	if ing.Spec.DefaultBackend != nil && ing.Spec.DefaultBackend.Service != nil {
		out = append(out, ingressBackend{
			Host:        "*",
			Path:        "/*",
			ServiceName: ing.Spec.DefaultBackend.Service.Name,
			ServicePort: serviceBackendPortToString(ing.Spec.DefaultBackend.Service.Port),
		})
	}

	for _, rule := range ing.Spec.Rules {
		host := rule.Host
		if host == "" {
			host = "*"
		}
		if rule.HTTP == nil {
			continue
		}

		for _, p := range rule.HTTP.Paths {
			if p.Backend.Service == nil {
				continue
			}
			path := p.Path
			if path == "" {
				path = "/"
			}
			out = append(out, ingressBackend{
				Host:        host,
				Path:        path,
				ServiceName: p.Backend.Service.Name,
				ServicePort: serviceBackendPortToString(p.Backend.Service.Port),
			})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].ServiceName == out[j].ServiceName {
			if out[i].Host == out[j].Host {
				return out[i].Path < out[j].Path
			}
			return out[i].Host < out[j].Host
		}
		return out[i].ServiceName < out[j].ServiceName
	})

	return out
}

func uniqueServiceNames(backends []ingressBackend) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for _, be := range backends {
		if _, ok := seen[be.ServiceName]; ok {
			continue
		}
		seen[be.ServiceName] = struct{}{}
		out = append(out, be.ServiceName)
	}
	sort.Strings(out)
	return out
}

func serviceBackendPortToString(p networkingv1.ServiceBackendPort) string {
	if p.Name != "" {
		return p.Name
	}
	if p.Number != 0 {
		return fmt.Sprintf("%d", p.Number)
	}
	return "<none>"
}

func ingressAddresses(ing *networkingv1.Ingress) []string {
	out := make([]string, 0)
	for _, lb := range ing.Status.LoadBalancer.Ingress {
		if lb.IP != "" {
			out = append(out, lb.IP)
			continue
		}
		if lb.Hostname != "" {
			out = append(out, lb.Hostname)
		}
	}
	sort.Strings(out)
	return out
}

func formatServicePorts(ports []corev1.ServicePort) []string {
	if len(ports) == 0 {
		return []string{"<none>"}
	}
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		name := p.Name
		if name == "" {
			name = "unnamed"
		}
		entry := fmt.Sprintf("%s %d/%s -> target:%s", name, p.Port, p.Protocol, p.TargetPort.String())
		if p.NodePort != 0 {
			entry += fmt.Sprintf(" node:%d", p.NodePort)
		}
		out = append(out, entry)
	}
	sort.Strings(out)
	return out
}

func formatPodPorts(pod *corev1.Pod) []string {
	out := make([]string, 0)
	for _, c := range pod.Spec.Containers {
		for _, p := range c.Ports {
			out = append(out, fmt.Sprintf("%s:%d/%s", c.Name, p.ContainerPort, p.Protocol))
		}
	}
	sort.Strings(out)
	return out
}

func formatEndpointPorts(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		parts := strings.Fields(v)
		if len(parts) < 2 {
			out = append(out, v)
			continue
		}
		out = append(out, parts[0]+" "+styleValue(parts[1]))
	}
	return out
}

func endpointsForService(ctx context.Context, clientset *kubernetes.Clientset, namespace, serviceName string) ([]string, []string, error) {
	labelSelector := fmt.Sprintf("kubernetes.io/service-name=%s", serviceName)
	esList, err := clientset.DiscoveryV1().EndpointSlices(namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return nil, nil, err
	}

	ipSet := map[string]struct{}{}
	portSet := map[string]struct{}{}

	for _, es := range esList.Items {
		for _, ep := range es.Endpoints {
			for _, addr := range ep.Addresses {
				ipSet[addr] = struct{}{}
			}
		}
		for _, p := range es.Ports {
			if p.Port == nil {
				continue
			}
			name := "unnamed"
			if p.Name != nil && *p.Name != "" {
				name = *p.Name
			}
			protocol := corev1.ProtocolTCP
			if p.Protocol != nil {
				protocol = *p.Protocol
			}
			portSet[fmt.Sprintf("%s %d/%s", name, *p.Port, protocol)] = struct{}{}
		}
	}

	ips := make([]string, 0, len(ipSet))
	for ip := range ipSet {
		ips = append(ips, ip)
	}
	sort.Strings(ips)

	ports := make([]string, 0, len(portSet))
	for p := range portSet {
		ports = append(ports, p)
	}
	sort.Strings(ports)

	return ips, ports, nil
}

func valueOrNone(v string) string {
	if v == "" {
		return "<none>"
	}
	return v
}

func branchMarkers(prefix string, isLast bool) (string, string) {
	if isLast {
		return prefix + "`-- ", prefix + "    "
	}
	return prefix + "|-- ", prefix + "|   "
}

func renderGatewayASCII(ctx context.Context, namespace, gatewayName string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Namespace: %s\n", styleValue(namespace))
	fmt.Fprintf(&b, "%s: %s\n", styleType("Gateway"), styleValue(gatewayName))
	fmt.Fprintf(&b, "`-- Note: Gateway API routes should be queried separately\n")
	fmt.Fprintf(&b, "    (e.g., kubectl graph httproute <name> -n %s)\n", styleValue(namespace))
	return b.String(), nil
}

func buildGatewayGraph(ctx context.Context, namespace, gatewayName string) (*graph, error) {
	g := newGraph()
	g.addNode("Gateway", namespace, gatewayName)
	return g, nil
}

func renderRouteASCII(ctx context.Context, namespace, routeName, routeType string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Namespace: %s\n", styleValue(namespace))
	fmt.Fprintf(&b, "%s: %s\n", styleType(routeType), styleValue(routeName))
	fmt.Fprintf(&b, "`-- Use 'kubectl describe %s %s -n %s' for detailed backend refs\n",
		strings.ToLower(routeType), styleValue(routeName), styleValue(namespace))
	return b.String(), nil
}

func buildRouteGraph(ctx context.Context, namespace, routeName, routeType string) (*graph, error) {
	g := newGraph()
	g.addNode(routeType, namespace, routeName)
	return g, nil
}

const zshCompletionScript = `#compdef kubectl-graph kubectl

_kubectl_graph_add_matches() {
	compadd -Q -S '' -- "$@"
}

_kubectl_graph() {
	local -a namespaces
	local namespace="$(kubectl config view --minify -o jsonpath='{..namespace}' 2>/dev/null)"
	local i
	[[ -z "${namespace}" ]] && namespace="default"

	for (( i = 1; i <= CURRENT; i++ )); do
		if [[ "${words[i]}" == "-n" || "${words[i]}" == "--namespace" ]]; then
			namespace="${words[i+1]}"
		fi
	done

	if [[ "${words[CURRENT-1]}" == "-n" || "${words[CURRENT-1]}" == "--namespace" ]]; then
		local -a namespaces
		namespaces=(${(f)"$(kubectl get ns -o custom-columns=':metadata.name' --no-headers 2>/dev/null)"})
		_kubectl_graph_add_matches "${namespaces[@]}"
		return
	fi

	if [[ "${words[CURRENT-1]}" == "--output" ]]; then
		_values 'format' 'ascii' 'mermaid' 'tree' 'json'
		return
	fi

	if [[ "${words[1]}" == "kubectl" ]]; then
		if (( CURRENT == 2 )); then
			_values 'kubectl plugin command' 'graph'
			return
		fi
		if [[ "${words[2]}" != "graph" ]]; then
			return
		fi
		shift words
		(( CURRENT-- ))
	fi

	# Find actual subcommand by skipping leading flags (-n namespace, --output value, etc.)
	local _subcmd="" _subcmd_pos=0 _f
	for (( _f = 2; _f <= ${#words[@]}; _f++ )); do
		case "${words[$_f]}" in
			-n|--namespace|--output) (( _f++ )) ;;
			-*) ;;
			'') ;;
			*) _subcmd="${words[$_f]}"; _subcmd_pos=$_f; break ;;
		esac
	done

	# No subcommand yet, or cursor is at the subcommand word → propose subcommands
	if [[ -z "${_subcmd}" ]] || (( CURRENT == _subcmd_pos )); then
		_values 'command' \
			'ingress[Graph dependencies from an ingress]' \
			'ing[Alias for ingress]' \
			'service[Graph dependencies from a service]' \
			'svc[Alias for service]' \
			'deploy[Alias for deployment]' \
			'deployment[Graph dependencies from a deployment]' \
			'sts[Alias for statefulset]' \
			'statefulset[Graph dependencies from a StatefulSet]' \
			'job[Graph dependencies from a job]' \
			'cron[Alias for cronjob]' \
			'cj[Alias for cronjob]' \
			'cronjob[Graph dependencies from a cronjob]' \
			'gateway[Graph dependencies from a gateway]' \
			'gtw[Alias for gateway]' \
			'httproute[Graph dependencies from an HTTPRoute]' \
			'tcproute[Graph dependencies from a TCPRoute]' \
			'udproute[Graph dependencies from a UDPRoute]' \
			'tlsroute[Graph dependencies from a TLSRoute]' \
			'grpcroute[Graph dependencies from a GRPCRoute]' \
			'pod[Graph dependencies from a pod]' \
			'pods[Alias for pod]' \
			'po[Alias for pod]' \
			'pvc[Graph storage dependencies from a PVC]' \
			'role[Graph RBAC dependencies from a Role]' \
			'rolebinding[Graph RBAC dependencies from a RoleBinding]' \
			'rb[Alias for rolebinding]' \
			'clusterrole[Graph RBAC dependencies from a ClusterRole]' \
			'cr[Alias for clusterrole]' \
			'clusterrolebinding[Graph RBAC dependencies from a ClusterRoleBinding]' \
			'crb[Alias for clusterrolebinding]' \
			'completion[Generate completion script]'
		return
	fi

	# -n / --namespace at the current token → complete namespace names
	if [[ "${words[CURRENT-1]}" == "-n" || "${words[CURRENT-1]}" == "--namespace" ]]; then
		namespaces=(${(f)"$(kubectl get ns -o custom-columns=':metadata.name' --no-headers 2>/dev/null)"})
		_kubectl_graph_add_matches "${namespaces[@]}"
		return
	fi

	# --output at current token → complete output formats
	if [[ "${words[CURRENT-1]}" == "--output" ]]; then
		_values 'format' 'ascii' 'mermaid' 'tree' 'json'
		return
	fi

	local _scan_start=$(( _subcmd_pos + 1 ))

	_kg_zsh_complete_namespaced() {
		local _res="$1" has_name=0 ci
		for (( ci = _scan_start; ci < CURRENT; ci++ )); do
			case "${words[$ci]}" in
				-n|--namespace|--output) (( ci++ )) ;;
				-*) ;;
				*) has_name=1 ;;
			esac
		done
		if (( has_name == 0 )); then
			local -a _res_list
			_res_list=(${(f)"$(kubectl get "${_res}" -n "${namespace}" -o custom-columns=':metadata.name' --no-headers 2>/dev/null)"})
			_kubectl_graph_add_matches "${_res_list[@]}"
			return
		fi
		namespaces=(${(f)"$(kubectl get ns -o custom-columns=':metadata.name' --no-headers 2>/dev/null)"})
		local state
		_arguments -s -C \
			'(-n --namespace)'{-n+,--namespace=}'[Namespace]:namespace:->ns' \
			'--output=[Output format]:format:(ascii mermaid tree json)' \
			':resource name:()'
		case $state in ns) _kubectl_graph_add_matches "${namespaces[@]}" ;; esac
	}

	case "${_subcmd}" in
		ingress|ing)                  _kg_zsh_complete_namespaced ingress ;;
		service|svc)                  _kg_zsh_complete_namespaced svc ;;
		deployment|deploy)            _kg_zsh_complete_namespaced deploy ;;
		statefulset|sts)              _kg_zsh_complete_namespaced statefulset ;;
		job)                          _kg_zsh_complete_namespaced job ;;
		cronjob|cron|cj)              _kg_zsh_complete_namespaced cronjob ;;
		gateway|gtw)                  _kg_zsh_complete_namespaced gateway ;;
		pod|pods|po)                  _kg_zsh_complete_namespaced pod ;;
		pvc)                          _kg_zsh_complete_namespaced pvc ;;
		role)                         _kg_zsh_complete_namespaced role ;;
		rolebinding|rb)               _kg_zsh_complete_namespaced rolebinding ;;
		clusterrole|cr)
			local has_name=0 ci
			for (( ci = _scan_start; ci < CURRENT; ci++ )); do
				case "${words[$ci]}" in --output) (( ci++ )) ;; -*) ;; *) has_name=1 ;; esac
			done
			if (( has_name == 0 )); then
				local -a _res_list
				_res_list=(${(f)"$(kubectl get clusterrole -o custom-columns=':metadata.name' --no-headers 2>/dev/null)"})
				_kubectl_graph_add_matches "${_res_list[@]}"
			else
				_arguments '--output=[Output format]:format:(ascii mermaid tree json)'
			fi
			;;
		clusterrolebinding|crb)
			local has_name=0 ci
			for (( ci = _scan_start; ci < CURRENT; ci++ )); do
				case "${words[$ci]}" in --output) (( ci++ )) ;; -*) ;; *) has_name=1 ;; esac
			done
			if (( has_name == 0 )); then
				local -a _res_list
				_res_list=(${(f)"$(kubectl get clusterrolebinding -o custom-columns=':metadata.name' --no-headers 2>/dev/null)"})
				_kubectl_graph_add_matches "${_res_list[@]}"
			else
				_arguments '--output=[Output format]:format:(ascii mermaid tree json)'
			fi
			;;
		httproute|tcproute|udproute|tlsroute|grpcroute)
			namespaces=(${(f)"$(kubectl get ns -o custom-columns=':metadata.name' --no-headers 2>/dev/null)"})
			local state
			_arguments -s -C \
				'(-n --namespace)'{-n+,--namespace=}'[Namespace]:namespace:->ns' \
				'--output=[Output format]:format:(ascii mermaid tree json)' \
				':resource name:()'
			case $state in ns) _kubectl_graph_add_matches "${namespaces[@]}" ;; esac
			;;
		completion)
			_values 'shell' 'zsh' 'bash'
			;;
	esac
}

_kubectl_graph_dispatch() {
	if [[ "${words[2]}" == "graph" ]]; then
		_kubectl_graph
		return
	fi

	if typeset -f _kubectl >/dev/null 2>&1; then
		_kubectl "$@"
		return
	fi
}

compdef _kubectl_graph kubectl-graph
compdef _kubectl_graph_dispatch kubectl
`

const bashCompletionScript = `#!/usr/bin/env bash

_kubectl_graph() {
	local cur prev
	local namespace
	COMPREPLY=()
	cur="${COMP_WORDS[$COMP_CWORD]}"
	prev="${COMP_WORDS[$((COMP_CWORD-1))]}"
	namespace="$(kubectl config view --minify -o jsonpath='{..namespace}' 2>/dev/null)"
	if [[ -z "${namespace}" ]]; then
		namespace="default"
	fi

	# Find the actual subcommand by skipping leading flags (-n namespace, --output value, etc.)
	local cmd="" cmd_idx _ki
	cmd_idx=${#COMP_WORDS[@]}
	for (( _ki=1; _ki < COMP_CWORD; _ki++ )); do
		case "${COMP_WORDS[$_ki]}" in
			-n|--namespace) namespace="${COMP_WORDS[$((_ki+1))]}"; (( _ki++ )) ;;
			--output) (( _ki++ )) ;;
			-* | '') ;;
			*) cmd="${COMP_WORDS[$_ki]}"; cmd_idx=$_ki; break ;;
		esac
	done

	if [[ "$prev" == "-n" || "$prev" == "--namespace" ]]; then
		COMPREPLY=( $(compgen -W "$(kubectl get ns -o custom-columns=':metadata.name' --no-headers 2>/dev/null)" -- "$cur") )
		return 0
	fi

	if [[ "$prev" == "--output" ]]; then
		COMPREPLY=( $(compgen -W "ascii mermaid tree json" -- "$cur") )
		return 0
	fi

	# No subcommand yet, or cursor is at the subcommand position → suggest subcommands
	if [[ -z "${cmd}" || ${COMP_CWORD} -eq ${cmd_idx} ]]; then
		COMPREPLY=( $(compgen -W "ingress ing service svc deployment deploy statefulset sts job cronjob cron cj pod pods po pvc role rolebinding rb clusterrole cr clusterrolebinding crb gateway gtw httproute tcproute udproute tlsroute grpcroute completion" -- "${cur}") )
		return 0
	fi

	if [[ "${prev}" == "-n" || "${prev}" == "--namespace" ]]; then
		COMPREPLY=( $(compgen -W "$(kubectl get ns -o custom-columns=':metadata.name' --no-headers 2>/dev/null)" -- "${cur}") )
		return 0
	fi

	if [[ "${prev}" == "--output" ]]; then
		COMPREPLY=( $(compgen -W "ascii mermaid tree json" -- "${cur}") )
		return 0
	fi

	if [[ "${cmd}" == "completion" ]]; then
		COMPREPLY=( $(compgen -W "zsh bash" -- "${cur}") )
		return 0
	fi

	local _scan_start=$(( cmd_idx + 1 ))

	# Helper: complete a namespaced resource (accesses namespace, _scan_start, cur via dynamic scoping)
	_kg_complete_namespaced() {
		local _resource="$1" has_name=0 bns="${namespace}" bi
		for (( bi = _scan_start; bi < COMP_CWORD; bi++ )); do
			case "${COMP_WORDS[$bi]}" in
				-n|--namespace) bns="${COMP_WORDS[$((bi+1))]}"; (( bi++ )) ;;
				--output) (( bi++ )) ;;
				-*) ;;
				*) has_name=1 ;;
			esac
		done
		if [[ "${has_name}" == "0" && "${cur}" != -* ]]; then
			COMPREPLY=( $(compgen -W "$(kubectl get "${_resource}" -n "${bns}" -o custom-columns=':metadata.name' --no-headers 2>/dev/null)" -- "${cur}") )
			return 0
		fi
		COMPREPLY=( $(compgen -W "-n --namespace --output" -- "${cur}") )
		return 0
	}

	case "${cmd}" in
		ingress|ing)                                          _kg_complete_namespaced ingress; return $? ;;
		service|svc)                                          _kg_complete_namespaced svc; return $? ;;
		deployment|deploy)                                    _kg_complete_namespaced deploy; return $? ;;
		statefulset|sts)                                      _kg_complete_namespaced statefulset; return $? ;;
		job)                                                  _kg_complete_namespaced job; return $? ;;
		cronjob|cron|cj)                                      _kg_complete_namespaced cronjob; return $? ;;
		gateway|gtw)                                          COMPREPLY=( $(compgen -W "-n --namespace --output" -- "${cur}") ); return 0 ;;
		pod|pods|po)                                          _kg_complete_namespaced pod; return $? ;;
		pvc)                                                  _kg_complete_namespaced pvc; return $? ;;
		role)                                                 _kg_complete_namespaced role; return $? ;;
		rolebinding|rb)                                       _kg_complete_namespaced rolebinding; return $? ;;
		httproute|tcproute|udproute|tlsroute|grpcroute)
			COMPREPLY=( $(compgen -W "-n --namespace --output" -- "${cur}") )
			return 0 ;;
		clusterrole|cr)
			local has_name=0 bi
			for (( bi = _scan_start; bi < COMP_CWORD; bi++ )); do
				case "${COMP_WORDS[$bi]}" in --output) (( bi++ )) ;; -*) ;; *) has_name=1 ;; esac
			done
			if [[ "${has_name}" == "0" && "${cur}" != -* ]]; then
				COMPREPLY=( $(compgen -W "$(kubectl get clusterrole -o custom-columns=':metadata.name' --no-headers 2>/dev/null)" -- "${cur}") )
				return 0
			fi
			COMPREPLY=( $(compgen -W "--output" -- "${cur}") ); return 0 ;;
		clusterrolebinding|crb)
			local has_name=0 bi
			for (( bi = _scan_start; bi < COMP_CWORD; bi++ )); do
				case "${COMP_WORDS[$bi]}" in --output) (( bi++ )) ;; -*) ;; *) has_name=1 ;; esac
			done
			if [[ "${has_name}" == "0" && "${cur}" != -* ]]; then
				COMPREPLY=( $(compgen -W "$(kubectl get clusterrolebinding -o custom-columns=':metadata.name' --no-headers 2>/dev/null)" -- "${cur}") )
				return 0
			fi
			COMPREPLY=( $(compgen -W "--output" -- "${cur}") ); return 0 ;;
	esac
}

_kubectl_graph_dispatch() {
	if [[ ${COMP_CWORD} -eq 1 ]]; then
		local current="${COMP_WORDS[$COMP_CWORD]}" graph_match
		if command -v __start_kubectl >/dev/null 2>&1; then
			__start_kubectl
		fi
		if [[ "graph" == "$current"* ]]; then
			graph_match=1
			local reply
			for reply in "${COMPREPLY[@]}"; do
				if [[ "$reply" == "graph" ]]; then
					graph_match=0
					break
				fi
			done
			if [[ "$graph_match" -eq 1 ]]; then
				COMPREPLY+=(graph)
			fi
		fi
		return 0
	fi

	if [[ "${COMP_WORDS[1]}" == "graph" ]]; then
		local saved_cword saved_line saved_point
		local -a saved_words
		saved_words=("${COMP_WORDS[@]}")
		saved_cword=${COMP_CWORD}
		saved_line=${COMP_LINE}
		saved_point=${COMP_POINT}

		COMP_WORDS=("kubectl-graph" "${saved_words[@]:2}")
		COMP_CWORD=$(( saved_cword - 1 ))
		COMP_LINE="kubectl-graph ${saved_line#kubectl graph }"
		COMP_POINT=${#COMP_LINE}

		_kubectl_graph

		COMP_WORDS=("${saved_words[@]}")
		COMP_CWORD=${saved_cword}
		COMP_LINE=${saved_line}
		COMP_POINT=${saved_point}
		return 0
	fi

	if command -v __start_kubectl >/dev/null 2>&1; then
		__start_kubectl
		return $?
	fi

	return 0
}

complete -F _kubectl_graph kubectl-graph
complete -o default -F _kubectl_graph_dispatch kubectl
`

func buildClientset() (*kubernetes.Clientset, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		kubeCfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{})
		cfg, err = kubeCfg.ClientConfig()
		if err != nil {
			return nil, err
		}
	}
	return kubernetes.NewForConfig(cfg)
}

func buildIngressGraph(ctx context.Context, clientset *kubernetes.Clientset, namespace, ingressName string) (*graph, error) {
	ing, err := clientset.NetworkingV1().Ingresses(namespace).Get(ctx, ingressName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	g := newGraph()
	ingNode := g.addNodeWithDetails("Ingress", ing.Namespace, ing.Name, buildIngressDetails(ing))
	backendLabelsByService := map[string][]string{}
	for _, be := range ingressBackends(ing) {
		backendLabelsByService[be.ServiceName] = append(backendLabelsByService[be.ServiceName], fmt.Sprintf("%s %s -> %s", be.Host, be.Path, be.ServicePort))
	}

	serviceNames := referencedServices(ing)
	if len(serviceNames) == 0 {
		return nil, errors.New("ingress does not reference any service backend")
	}

	sort.Strings(serviceNames)
	for _, svcName := range serviceNames {
		svc, err := clientset.CoreV1().Services(namespace).Get(ctx, svcName, metav1.GetOptions{})
		if err != nil {
			continue
		}
		svcDetails := buildServiceDetails(ctx, clientset, svc)
		svcNode := g.addNodeWithDetails("Service", svc.Namespace, svc.Name, svcDetails)
		routeLabel := ""
		if labels, ok := backendLabelsByService[svcName]; ok && len(labels) > 0 {
			sort.Strings(labels)
			routeLabel = strings.Join(labels, ", ")
		}
		g.addEdgeWithLabel(ingNode, svcNode, routeLabel)
		svcPortsLabel := buildServicePortsLabel(svc.Spec.Ports)

		pods, err := podsForService(ctx, clientset, svc)
		if err != nil {
			continue
		}

		sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
		for _, pod := range pods {
			podNode := g.addNodeWithDetails("Pod", pod.Namespace, pod.Name, buildPodDetails(&pod))
			g.addEdgeWithLabel(svcNode, podNode, svcPortsLabel)

			controllerKind, controllerName := resolveWorkloadOwner(ctx, clientset, &pod)
			if controllerName == "" {
				continue
			}
			ctrlNode := g.addNode(controllerKind, pod.Namespace, controllerName)
			g.addEdge(ctrlNode, podNode)
		}
	}

	return g, nil
}

func referencedServices(ing *networkingv1.Ingress) []string {
	seen := map[string]struct{}{}
	add := func(name string) {
		if name == "" {
			return
		}
		seen[name] = struct{}{}
	}

	if ing.Spec.DefaultBackend != nil && ing.Spec.DefaultBackend.Service != nil {
		add(ing.Spec.DefaultBackend.Service.Name)
	}

	for _, rule := range ing.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		for _, p := range rule.HTTP.Paths {
			if p.Backend.Service != nil {
				add(p.Backend.Service.Name)
			}
		}
	}

	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	return out
}

func podsForService(ctx context.Context, clientset *kubernetes.Clientset, svc *corev1.Service) ([]corev1.Pod, error) {
	selector := metav1.FormatLabelSelector(&metav1.LabelSelector{MatchLabels: svc.Spec.Selector})
	if selector == "" {
		return nil, errors.New("service has no selector")
	}

	pods, err := clientset.CoreV1().Pods(svc.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	return pods.Items, nil
}

func renderPVCASCII(ctx context.Context, clientset *kubernetes.Clientset, namespace, pvcName string) (string, error) {
	pvc, err := clientset.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Namespace: %s\n", styleValue(pvc.Namespace))
	fmt.Fprintf(&b, "%s: %s\n", styleType("PVC"), styleValue(pvc.Name))

	accessModes := make([]string, len(pvc.Spec.AccessModes))
	for i, am := range pvc.Spec.AccessModes {
		accessModes[i] = string(am)
	}
	scName := "<none>"
	if pvc.Spec.StorageClassName != nil {
		scName = *pvc.Spec.StorageClassName
	}
	fmt.Fprintf(&b, "|-- StorageClass: %s\n", styleValue(scName))
	fmt.Fprintf(&b, "|-- AccessModes: %s\n", styleValue(strings.Join(accessModes, ", ")))

	if pvc.Spec.VolumeName != "" {
		pv, err := clientset.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
		if err == nil && pv != nil {
			fmt.Fprintf(&b, "|-- %s: %s\n", styleType("PV"), styleValue(pv.Name))
			if pv.Spec.StorageClassName != "" {
				fmt.Fprintf(&b, "|   |-- StorageClass: %s\n", styleValue(pv.Spec.StorageClassName))
			}
			if quantity, ok := pv.Spec.Capacity[corev1.ResourceStorage]; ok {
				fmt.Fprintf(&b, "|   |-- Capacity: %s\n", styleValue(quantity.String()))
			}
			fmt.Fprintf(&b, "|   `-- Source: %s\n", styleValue(getPVSourceType(pv)))
		}
	}

	pods, _ := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	usingPods := make([]corev1.Pod, 0)
	for _, pod := range pods.Items {
		for _, vol := range pod.Spec.Volumes {
			if vol.PersistentVolumeClaim != nil && vol.PersistentVolumeClaim.ClaimName == pvcName {
				usingPods = append(usingPods, pod)
				break
			}
		}
	}

	fmt.Fprintf(&b, "`-- Pods using this PVC:\n")
	if len(usingPods) == 0 {
		fmt.Fprintf(&b, "    `-- none\n")
	} else {
		for i, pod := range usingPods {
			line, child := branchMarkers("    ", i == len(usingPods)-1)
			fmt.Fprintf(&b, "%s%s: %s\n", line, styleType("Pod"), styleValue(pod.Name))
			fmt.Fprintf(&b, "%s|-- IP: %s\n", child, styleValue(valueOrNone(pod.Status.PodIP)))
			fmt.Fprintf(&b, "%s`-- Phase: %s\n", child, stylePhase(pod.Status.Phase))
		}
	}

	return b.String(), nil
}

func buildPVCGraph(ctx context.Context, clientset *kubernetes.Clientset, namespace, pvcName string) (*graph, error) {
	pvc, err := clientset.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	g := newGraph()
	pvcNode := g.addNodeWithDetails("PVC", pvc.Namespace, pvc.Name, buildPVCDetails(pvc))

	if pvc.Spec.VolumeName != "" {
		pv, err := clientset.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
		if err == nil && pv != nil {
			pvNode := g.addNodeWithDetails("PV", "", pv.Name, buildPVDetails(pv))
			g.addEdgeWithLabel(pvcNode, pvNode, "")
		}
	}

	pods, _ := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	for _, pod := range pods.Items {
		for _, vol := range pod.Spec.Volumes {
			if vol.PersistentVolumeClaim != nil && vol.PersistentVolumeClaim.ClaimName == pvcName {
				podNode := g.addNodeWithDetails("Pod", pod.Namespace, pod.Name, buildPodDetails(&pod))
				g.addEdgeWithLabel(podNode, pvcNode, "")
				break
			}
		}
	}

	return g, nil
}

func buildPVCDetails(pvc *corev1.PersistentVolumeClaim) string {
	accessModes := make([]string, len(pvc.Spec.AccessModes))
	for i, am := range pvc.Spec.AccessModes {
		accessModes[i] = string(am)
	}

	scName := "<none>"
	if pvc.Spec.StorageClassName != nil {
		scName = *pvc.Spec.StorageClassName
	}

	parts := []string{
		"StorageClass: " + scName,
		"AccessModes: " + strings.Join(accessModes, ", "),
	}
	return strings.Join(parts, "<br/>")
}

func buildPVDetails(pv *corev1.PersistentVolume) string {
	parts := []string{
		"StorageClass: " + pv.Spec.StorageClassName,
		"Source: " + getPVSourceType(pv),
	}
	if quantity, ok := pv.Spec.Capacity[corev1.ResourceStorage]; ok {
		parts = append(parts, "Capacity: "+quantity.String())
	}
	return strings.Join(parts, "<br/>")
}

func getPVSourceType(pv *corev1.PersistentVolume) string {
	spec := pv.Spec
	if spec.HostPath != nil {
		return "hostPath: " + spec.HostPath.Path
	}
	if spec.NFS != nil {
		return "nfs: " + spec.NFS.Server + ":" + spec.NFS.Path
	}
	if spec.CSI != nil {
		return "csi: " + spec.CSI.Driver
	}
	if spec.Cinder != nil {
		return "cinder: " + spec.Cinder.VolumeID
	}
	return "<unknown>"
}

func renderStatefulSetASCII(ctx context.Context, clientset *kubernetes.Clientset, namespace, stsName string) (string, error) {
	sts, err := clientset.AppsV1().StatefulSets(namespace).Get(ctx, stsName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	pods, err := podsForStatefulSet(ctx, clientset, sts)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Namespace: %s\n", styleValue(sts.Namespace))
	fmt.Fprintf(&b, "%s: %s\n", styleType("StatefulSet"), styleValue(sts.Name))
	fmt.Fprintf(&b, "|-- Replicas: %s\n", styleValue(fmt.Sprintf("%d", valueOrZero(sts.Spec.Replicas))))
	fmt.Fprintf(&b, "|-- Ready: %s\n", styleValue(fmt.Sprintf("%d", sts.Status.ReadyReplicas)))
	fmt.Fprintf(&b, "`-- Pods:\n")
	if len(pods) == 0 {
		fmt.Fprintf(&b, "    `-- none\n")
		return b.String(), nil
	}

	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	for i, pod := range pods {
		line, child := branchMarkers("    ", i == len(pods)-1)
		fmt.Fprintf(&b, "%s%s: %s\n", line, styleType("Pod"), styleValue(pod.Name))
		fmt.Fprintf(&b, "%s|-- IP: %s\n", child, styleValue(valueOrNone(pod.Status.PodIP)))
		fmt.Fprintf(&b, "%s`-- Phase: %s\n", child, stylePhase(pod.Status.Phase))
	}

	return b.String(), nil
}

func buildStatefulSetGraph(ctx context.Context, clientset *kubernetes.Clientset, namespace, stsName string) (*graph, error) {
	sts, err := clientset.AppsV1().StatefulSets(namespace).Get(ctx, stsName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	g := newGraph()
	stsDetails := fmt.Sprintf("replicas: %d | ready: %d", valueOrZero(sts.Spec.Replicas), sts.Status.ReadyReplicas)
	stsNode := g.addNodeWithDetails("StatefulSet", sts.Namespace, sts.Name, stsDetails)

	pods, err := podsForStatefulSet(ctx, clientset, sts)
	if err == nil {
		sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
		for _, pod := range pods {
			podNode := g.addNodeWithDetails("Pod", pod.Namespace, pod.Name, buildPodDetails(&pod))
			g.addEdge(stsNode, podNode)
		}
	}

	services, _ := servicesMatchingSelector(ctx, clientset, namespace, sts.Spec.Template.Labels)
	for _, svc := range services {
		svcNode := g.addNodeWithDetails("Service", svc.Namespace, svc.Name, buildServiceDetails(ctx, clientset, &svc))
		svcPortsLabel := buildServicePortsLabel(svc.Spec.Ports)
		for _, pod := range pods {
			podNode := g.addNodeWithDetails("Pod", pod.Namespace, pod.Name, buildPodDetails(&pod))
			g.addEdgeWithLabel(svcNode, podNode, svcPortsLabel)
		}
	}

	return g, nil
}

func podsForStatefulSet(ctx context.Context, clientset *kubernetes.Clientset, sts *appsv1.StatefulSet) ([]corev1.Pod, error) {
	selector, err := metav1.LabelSelectorAsSelector(sts.Spec.Selector)
	if err != nil {
		return nil, err
	}
	podList, err := clientset.CoreV1().Pods(sts.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return nil, err
	}
	return podList.Items, nil
}

func renderRoleASCII(ctx context.Context, clientset *kubernetes.Clientset, namespace, roleName string) (string, error) {
	role, err := clientset.RbacV1().Roles(namespace).Get(ctx, roleName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	roleBindings, _ := clientset.RbacV1().RoleBindings(namespace).List(ctx, metav1.ListOptions{})
	linkedRoleBindings := make([]string, 0)
	for _, rb := range roleBindings.Items {
		if rb.RoleRef.Kind == "Role" && rb.RoleRef.Name == roleName {
			linkedRoleBindings = append(linkedRoleBindings, rb.Name)
		}
	}
	sort.Strings(linkedRoleBindings)

	var b strings.Builder
	fmt.Fprintf(&b, "Namespace: %s\n", styleValue(role.Namespace))
	fmt.Fprintf(&b, "%s: %s\n", styleType("Role"), styleValue(role.Name))
	fmt.Fprintf(&b, "|-- RoleBindings:\n")
	if len(linkedRoleBindings) == 0 {
		fmt.Fprintf(&b, "|   `-- none\n")
	} else {
		for i, rbName := range linkedRoleBindings {
			line, _ := branchMarkers("|   ", i == len(linkedRoleBindings)-1)
			fmt.Fprintf(&b, "%s%s\n", line, styleValue(rbName))
		}
	}
	fmt.Fprintf(&b, "`-- Rules: %d\n", len(role.Rules))
	for i, rule := range role.Rules {
		line, child := branchMarkers("    ", i == len(role.Rules)-1)
		fmt.Fprintf(&b, "%sRule %d:\n", line, i+1)
		fmt.Fprintf(&b, "%s|-- Verbs: %s\n", child, strings.Join(rule.Verbs, ", "))
		fmt.Fprintf(&b, "%s|-- Resources: %s\n", child, strings.Join(rule.Resources, ", "))
		fmt.Fprintf(&b, "%s`-- APIGroups: %s\n", child, strings.Join(rule.APIGroups, ", "))
	}

	return b.String(), nil
}

func buildRoleGraph(ctx context.Context, clientset *kubernetes.Clientset, namespace, roleName string) (*graph, error) {
	role, err := clientset.RbacV1().Roles(namespace).Get(ctx, roleName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	g := newGraph()
	roleNode := g.addNodeWithDetails("Role", role.Namespace, role.Name, fmt.Sprintf("rules: %d", len(role.Rules)))

	roleBindings, err := clientset.RbacV1().RoleBindings(namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		for _, rb := range roleBindings.Items {
			if rb.RoleRef.Name == roleName && rb.RoleRef.Kind == "Role" {
				rbNode := g.addNode("RoleBinding", rb.Namespace, rb.Name)
				g.addEdge(rbNode, roleNode)
			}
		}
	}

	return g, nil
}

func renderRoleBindingASCII(ctx context.Context, clientset *kubernetes.Clientset, namespace, rbName string) (string, error) {
	rb, err := clientset.RbacV1().RoleBindings(namespace).Get(ctx, rbName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Namespace: %s\n", styleValue(rb.Namespace))
	fmt.Fprintf(&b, "%s: %s\n", styleType("RoleBinding"), styleValue(rb.Name))
	fmt.Fprintf(&b, "|-- Role: %s\n", styleValue(rb.RoleRef.Name))
	fmt.Fprintf(&b, "`-- Subjects: %d\n", len(rb.Subjects))
	if len(rb.Subjects) == 0 {
		fmt.Fprintf(&b, "    `-- none\n")
		return b.String(), nil
	}
	for i, subject := range rb.Subjects {
		line, _ := branchMarkers("    ", i == len(rb.Subjects)-1)
		fmt.Fprintf(&b, "%s%s: %s (name: %s)\n", line, styleType(subject.Kind), styleValue(subject.Name), styleValue(subject.Namespace))
	}

	return b.String(), nil
}

func buildRoleBindingGraph(ctx context.Context, clientset *kubernetes.Clientset, namespace, rbName string) (*graph, error) {
	rb, err := clientset.RbacV1().RoleBindings(namespace).Get(ctx, rbName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	g := newGraph()
	rbNode := g.addNode("RoleBinding", rb.Namespace, rb.Name)

	roleNode := g.addNode("Role", rb.Namespace, rb.RoleRef.Name)
	g.addEdge(rbNode, roleNode)

	for _, subject := range rb.Subjects {
		subjNode := g.addNode(subject.Kind, subject.Namespace, subject.Name)
		g.addEdge(subjNode, rbNode)
	}

	return g, nil
}

func renderClusterRoleASCII(ctx context.Context, clientset *kubernetes.Clientset, roleName string) (string, error) {
	role, err := clientset.RbacV1().ClusterRoles().Get(ctx, roleName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	crbs, _ := clientset.RbacV1().ClusterRoleBindings().List(ctx, metav1.ListOptions{})
	linkedBindings := make([]string, 0)
	for _, crb := range crbs.Items {
		if crb.RoleRef.Kind == "ClusterRole" && crb.RoleRef.Name == roleName {
			linkedBindings = append(linkedBindings, crb.Name)
		}
	}
	sort.Strings(linkedBindings)

	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n", styleType("ClusterRole"), styleValue(role.Name))
	fmt.Fprintf(&b, "|-- ClusterRoleBindings:\n")
	if len(linkedBindings) == 0 {
		fmt.Fprintf(&b, "|   `-- none\n")
	} else {
		for i, name := range linkedBindings {
			line, _ := branchMarkers("|   ", i == len(linkedBindings)-1)
			fmt.Fprintf(&b, "%s%s\n", line, styleValue(name))
		}
	}
	fmt.Fprintf(&b, "`-- Rules: %d\n", len(role.Rules))
	for i, rule := range role.Rules {
		line, child := branchMarkers("    ", i == len(role.Rules)-1)
		fmt.Fprintf(&b, "%sRule %d:\n", line, i+1)
		fmt.Fprintf(&b, "%s|-- Verbs: %s\n", child, strings.Join(rule.Verbs, ", "))
		fmt.Fprintf(&b, "%s|-- Resources: %s\n", child, strings.Join(rule.Resources, ", "))
		fmt.Fprintf(&b, "%s`-- APIGroups: %s\n", child, strings.Join(rule.APIGroups, ", "))
	}

	return b.String(), nil
}

func buildClusterRoleGraph(ctx context.Context, clientset *kubernetes.Clientset, roleName string) (*graph, error) {
	role, err := clientset.RbacV1().ClusterRoles().Get(ctx, roleName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	g := newGraph()
	roleNode := g.addNodeWithDetails("ClusterRole", "cluster", role.Name, fmt.Sprintf("rules: %d", len(role.Rules)))

	crbs, err := clientset.RbacV1().ClusterRoleBindings().List(ctx, metav1.ListOptions{})
	if err == nil {
		for _, crb := range crbs.Items {
			if crb.RoleRef.Name == roleName && crb.RoleRef.Kind == "ClusterRole" {
				crbNode := g.addNode("ClusterRoleBinding", "cluster", crb.Name)
				g.addEdge(crbNode, roleNode)
			}
		}
	}

	return g, nil
}

func renderClusterRoleBindingASCII(ctx context.Context, clientset *kubernetes.Clientset, crbName string) (string, error) {
	crb, err := clientset.RbacV1().ClusterRoleBindings().Get(ctx, crbName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n", styleType("ClusterRoleBinding"), styleValue(crb.Name))
	fmt.Fprintf(&b, "|-- ClusterRole: %s\n", styleValue(crb.RoleRef.Name))
	fmt.Fprintf(&b, "`-- Subjects: %d\n", len(crb.Subjects))
	if len(crb.Subjects) == 0 {
		fmt.Fprintf(&b, "    `-- none\n")
		return b.String(), nil
	}
	for i, subject := range crb.Subjects {
		line, _ := branchMarkers("    ", i == len(crb.Subjects)-1)
		fmt.Fprintf(&b, "%s%s: %s (namespace: %s)\n", line, styleType(subject.Kind), styleValue(subject.Name), styleValue(subject.Namespace))
	}

	return b.String(), nil
}

func buildClusterRoleBindingGraph(ctx context.Context, clientset *kubernetes.Clientset, crbName string) (*graph, error) {
	crb, err := clientset.RbacV1().ClusterRoleBindings().Get(ctx, crbName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	g := newGraph()
	crbNode := g.addNode("ClusterRoleBinding", "cluster", crb.Name)

	roleNode := g.addNode("ClusterRole", "cluster", crb.RoleRef.Name)
	g.addEdge(crbNode, roleNode)

	for _, subject := range crb.Subjects {
		subjNode := g.addNode(subject.Kind, subject.Namespace, subject.Name)
		g.addEdge(subjNode, crbNode)
	}

	return g, nil
}

func resolveWorkloadOwner(ctx context.Context, clientset *kubernetes.Clientset, pod *corev1.Pod) (string, string) {
	for _, owner := range pod.OwnerReferences {
		switch owner.Kind {
		case "ReplicaSet":
			rs, err := clientset.AppsV1().ReplicaSets(pod.Namespace).Get(ctx, owner.Name, metav1.GetOptions{})
			if err != nil {
				return "ReplicaSet", owner.Name
			}
			for _, rsOwner := range rs.OwnerReferences {
				if rsOwner.Kind == "Deployment" {
					return "Deployment", rsOwner.Name
				}
			}
			return "ReplicaSet", rs.Name
		case "StatefulSet", "DaemonSet", "Job", "CronJob":
			return owner.Kind, owner.Name
		}
	}
	return "", ""
}

func renderMermaid(g *graph) string {
	nodes := make([]node, 0, len(g.nodes))
	for _, n := range g.nodes {
		nodes = append(nodes, n)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })

	edges := make([]edge, 0, len(g.edges))
	for _, e := range g.edges {
		edges = append(edges, e)
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From == edges[j].From {
			if edges[i].To == edges[j].To {
				return edges[i].Label < edges[j].Label
			}
			return edges[i].To < edges[j].To
		}
		return edges[i].From < edges[j].From
	})

	var b strings.Builder
	b.WriteString("graph LR\n")

	nonPods := make([]node, 0, len(nodes))
	pods := make([]node, 0)
	for _, n := range nodes {
		if n.Kind == "Pod" {
			pods = append(pods, n)
			continue
		}
		nonPods = append(nonPods, n)
	}

	for _, n := range nonPods {
		b.WriteString(renderMermaidNodeLine(n))
		b.WriteString("\n")
	}

	for _, n := range pods {
		b.WriteString(renderMermaidNodeLine(n))
		b.WriteString("\n")
	}

	kindOf := func(nodeID string) string {
		if n, ok := g.nodes[nodeID]; ok {
			return n.Kind
		}
		return ""
	}

	for _, e := range edges {
		b.WriteString(renderMermaidEdgeLine(e.From, e.To, e.Label, mermaidConnector(kindOf(e.From), kindOf(e.To))))
		b.WriteString("\n")
	}
	return b.String()
}

func renderMermaidEdgeLine(from, to, label, connector string) string {
	if connector == "" {
		connector = "-->"
	}
	if label == "" {
		return fmt.Sprintf("  %s %s %s", from, connector, to)
	}
	edgeLabel := strings.ReplaceAll(label, "\"", "#quot;")
	edgeLabel = strings.ReplaceAll(edgeLabel, "|", "/")
	return fmt.Sprintf("  %s %s|%s| %s", from, connector, edgeLabel, to)
}

func mermaidConnector(fromKind, toKind string) string {
	if toKind == "Pod" {
		switch fromKind {
		case "Service":
			return "-.->"
		case "Deployment":
			return "==>"
		}
	}
	if fromKind == "Ingress" && toKind == "Service" {
		return "-->"
	}
	return "-->"
}

// buildServiceDetails returns a single-line summary of service fields joined with " | ".
func buildServiceDetails(ctx context.Context, clientset *kubernetes.Clientset, svc *corev1.Service) string {
	parts := []string{
		"type: " + string(svc.Spec.Type),
		"clusterIP: " + valueOrNone(svc.Spec.ClusterIP),
	}
	return strings.Join(parts, "<br/>")
}

// buildPodDetails returns a single-line summary of pod fields.
func buildPodDetails(pod *corev1.Pod) string {
	parts := []string{
		"IP: " + valueOrNone(pod.Status.PodIP),
		"phase: " + string(pod.Status.Phase),
	}
	return strings.Join(parts, "<br/>")
}

func buildServicePortsLabel(ports []corev1.ServicePort) string {
	values := formatServicePorts(ports)
	if len(values) == 0 {
		return ""
	}
	return "ports: " + strings.Join(values, ", ")
}

func ingressRoutesForServiceLabel(ing *networkingv1.Ingress, serviceName string) string {
	labels := make([]string, 0)
	for _, be := range ingressBackends(ing) {
		if be.ServiceName != serviceName {
			continue
		}
		labels = append(labels, fmt.Sprintf("%s %s -> %s", be.Host, be.Path, be.ServicePort))
	}
	if len(labels) == 0 {
		return ""
	}
	sort.Strings(labels)
	return strings.Join(labels, ", ")
}

// buildIngressDetails returns a summary of ingress fields for mermaid labels.
func buildIngressDetails(ing *networkingv1.Ingress) string {
	className := "<none>"
	if ing.Spec.IngressClassName != nil && *ing.Spec.IngressClassName != "" {
		className = *ing.Spec.IngressClassName
	}

	parts := []string{"class: " + className}
	addresses := ingressAddresses(ing)
	if len(addresses) > 0 {
		parts = append(parts, "addresses: "+strings.Join(addresses, ", "))
	}

	backends := ingressBackends(ing)
	if len(backends) > 0 {
		routes := make([]string, 0, len(backends))
		for _, be := range backends {
			routes = append(routes, fmt.Sprintf("%s %s -> %s:%s", be.Host, be.Path, be.ServiceName, be.ServicePort))
		}
		parts = append(parts, "routes: "+strings.Join(routes, " | "))
	}

	return strings.Join(parts, "<br/>")
}

func renderTree(g *graph) string {
	type childrenMap map[string][]string
	children := childrenMap{}
	parents := map[string]int{}

	for _, e := range g.edges {
		children[e.From] = append(children[e.From], e.To)
		parents[e.To]++
	}

	for from := range children {
		sort.Strings(children[from])
	}

	roots := make([]string, 0)
	for id := range g.nodes {
		if parents[id] == 0 {
			roots = append(roots, id)
		}
	}
	sort.Strings(roots)

	var b strings.Builder
	seen := map[string]bool{}
	for _, root := range roots {
		renderNode(&b, g, children, seen, root, 0)
	}
	return b.String()
}

func renderNode(b *strings.Builder, g *graph, children map[string][]string, seen map[string]bool, id string, depth int) {
	n, ok := g.nodes[id]
	if !ok {
		return
	}
	fmt.Fprintf(b, "%s- %s\n", strings.Repeat("  ", depth), strings.ReplaceAll(n.Label, "\\n", " "))
	if seen[id] {
		return
	}
	seen[id] = true

	for _, child := range children[id] {
		renderNode(b, g, children, seen, child, depth+1)
	}
}

type GraphJSON struct {
	Nodes []NodeJSON `json:"nodes"`
	Edges []EdgeJSON `json:"edges"`
}

type NodeJSON struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Details string `json:"details,omitempty"`
}

type EdgeJSON struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label,omitempty"`
}

func renderGraphJSON(g *graph) GraphJSON {
	nodes := make([]NodeJSON, 0, len(g.nodes))
	for _, n := range g.nodes {
		nodes = append(nodes, NodeJSON{
			ID:      n.ID,
			Kind:    n.Kind,
			Label:   strings.ReplaceAll(n.Label, "\\n", " / "),
			Details: n.Details,
		})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })

	edges := make([]EdgeJSON, 0, len(g.edges))
	for _, e := range g.edges {
		edges = append(edges, EdgeJSON{
			From:  e.From,
			To:    e.To,
			Label: e.Label,
		})
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From == edges[j].From {
			if edges[i].To == edges[j].To {
				return edges[i].Label < edges[j].Label
			}
			return edges[i].To < edges[j].To
		}
		return edges[i].From < edges[j].From
	})

	return GraphJSON{Nodes: nodes, Edges: edges}
}
