# Vendor integration template

`fieldmapping.yaml` here is a **working** FieldMapping for a fictional vendor
(ExampleVendor EV100). It is the starting point for onboarding a new DPU vendor.

Use it:

```bash
make new-vendor VENDOR=acme GROUP=dpu.acme.com MODEL=A100 PREFIX=ACME
```

Read [docs/vendor-integration.md](../../docs/vendor-integration.md) for the five
things a vendor port touches and the four mistakes the template is shaped to
prevent.

## Why this file is executable, not illustrative

A template that only parses is close to worthless: a team cloning it would find
out at runtime that the CEL does not compile or a required field never resolves.

So `pkg/mapping/vendor_template_test.go` runs the real loader and the real
engine over this file on every CI run, asserts the objects it emits, and
separately runs `hack/new-vendor.sh` into a temporary directory to check the
generated output still loads, translates, and leaves no unsubstituted token
behind. Both of those caught real bugs the first time they ran.

If you change the engine and this template stops working, the mapping tests
fail — which is the point.
