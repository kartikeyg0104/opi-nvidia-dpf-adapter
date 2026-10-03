# FieldMapping spec

The TSC mandate for this companion repo is that **OPI→DPF field mapping is data**, not hardcoded Go structs or `switch` statements on Kind.

This file is the schema. The interpreter lives in `pkg/mapping`. Concrete documents live in `config/mappings/`.


## Machine-readable schema

`config/mappings/fieldmapping.schema.json` is this document in JSON Schema form
(draft 2020-12). Every shipped mapping carries a modeline, so editors with
`yaml-language-server` give completion and inline errors:

```yaml
# yaml-language-server: $schema=./fieldmapping.schema.json
```

The schema encodes the rules, not just the field names: `exactly one of
from/cel/value` as a `oneOf`, and the reserved `Ready` condition type as a `not`
pattern — so an editor rejects it before the controller refuses to start.

It is hand-written, which means it could drift from the Go structs the engine
actually uses. `pkg/mapping/schema_test.go` reflects over the struct json tags
and fails if either side has a field the other does not, so a schema that
reported a valid field as an error could not merge.

This file is also the artifact the architecture review's Phase 1 asks for when
it says to ratify the mapping-spec format as an OPI subproject: a contract that
can be versioned and depended on, rather than prose plus a Go package.

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

## Spec-level defaults

`defaults:` supplies values inherited by every emit that does not set them,
removing per-emit boilerplate. Today it carries `namespace` (a `Value`), which
an emit uses only when it declares no `namespace` of its own:

```yaml
defaults:
  namespace:
    cel: "source.metadata.?namespace.orValue('') != '' ? source.metadata.namespace : 'dpf-operator-system'"
```

`name` and `namespace` (both `Value`) also accept `default:` and `required:`,
mirroring `fields[]`: `default` supplies a fallback when `from`/`cel` resolves
empty, and `required: true` fails the mapping if it is still empty.

## Status mirroring

`status:` is the reverse direction of `emit:` — it rolls the status of the
emitted DPF children back onto the OPI source's `.status`. It keeps status
roll-up as data, not Go. Rules evaluate with two CEL variables:

- `source` — the OPI object as a map.
- `children` — the list of emitted child objects (each an unstructured map),
  located by the `translation.opi.nvidia.com/*` labels the engine stamps.

```yaml
status:
  fields:                          # dotted paths rooted at the object
    - to: status.serviceCount
      cel: "children.size()"
  conditions:                      # upserted onto status.conditions by type
    - type: DPFReady               # NOT "Ready" -- see the reserved type below
      status: "children.size() > 0 && children.all(c, c.?status.?conditions.orValue([]).exists(cond, cond.type == 'Ready' && cond.status == 'True'))"
      reason: AllServicesReady
      message: "All translated DPUServices report Ready"
```

`status.when` gates the whole block: when it is present and evaluates false,
nothing is mirrored at all — no conditions, no fields. Set it to the same guard
your `emit` rules use, or a mapping that emits nothing for a card will still
write a `False` condition onto it.

Each `conditions[]` entry's `status` is a CEL expression that must evaluate to
bool; `type`, `reason`, and `message` are literals. Write null-safe CEL
(`.?field.orValue(...)`) so children that have not yet published status do not
error the roll-up. The controller upserts conditions by `type`, preserving
`lastTransitionTime` while a condition's status is unchanged and stamping a
fresh time when it flips.

Note the asymmetry in the example above: the condition this mapping *writes* is
`DPFReady`, while the CEL *reads* `cond.type == 'Ready'` on the children. That
is deliberate — the children are DPF's own objects publishing their own `Ready`,
which is exactly what the roll-up should consult.

### Reserved condition type: `Ready`

A mapping document **may not** write a condition of type `Ready`. On an OPI
source that condition belongs to the per-node dpu-operator daemon
(`plugin.ReadyConditionType`); since the controller upserts by `type`, a mapping
writing it would fight the daemon and flap the condition on every reconcile.

`Spec.Validate` rejects it case-insensitively, so such a document fails to load
and the controller will not start. Own a vendor-scoped condition instead
(`DPFReady`, `DSCReady`). The constant is `mapping.ReservedConditionType`, and
`docs/multi-vendor.md` §3 explains the single-writer model in full.

## Multiple vendors on one source kind

Two mapping documents may declare the same `source`. `cmd/main.go` then starts
one controller per mapping and both reconcile every object of that kind, so each
`emit` needs a `when` guard that claims only its own hardware, and each mapping
must mirror a **distinct**, vendor-scoped condition type — neither sharing one
with each other, nor taking the daemon's reserved `Ready`.

To start a new vendor from a working, CI-tested skeleton rather than from this
reference, run `make new-vendor` and read
[docs/vendor-integration.md](vendor-integration.md).

`config/mappings/amd-dsc200.yaml` is the worked example, and
[docs/multi-vendor.md](multi-vendor.md) covers the routing, the single-writer
status model, and what is still needed from the lab hosts.
