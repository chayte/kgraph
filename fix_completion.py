import re

with open('cmd/kubectl-graph/main.go', 'r') as f:
    content = f.read()

old = (
    "\t\tnamespaces=(${(f)\"$(kubectl get ns -o custom-columns=':metadata.name' --no-headers 2>/dev/null)\"})\n"
    "\t\t_arguments -s \\\n"
    "\t\t\t'(-n --namespace)'{-n+,--namespace=}'[Namespace]:namespace:->ns' \\\n"
    "\t\t\t'--output=[Output format]:format:(ascii mermaid tree)'\n"
    "\n"
    "\t\tcase $state in\n"
    "\t\t\tns)\n"
    "\t\t\t\t_kubectl_graph_add_matches \"${namespaces[@]}\"\n"
    "\t\t\t\t;;\n"
    "\t\tesac\n"
    "\t\treturn"
)

new = (
    "\t\tnamespaces=(${(f)\"$(kubectl get ns -o custom-columns=':metadata.name' --no-headers 2>/dev/null)\"})\n"
    "\t\tlocal state\n"
    "\t\t_arguments -s -C \\\n"
    "\t\t\t'(-n --namespace)'{-n+,--namespace=}'[Namespace]:namespace:->ns' \\\n"
    "\t\t\t'--output=[Output format]:format:(ascii mermaid tree)' \\\n"
    "\t\t\t':subcommand:' \\\n"
    "\t\t\t':resource name:()'\n"
    "\n"
    "\t\tcase $state in\n"
    "\t\t\tns)\n"
    "\t\t\t\t_kubectl_graph_add_matches \"${namespaces[@]}\"\n"
    "\t\t\t\t;;\n"
    "\t\tesac\n"
    "\t\treturn"
)

count = content.count(old)
print(f"Found {count} occurrences")

if count == 3:
    content = content.replace(old, new)
    with open('cmd/kubectl-graph/main.go', 'w') as f:
        f.write(content)
    print("Replaced all 3 occurrences successfully")
else:
    print("ERROR: expected 3 occurrences")
