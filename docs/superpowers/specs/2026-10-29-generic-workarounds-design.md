# Design: Design-Agnostic Workarounds (State Reconciliation + Universal Algorithms)

Date: 2026-10-29
Status: Approved

## Problem

The Fusion 360 → SolidCraft translator replays a Fusion timeline in SolidCraft. Several
compensations for Fusion-API unreliability and SolidCraft kernel differences are keyed on
feature/sketch/body **names** from one specific design (TestDrive Fifi Xiao v29):
`Extrude56/57/59/60`, `Corne36_Left_MCULid`, `Xiao_RP2040_Fifi_*`, `usbc`, `Socket`,
`connectors`, `Sketch1`, `bottom_covers`, plus a name-substring + face-count mirror
heuristic. This makes the translator specific to that design.

Root causes:

1. Fusion's `participantBodies`/`inputEntities` are unreliable for target selection.
2. Fusion body renames are not timeline features, so the extractor misses them and IR body
   names drift from reality as the timeline advances.
3. SolidCraft kernel quirks (FP-sensitive booleans, profile region detection, centroid
   discrepancies) that were patched per-sketch instead of universally.

## Goals

- Remove all design-specific literals and heuristics from `pkg/`.
- TestDrive Fifi Xiao v29 still replays end-to-end with all verification checkpoints passing.
- A second design replays without name-specific code paths.

## Non-Goals

- Improving extraction fidelity beyond body snapshots (e.g., thread geometry, occurrences).
- Changing the SolidCraft MCP tool surface.
- Handling arc-arc intersection splitting (only arc-circle, as today, but universally).

## Part 1 — Ground-truth pipeline: Fusion body snapshots + state reconciliation

### A. IR schema (`pkg/cadir/ast.go`)

```go
type BodySnapshot struct {
    Name      string  `json:"name"`
    VolumeMM3 float64 `json:"volume_mm3"`
    AreaMM2   float64 `json:"area_mm2"`
    Centroid  Point3D `json:"centroid"` // center of mass
    BBoxMin   Point3D `json:"bbox_min"`
    BBoxMax   Point3D `json:"bbox_max"`
}
```

`TimelineStep` gains `BodySnapshot []BodySnapshot \`json:"body_snapshot,omitempty"\`` —
the complete body inventory in Fusion immediately after that timeline step executes.

### B. Extractor (`pkg/fusion/extractor.go`)

The extraction script already iterates every body for `target_stats` on every step; it
additionally emits per-body records into `body_snapshot`. No extra timeline rolls.

### C. Compiler reconciliation (`pkg/solidcraft/compiler.go`)

New `reconcileBodies(expected []BodySnapshot)`, run after **every** executed step in
`CompileModelWithOptions`:

1. Read actual SolidCraft bodies via `inspect_design` (volume, area, center of mass, bbox,
   face counts are already returned per body).
2. Greedy best-first geometric matching actual↔expected. A pair is a match candidate when
   relative volume difference ≤ 5% AND bbox-center distance ≤ 1 mm; among candidates pick
   the best score (volume agreement + center distance).
3. For matched pairs whose names differ, rename actual→Fusion name with collision-safe
   ordering: perform renames whose target name is currently free first; break cycles with
   temporary names.
4. Every rename is logged loudly. After reconciliation `bodyMap` is effectively identity,
   so body names recorded in the IR at each timeline point resolve correctly by construction.

This single mechanism replaces:

- **`Extrude60` rename chain** (`finalizeExtrudeBodies`): reconciliation applies Fusion's
  renames exactly when they happened.
- **`mergeParticipantBodies` name-skip hack**: merged participants := participants **absent
  from the post-step Fusion snapshot** (Fusion's own truth about what it consumed).
  Participants still present in the snapshot are not merged (fixes `Extrude56` and the
  bottom-cover join steps). Each consumed participant still present in SolidCraft is merged
  into the result body with maximal bbox overlap. If a step has no snapshot (older IR),
  fall back to the existing bbox-touch behavior.
- **`Extrude56/57/59/60` + `Corne36_Left_MCULid` target override**: deleted; join/cut
  targets resolve from participant/result names against reconciled state. Generic fallback:
  if a listed body name does not resolve, select bodies whose bbox intersects the
  swept-volume bbox computed from the sketch plane, profile bbox, and extrude distance.
- **Mirror hardcoded lid target and `"mcucover"` face-count heuristic**: deleted;
  `target_bodies` resolve correctly post-reconciliation, falling back to the last body.

## Part 2 — Universal sketch algorithms

- **Coordinate snap** (replaces `"usbc"`): snap all sketch coordinates sent to SolidCraft —
  lines, arcs, circles, splines — to a 1e-4 mm grid so coincident/tangential geometry is
  exactly coincident.
- **Arc/circle splitting** (replaces `"Socket"`): for every sketch, split each arc at all
  intersection points with any circle in the same sketch that lie strictly interior to the
  arc's angular span (not near endpoints), emitting sub-arcs in angular order. Also fixes
  the current last-circle-only / first-intersection-only limitation.
- **Degenerate-profile skip** (replaces `"connectors"`): a SolidCraft profile whose curve-ID
  list repeats an ID is degenerate and skipped for all sketches.
- **Size-aware profile matching** (replaces `"Sketch1"` tolerance): candidates scored by
  area agreement plus centroid distance with tolerance scaling with `sqrt(area)`; no
  sketch-specific constants. Constants tuned against the TestDrive regression run.
- **Claim-based compound matching** (replaces `"bottom_covers"` guard): a sub-profile is
  added to a Fusion profile's compound match only if that profile is the nearest Fusion
  profile to the sub-profile's centroid (mutual-exclusivity claim test).

## Part 3 — `--skip-steps` diagnostic flag

- `-skip-steps "51,52"` — comma-separated step indices; `a-b` ranges supported.
- Skipped steps are not executed; their verification checkpoint is skipped; a notice is
  printed per skipped step.
- Reconciliation is disabled for the remainder of the run once any step is skipped
  (SolidCraft state intentionally diverges; geometric renaming from mismatched state would
  mis-rename). Logged once when disabled.

## Part 4 — Cleanup & verification

- Delete the dead `if false` block and `finalizeExtrudeBodies`; remove the unused
  `scProfilesCentroids` stub parameter from `compileMultiBodyExtrude`.
- **Literal gate**: a unit test asserts `pkg/` contains none of the known design-specific
  literals (`Extrude56`, `Sketch1`, `usbc`, `Socket`, `connectors`, `bottom_covers`,
  `Corne36`, `Xiao_`, `McuCover`, `MCULid`, …).
- **Unit tests** (no MCP required): collision-safe rename ordering, geometric matcher,
  arc-circle splitting, claim-based matching.
- **Regression**: re-extract the IR from Fusion (schema changed; Fusion must be running),
  replay TestDrive with `--find-divergence` until all checkpoints pass, then replay the
  second design.

## Risks

- Mis-renames if SolidCraft geometry diverges badly from Fusion: mitigated by conservative
  match thresholds and loud logging of every rename.
- Reconciliation adds one `inspect_design` call per step (~96 per replay): acceptable.
- Backward compatibility: IR files without `body_snapshot` still replay; reconciliation
  no-ops and participant merging falls back to bbox-touch.

## Success Criteria

1. `go build ./...`, `go test ./...` pass, including the literal-gate test.
2. TestDrive replay: all verification checkpoints pass.
3. Second-design replay completes with no design-specific code paths involved.
