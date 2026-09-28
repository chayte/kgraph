# kubectl-graph

`kubectl-graph` is a Kubernetes plugin that helps you quickly understand workload and network relationships from the command line.

It provides a dependency-oriented view so you can inspect how resources connect without chaining multiple `kubectl get ...` commands.

## What It Shows

Depending on the command, the graph can include:

- Ingress -> Service
- Service -> Pod
- Workload controller -> Pod (`Deployment`, `StatefulSet`, `DaemonSet`, `Job`, `CronJob`, `ReplicaSet`)
- Pod -> PVC -> PV (storage dependencies)

## Output Formats

Supported output modes:

- `ascii` (default)
- `tree`
- `mermaid`
- `json`

## Requirements

- Go 1.26+ to build from source
- Access to a Kubernetes cluster through your current kubeconfig context
- Kubernetes 1.35 to 1.37 (minimum and maximum supported versions)
- Read permissions (RBAC) for the resources you query (`ingresses`, `services`, `pods`, `deployments`, `replicasets`, `persistentvolumeclaims`, `persistentvolumes`, and related APIs)

## Install a Prebuilt Release

Download the latest archive matching your operating system and architecture from [GitHub Releases](https://github.com/chayte/kgraph/releases/latest). Choose `darwin` for macOS, `linux` for Linux, or `windows` for Windows; choose `arm64` for Apple Silicon or ARM machines, and `amd64` for Intel/AMD machines. Windows archives use `.zip`; macOS and Linux archives use `.tar.gz`.

On macOS or Linux, set the release version:

```bash
VERSION=v0.2.3
```

Then run this block to detect your OS and CPU architecture, download the matching archive, and install the plugin. It defaults to `v0.2.3` if the version variable was not set in the current shell:

```bash
VERSION="${VERSION:-v0.2.3}"
case "$(uname -s)" in
  Darwin) OS=darwin ;;
  Linux) OS=linux ;;
  *) echo "Unsupported operating system" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  arm64|aarch64) ARCH=arm64 ;;
  x86_64|amd64) ARCH=amd64 ;;
  *) echo "Unsupported CPU architecture" >&2; exit 1 ;;
esac
ARCHIVE="kubectl-graph_${VERSION}_${OS}_${ARCH}.tar.gz"
curl -fL "https://github.com/chayte/kgraph/releases/download/${VERSION}/${ARCHIVE}" -o "$ARCHIVE"
tar -xzf "$ARCHIVE"
mkdir -p ~/.local/bin
install -m 0755 "kubectl-graph_${VERSION}_${OS}_${ARCH}/kubectl-graph" ~/.local/bin/kubectl-graph
export PATH="$HOME/.local/bin:$PATH"
```

If `~/.local/bin` is not already in your `PATH`, add `export PATH="$HOME/.local/bin:$PATH"` to `~/.zshrc` (zsh) or `~/.bashrc` (bash), then open a new terminal.

### Enable Shell Completion

Run the commands for your shell to enable completion for both `kubectl-graph` and `kubectl graph`.

#### zsh

```bash
kubectl-graph completion zsh > ~/.kubectl-graph-completion.zsh
grep -qxF 'source "$HOME/.kubectl-graph-completion.zsh"' ~/.zshrc || echo 'source "$HOME/.kubectl-graph-completion.zsh"' >> ~/.zshrc
source ~/.zshrc
```

#### bash

```bash
kubectl-graph completion bash > ~/.kubectl-graph-completion.bash
grep -qxF 'source "$HOME/.kubectl-graph-completion.bash"' ~/.bashrc || echo 'source "$HOME/.kubectl-graph-completion.bash"' >> ~/.bashrc
source ~/.bashrc
```

On Windows, download and extract `kubectl-graph_v0.2.3_windows_amd64.zip` or `kubectl-graph_v0.2.3_windows_arm64.zip`, then add the extracted directory containing `kubectl-graph.exe` to your `PATH`.

Each release archive includes this project's license, third-party license texts, and a CSV license report. `SHA256SUMS` is available on the release page to verify downloaded archives.

To publish a new release, push a new version tag, for example `v0.2.0`:

```bash
git tag v0.2.0
git push origin v0.2.0
```

GitHub Actions then builds the release archives and attaches them to the GitHub Release for that tag.

## Build

```bash
go mod tidy
go build -o bin/kubectl-graph ./cmd/kubectl-graph
```

## Install as a kubectl Plugin

`kubectl` automatically discovers executables named `kubectl-*` in your `PATH`.

Example:

```bash
mkdir -p ~/.local/bin
cp bin/kubectl-graph ~/.local/bin/
export PATH="$HOME/.local/bin:$PATH"
```

## Usage

```bash
kubectl graph <resource> <name> -n <namespace>
kubectl graph ingress web -n prod
kubectl graph service web-svc -n prod
kubectl graph deployment web -n prod
kubectl graph ingress web -n prod --output tree
```

`-n/--namespace` can be placed before or after the resource name.

## ASCII View Notes

The `ascii` mode is optimized for terminal readability and includes key metadata such as:

- routes (`host`, `path`, backend target)
- service properties (`type`, `ClusterIP`, ports)
- endpoint and pod details (`IP`, `phase`, container ports, owner)
- PVC/PV details for storage queries

Values are styled with ANSI bold for readability.
To disable styling:

```bash
NO_COLOR=1 kubectl graph ...
```

## Mermaid Example

```mermaid
graph LR
  ingress_default_web(["Ingress\nweb"])
  service_default_web_svc(["Service\nweb-svc"])
  deployment_default_web["Deployment\nweb"]
  pod_default_web_abc["Pod\nweb-abc"]

  ingress_default_web --> service_default_web_svc
  service_default_web_svc -.-> pod_default_web_abc
  deployment_default_web ==> pod_default_web_abc
```

## Current Scope

- Namespace-scoped queries (single namespace at a time)
- Read-only graph reconstruction from Kubernetes APIs
- Designed for quick operational visibility in CLI workflows

## Design Choice

The plugin reads from Kubernetes APIs (like `kubectl`) rather than directly from etcd. This keeps it:

- portable
- RBAC-compatible
- stable across Kubernetes environments

## License

This project is licensed under the Apache License 2.0. See [LICENSE](LICENSE) for details.
