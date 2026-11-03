# Hardware-aware DPF Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent unsuitable hardware from entering the pinned NVIDIA provisioning path while retaining truthful discovery.

**Architecture:** Restrict the real deployment by persistent administrator enrollment, verified PCI identity and vendor inventory. Feed observed product identity into the data-driven mapping's eligibility and readiness conditions. Preserve mock e2e and existing physical resources.

**Tech Stack:** Go, Kubernetes CEL FieldMappings, Kustomize, envtest, Kind.

**Spec:** `docs/superpowers/specs/2026-10-28-hardware-profiles.md`

## Global Constraints

- DPF remains pinned to 26.4; no invented BF2 support.
- Preserve existing workspace changes, daemon-owned Ready, and existing DPF children.
- No private lab details in new source fixtures; no real-cluster mock e2e.
- Go changes require `make lint-fix` and `make test`.

## Review Focus

- Mixed vendor host: NVIDIA firmware cannot reach an AMD inventory entry.
- Card replacement/removal: old model identity cannot authorize provisioning.
- Misdeclared BF2 as BF3: observed model must prevent emission.
- Previously Ready children: an unsupported source cannot retain DPFReady=True.
- Missing NFD labels: real deployment fails closed; mock deployment still schedules.

### Task 1: Vendor-isolated inventory and observed product identity

**Files:** `pkg/discovery/pci.go`, `inventory.go`, `types.go`, their tests, `cmd/vsp/main.go`.
**Interfaces:** `PCIEnumerator.VendorID uint16` (zero means all); product annotation from `IdentityAnnotations`.

- [x] Add tests for mixed PCI vendors, read errors on excluded vendors, observed BF2/BF3 model, stale model removal.
- [x] Run focused tests RED before implementation.
- [x] Implement filter before serial reads and identity annotation cleanup; expose `--pci-vendor` independently of mock `--vendor`.
- [x] Run discovery and VSP tests GREEN.

### Task 2: Deployment selection and DPF eligibility

**Files:** hardware VSP patch, independent daemon manifest, DPU mapping; mapping, deployment and API integration tests.
**Interfaces:** observed product annotation; `DPFHardwareCompatible` and `DPFReady` conditions.

- [x] Exercise patched manifests with Kubernetes label matching; test BF2/BF3 enrollment, ConnectX, AMD, Marvell, unlabeled nodes and cleanup after last-card removal.
- [x] Add mapping negative cases and an API transition test preserving existing children and Ready.
- [x] Run RED, then require declared/observed BF3 for all provisioning emits and DPFReady; expose explicit unsupported/unknown reasons.
- [x] Update supported provisioning fixtures to BF3 with observed identity; retain dedicated BF2 negative tests.
- [x] Run focused tests GREEN and document the profile boundary.

### Task 3: Verification and lab rollout

**Files:** generic hardware documentation; private evidence outside source repository.

- [x] Run lint-fix, make test, race tests and API integration; stage the exact source on the lab and repeat.
- [x] Run e2e in dedicated Kind clusters; remove those clusters afterward.
- [x] Request independent review; reproduce and fix important findings with RED/GREEN tests.
- [x] Build and dry-run bounded real deployment changes, then observe rollout and preserved resources.
- [x] Recheck physical acceptance gates and report remaining failures explicitly.

Execution follows the user's instruction to complete the previously presented hardware adaptation and tests in this session. Changes are made in the existing feature checkout with a protected baseline snapshot so existing uncommitted work stays intact.

Review outcome: two important findings reproduced RED and fixed GREEN: losing all model identity retained old success; transient NFD selection prevented cleanup after card removal. Persistent administrator enrollment replaces transient PCI affinity so the inventory cleaner remains scheduled. No node-write RBAC added.

Physical ARM recovery remains blocked on authenticated service access; it is not completed by this plan's software verification.
