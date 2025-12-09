# hf-operator

`hf-operator` is a Kubernetes operator that automates downloading Hugging Face models onto a PersistentVolumeClaim (PVC). Each `ModelDownload` custom resource (CR) expresses the models you need, the PVC that will hold the artifacts, and optional runtime knobs such as the node pool to run on or whether to force a re-download. The operator turns that desired state into isolated, reproducible `Job` objects—one per model—so that downstream workloads (Serving, fine-tuning, evaluation, etc.) can rely on warm local storage without having to embed download logic.

The project is built with Kubebuilder and controller-runtime and runs on any conformant Kubernetes cluster.

## Core Features

- Declarative model replication: describe one or more Hugging Face models in a CR; the operator keeps the PVC in sync.
- Secret-aware reconciliation: changes to the referenced HF token secret automatically retrigger downloads.
- Idempotent jobs: the controller hashes the model spec and download settings; jobs are recreated when something relevant changes or when `forceReload` is set.
- Status tracking: `pending`, `completed`, and `failed` arrays surface the state of each model at a glance.
- Distribution options: run straight from manifests, use the included Helm chart (`charts/hf-operator`), or ship a single `dist/install.yaml` bundle.

## Architecture & Reconciliation Flow

1. The controller watches `ModelDownload` CRs plus owned `Job` objects and referenced `Secrets`.
2. For every model declared in `.spec.models`, the reconciler ensures there is exactly one job named `hf-dl-<cr>-<model>`.
3. Each job runs an Ubuntu-based container that installs `huggingface_hub`, exports the token (if provided), optionally enables `hf_transfer`, and invokes `hf download <model> --local-dir /models/<model>`.
4. All jobs mount the PVC declared in `.spec.storagePVC` at `/models`.
5. When a job completes it is marked in `.status.completed`; failures inflight update `.status.failed`. Pending models remain listed until they succeed or fail.
6. The reconciler requeues every 20s to refresh job status and also reacts immediately to spec or secret changes.

## Custom Resource Schema

`apiVersion: hfops.kamal.dev/v1alpha1`, `kind: ModelDownload`

| Field | Type | Required | Description |
| ----- | ---- | -------- | ----------- |
| `.spec.models[]` | list of `ModelSpec` | yes | Models to download. `name` is the Hugging Face repo (e.g. `meta-llama/Llama-3.1-8B`). `sha` and `retryCount` are informational today but included in the spec hash so any change triggers a new job. |
| `.spec.storagePVC` | string | yes | Name of an existing PVC mounted at `/models`. Each model is stored inside `/models/<model-name>`. |
| `.spec.hfTokenRef` | `HFTokenSecretRef` | required for private models | Points to the secret that holds your Hugging Face access token (`name` + `key`). The secret must live in the same namespace as the CR. |
| `.spec.nodePool` | string | no | When set, pods receive `nodeSelector: {"karpenter.sh/nodepool": <value>}` so you can steer downloads to a particular Karpenter node pool. |
| `.spec.settings.timeoutMillis` | int | no | Reserved for future client-side timeouts (not yet enforced). |
| `.spec.settings.enableHFTransfer` | bool | no | Toggles `HF_HUB_ENABLE_HF_TRANSFER`, unlocking the experimental HF Transfer acceleration path. |
| `.spec.settings.keepAliveSeconds` | int | no | Sets `ttlSecondsAfterFinished` on the `Job` (defaults to 3600) so finished pods are garbage-collected. |
| `.spec.settings.cpu` / `.spec.settings.memory` | string | no | Requests/limits for the download container. Defaults: `1` CPU, `4Gi` RAM. |
| `.spec.settings.forceReload` | bool | no | When `true`, existing jobs are deleted and recreated even if the spec hash matches, ensuring a fresh download. |

Status fields:

- `.status.pending[]`: models with jobs created but not yet successful/failed.
- `.status.completed[]`: models whose job succeeded at least once.
- `.status.failed[]`: models whose job failed (controller leaves the job for inspection).

## Quick Start

### Prerequisites

- Go `>= 1.24.6` if you plan to build from source.
- Docker (or another OCI builder) `>= 17.03`.
- `kubectl` configured for a v1.26+ cluster (any modern cluster works; Kind is great for local testing).
- Existing PVC in the namespace where you deploy the CR.
- Optional: Karpenter node pools when using `.spec.nodePool`.

### 1. Build and push the controller image

```sh
make docker-build docker-push IMG=<registry>/hf-operator:<tag>
```

### 2. Install CRDs and deploy the controller

```sh
make install
make deploy IMG=<registry>/hf-operator:<tag>
```

### 3. Create the HF token secret (skip for public models)

```sh
kubectl -n <ns> create secret generic hf-token \
  --from-literal=token=<hf_access_token>
```

### 4. Author a `ModelDownload`

```yaml
apiVersion: hfops.kamal.dev/v1alpha1
kind: ModelDownload
metadata:
  name: llama-cache
  namespace: inference
spec:
  storagePVC: llama-pvc
  hfTokenRef:
    name: hf-token
    key: token
  nodePool: spot-downloaders
  settings:
    enableHFTransfer: true
    cpu: "2"
    memory: 8Gi
    keepAliveSeconds: 600
  models:
    - name: meta-llama/Llama-3.1-8B
    - name: microsoft/Phi-3.5-mini-instruct
      sha: 123abc456def # optional note for auditors
```

Apply it:

```sh
kubectl apply -f modeldownload.yaml
kubectl get modeldownloads.hfops.kamal.dev llama-cache -n inference -o wide
```

Check the per-model job logs with:

```sh
kubectl logs job/hf-dl-llama-cache-meta-llama-llama-3-1-8b -n inference
```

### Uninstall

```sh
kubectl delete -f modeldownload.yaml
make undeploy
make uninstall
```

## Installation Options

### From source (Kustomize-based)

```sh
make install
make deploy IMG=<registry>/hf-operator:<tag>
```

`make undeploy` and `make uninstall` reverse the process.

### Pre-built installer bundle

```sh
make build-installer IMG=<registry>/hf-operator:<tag>
kubectl apply -f dist/install.yaml
```

Host the resulting manifest and users can install with a single `kubectl apply -f <url>`.

### Helm chart

The chart in `charts/hf-operator` mirrors the default Kustomize deployment and can also install the CRDs.

```sh
helm upgrade --install hf-operator charts/hf-operator \
  --namespace hf-operator --create-namespace \
  --set image.repository=<registry>/hf-operator \
  --set image.tag=<tag>
```

Key values:

- `image.repository` / `image.tag`: controller image.
- `serviceAccount.*` / `rbac.create`: RBAC generation toggles.
- `installCRDs`: set to `false` if CRDs are managed elsewhere.

## Managing Downloads

- **Force a refresh**: Patch `.spec.settings.forceReload=true` and the operator will delete/recreate jobs, guaranteeing new artifacts on disk.
- **Scheduling control**: `.spec.nodePool` constrains downloads to nodes labeled `karpenter.sh/nodepool=<value>`. Remove the field to let the scheduler choose.
- **Resource sizing**: adjust `.spec.settings.cpu` / `.spec.settings.memory` to match the size of the artifacts being pulled.
- **PVC hygiene**: the job script skips downloads if `/models/<model>` already exists and contains files. Remove the folder (or set `forceReload`) to trigger a re-download.
- **Secret rotation**: updating the referenced secret automatically enqueues the owning CR thanks to the additional watch in `SetupWithManager`.

## Observability & Troubleshooting

- Operator logs: `kubectl logs deployment/hf-operator-controller-manager -n hf-operator`.
- Custom resource status: `kubectl get modeldownloads -A -o wide` or `kubectl describe` for per-model status arrays.
- Job inspection: `kubectl get jobs -l app=hf-downloader,parent=<cr-name> -n <ns>`.
- Metrics endpoint: disabled by default (`--metrics-bind-address=0`). Supply `--metrics-bind-address=:8443` and certificates (or `--metrics-secure=false` for HTTP) to expose controller-runtime metrics.
- Health probes: `/healthz` and `/readyz` run on `:8081` by default and are wired into the manager deployment.
- Common failure modes:
  - Secret/key missing → operator requeues every 10s until present.
  - PVC absent → job creation fails; check controller logs and ensure the PVC exists in the same namespace.
  - Download throttled → inspect job logs; consider enabling HF Transfer or increasing resources.

## Development

Common make targets (run `make help` for the full list):

- `make test`: runs unit tests with envtest (requires Kind binaries downloaded automatically).
- `make lint` / `make lint-fix`: executes `golangci-lint`.
- `make run`: runs the controller locally against the current kubeconfig.
- `make build`: produces `bin/manager`.
- `make test-e2e`: spins up a Kind cluster (`hf-operator-test-e2e`), runs Ginkgo tests in `test/e2e`, and tears the cluster down.
- `make docker-buildx`: builds and pushes multi-arch images if you have BuildKit + `docker buildx` configured.

Helpful tips:

- This repository vendors tooling into `./bin` on demand; ensure that directory is writable.
- The controller currently uses the stock Ubuntu base image and installs Python packages at runtime. For production, bake a custom downloader image and update the job template logic accordingly.
- Keep `api/v1alpha1` types and generated CRDs in sync by running `make generate && make manifests` whenever spec changes.

## Project Distribution

- **Single YAML bundle**: `make build-installer IMG=<registry>/hf-operator:<tag>` emits `dist/install.yaml`, suitable for `kubectl apply -f` workflows or GitOps tooling.
- **Helm package**: package the chart (e.g., `helm package charts/hf-operator`) and publish to an OCI registry or static chart repo.
- **CI**: GitHub workflows under `.github/workflows/` cover linting, conformance tests, and publishing releases; adjust them to point at your registry before tagging.

## Contributing

Issues and pull requests are welcome. Please:

1. Create an issue describing the change or bug.
2. Fork, branch, and run `make test lint`.
3. Include relevant docs/tests.
4. Submit a PR referencing the issue.

For significant features (new CRDs, webhook logic, etc.) open a design discussion first so the API surface remains coherent.

## License

Copyright 2025 Kamal.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
