# opi-nvidia-dpf-adapter

NVIDIA companion operator that translates OPI objects
(`DataProcessingUnit`, `ServiceFunctionChain` from
[`openshift/dpu-operator`](https://github.com/openshift/dpu-operator))
into DPF objects (`DPU`, `DPUService`, … from
[`NVIDIA/doca-platform`](https://github.com/NVIDIA/doca-platform)).

This is the Kubernetes analogue of
[`opi-nvidia-bridge`](https://github.com/opiproject/opi-nvidia-bridge):
NVIDIA-specific code lives **here**, not in-tree in `dpu-operator`.

The OPI→DPF field mapping is **data**. The controller loads
`config/mappings/*.yaml` (CEL + JSONPath) and interprets them. There is
no `switch obj.GetKind()` and no hardcoded struct copies. See
[docs/mapping-spec.md](docs/mapping-spec.md).

```
OPI DataProcessingUnit  --mapping yaml-->  DPUDevice + DPUFlavor + BFB + DPU
OPI ServiceFunctionChain --mapping yaml-->  DPUService (one per Helm chart NF)
```

## Mapping files

| File | Watches | Emits |
|---|---|---|
| `config/mappings/dataprocessingunit.yaml` | `config.openshift.io/v1 DataProcessingUnit` | `DPUDevice`, `DPUFlavor`, `BFB`, `DPU` |
| `config/mappings/servicefunctionchain.yaml` | `config.openshift.io/v1 ServiceFunctionChain` | `DPUService` per `networkFunctions[].chart` |

Run the interpreter without a cluster:

```sh
go test ./pkg/mapping/ -count=1
```

Run the operator against a cluster that already has the OPI and DPF CRDs:

```sh
go run ./cmd/main.go --mapping-dir=config/mappings --metrics-bind-address=0
```

Hardware identity such as `serialNumber` is **not** invented. Until the
NVIDIA VSP can discover it, set:

```yaml
metadata:
  annotations:
    provisioning.dpu.nvidia.com/serial-number: "MT1234"
    dpu.nvidia.com/bfb-url: "https://example.invalid/fw.bfb"
```

## Description
Companion adapter for the OPI DPU Operator. It does not own OPI CRDs.

## Getting Started


### Prerequisites
- go version v1.24.6+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/opi-nvidia-dpf-adapter:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/opi-nvidia-dpf-adapter:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following the options to release and provide this solution to the users.

### By providing a bundle with all YAML files

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/opi-nvidia-dpf-adapter:tag
```

**NOTE:** The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without its
dependencies.

2. Using the installer

Users can just run 'kubectl apply -f <URL for YAML BUNDLE>' to install
the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/opi-nvidia-dpf-adapter/<tag or branch>/dist/install.yaml
```

### By providing a Helm Chart

1. Build the chart using the optional helm plugin

```sh
kubebuilder edit --plugins=helm/v2-alpha
```

2. See that a chart was generated under 'dist/chart', and users
can obtain this solution from there.

**NOTE:** If you change the project, you need to update the Helm Chart
using the same command above to sync the latest changes. Furthermore,
if you create webhooks, you need to use the above command with
the '--force' flag and manually ensure that any custom configuration
previously added to 'dist/chart/values.yaml' or 'dist/chart/manager/manager.yaml'
is manually re-applied afterwards.

## Contributing
// TODO(user): Add detailed information on how you would like others to contribute to this project

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2026 Kartikey Gupta.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

