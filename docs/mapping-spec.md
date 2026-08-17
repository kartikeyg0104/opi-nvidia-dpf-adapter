# FieldMapping spec

The TSC mandate for this companion repo is that **OPI→DPF field mapping is data**, not hardcoded Go structs or `switch` statements on Kind.

This file is the schema. The interpreter lives in `pkg/mapping`. Concrete documents live in `config/mappings/`.

## Document shape

```yaml
apiVersion: translation.opi.nvidia.com/v1alpha1
kind: FieldMapping
metadata:
  name: dataprocessingunit          # used as the controller name and a label
  description: human-readable notes
source:                            # the OPI object this mapping watches
  group: config.openshift.io
  version: v1
  kind: DataProcessingUnit
emit:                              # one or more DPF objects to write
  - target:
      group: provisioning.dpu.nvidia.com
      version: v1alpha1
      kind: DPU
    name:
      from: metadata.name          # JSONPath | cel | value
    namespace:
      cel: "source.metadata.?namespace.orValue('') != '' ? source.metadata.namespace : 'dpf-operator-system'"
    when: ""                       # optional CEL; skip this emit when false
    forEach:                       # optional; repeats this emit per list item
      in: spec.networkFunctions    # JSONPath selecting a list
    fields:
      - to: spec.dpuNodeName       # dotted JSONPath on the destination
        from: spec.nodeName        # exactly one of from | cel | value
        default: ""                # used when from/cel is empty
        required: false            # fail the mapping if still empty
```

## How a field is resolved

Exactly one source per field:

| Key | Meaning |
|---|---|
| `from` | Dotted JSONPath on the OPI object. Prefix `item.` to read the current `forEach` element. |
| `cel` | CEL expression. Variables: `source` (the whole OPI object as a map), `item` (current forEach element, or `{}`). |
| `value` | Literal YAML (string, bool, number, object, list). |

Then, if the result is null or `""` and `default` is set, `default` is used. If `required: true` and the result is still empty, Apply returns an error and the controller does not write.

## Why JSONPath *and* CEL

JSONPath is the 1:1 case: `spec.nodeName` → `spec.dpuNodeName`.

CEL is everything JSONPath cannot say:

- annotation keys that contain `/`
- conditionals (`'chart' in item`)
- string concat (`source.metadata.name + '-device'`)
- optional chaining (`source.metadata.?namespace.orValue('')`)

Defaults (`value: true` on `spec.nodeEffect.noEffect`) are how the adapter fills DPF-required fields that OPI does not have. Changing a default is a YAML edit, not a Go rebuild.

## One-to-many

A single OPI `DataProcessingUnit` cannot become a single DPF `DPU`. `dpuDeviceName`, `dpuFlavor`, and `bfb` are required references. The mapping file therefore has four `emit` entries. Adding a fifth DPF object is another YAML block, not another reconciler.

`forEach` is the same idea for lists: one `ServiceFunctionChain` emits one `DPUService` per `networkFunctions[]` entry that has a chart.

## What the Go code is allowed to do

`pkg/mapping` may parse this schema, walk JSONPath, evaluate CEL, and return unstructured objects.

It may **not**:

- `switch obj.GetKind()`
- import DPF or OPI Go types to copy struct fields
- encode NVIDIA-specific defaults in Go

NVIDIA-specific defaults belong in `config/mappings/*.yaml`. That is the companion-repo analogue of `opi-nvidia-bridge`: vendor knowledge lives here, not in `openshift/dpu-operator`.
