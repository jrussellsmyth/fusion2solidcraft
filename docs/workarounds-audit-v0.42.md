# Conversion Workarounds Audit (SolidCraft v0.30.1 → v0.42.0)

**Date:** 2026-11-15  
**Scope:** Audit of current Fusion 360 → SolidCraft conversion workarounds in `fusion2solidcraft`, evaluated against the [SolidCraft releases](https://github.com/tangency-labs/solidcraft/releases) from `v0.30.1` through `v0.42.0-nightly.20261115.134.1`.

---

## Summary: Workarounds Likely Impacted by SolidCraft Upgrades

| Workaround | Location | Status Assessment | Key Evidence |
| :--- | :--- | :--- | :--- |
| Raised-tool cut retry (`StartOffset + {1, 5}`) & new-body + combine fallback | `pkg/solidcraft/compiler.go` (`compileAndExecuteExtrude`, `compileMultiBodyExtrude`) | **Likely removable / less needed** | Extensive touching/coincident/curved-face boolean fixes in v0.40.1–v0.42.0-nightly |
| Coincident topology `status 1001` absorption & multi-pass combine fallback | `pkg/solidcraft/compiler.go` (`mergeParticipantBodies`, `compileAndExecuteCombine`) | **Likely removable / less needed** | `relate: upstream status 1001`, self-touching & strip tolerance fixes in v0.42.0 |
| Fillet `< 15 mm` short B-spline span filtering & `TangentChain: false` | `pkg/solidcraft/compiler.go` (`compileAndExecuteFillet`) | **Likely removable / less needed** | Retired `fillet_trace_rejects_short_step` (v0.42.0), 2° tangent chain alignment (v0.41.0), freeform/spline fillet fixes |
| Sketch pre-conditioning (`SeparateIsolatedTangentCircles`, `SplitArcsAtCircles`, `BridgeOpenCollinearGaps`) | `pkg/cadir/precondition.go`, `pkg/solidcraft/compiler.go` | **Partially removed (2 of 3)** | Verified on SolidCraft 0.42.0: `SeparateIsolatedTangentCircles` & `SplitArcsAtCircles` removed; `BridgeOpenCollinearGaps` still required (0.10 mm gap yields 0 profiles without it) |
| Recursive multi-profile batch bisection (`executeBatchExtrude`) & `pending` polling | `pkg/solidcraft/compiler.go`, `pkg/solidcraft/client.go` | **Faster / less triggered** | 10× / linear-time multi-hole extrusions & cuts (v0.40.0–v0.42.0-nightly); `running` responds during `batch` |
| Coplanar subdivided face collection (`collectCoplanarFaces`) | `pkg/solidcraft/compiler.go` | **Possibly simplified — re-test** | Coplanar sheet merging (v0.42.0) and flat B-spline planar recognition |
| Move rotate+translate split, body reconciliation, temporary-merge lookahead | `pkg/solidcraft/compiler.go`, `pkg/cadir/snapshot.go` | **Unchanged — keep** | No `solid.move` dual-transform changes; reconciliation/lookahead bridge Fusion vs. SolidCraft semantics |

---

## 1. Workarounds Likely No Longer Necessary or Changed

### 1.1 Raised-Tool Cut Retry & Coincident / Touching-Face Boolean Fallbacks
* **Current implementation:**
  * `compileAndExecuteExtrude` (`pkg/solidcraft/compiler.go`, lines 1520–1539) and `compileMultiBodyExtrude` (lines 2427–2449): retries failed `cut` extrudes with `StartOffset + raise` and `Distance - raise` for `raise ∈ {1, 5}` mm to avoid boolean failures on touching curved faces (see `BUG_REPORT.md`).
  * `compileAndExecuteExtrude` (lines 1541–1589) and `compileMultiBodyExtrude` (lines 2453–2498): falls back to extruding with `operation: "new"` followed by `solid.combine` when direct `join` (or multi-body `cut`/`join`) fails.
  * `mergeParticipantBodies` (lines 879–933): catches SMLib coincident-topology errors (e.g. `status 1001`) by checking if either body already holds the expected post-step volume and removing/renaming the redundant body.
  * `compileAndExecuteCombine` (lines 1933–1973): falls back to sequential pair-by-pair `solid.combine` across up to 3 passes when multi-tool combine fails.
* **Changelog evidence:**
  * **v0.42.0:** *"Kernel booleans: intersecting or cutting parts whose round walls coincide only locally no longer fails"* (PR #15869).
  * **v0.42.0:** *"Booleans: two solids joined where they touch along a line no longer fail with 'relate: upstream status 1001'"* (PR #16473).
  * **v0.42.0:** *"solidcore: a boolean operand touching itself along a line is glued into one edge first"* (#13650 / PR #16392).
  * **v0.42.0:** *"Kernel booleans: two boxes that meet across a strip thinner than tolerance now union into one solid by default (retire the WR-614/WR-619 flags)"* (PR #16028), and *"two parts whose flat walls drift apart beyond tolerance no longer fail to join"* (PR #15830).
  * **v0.42.0:** *"Booleans: a pocket grazing a slanted face is cut instead of refused"* (PR #16187); *"a cut leaving an earlier cut's flat side at its tangent round no longer fails in make_faceuse"* (#15247 / PR #16380).
  * **v0.42.0-nightly:** *"Join: a box onto a face that a near-tangent arc grazes no longer fails with 'another entity is already related'"* (PR #16643); *"a slab a hair past a fillet's tangent edge no longer fails the vertex gap check (2e-5 to 2.5e-5 mm)"* (PR #16538).
  * **v0.42.0-nightly:** *"Boolean and Split Body: a body with a cylinder face is no longer refused over a generating curve 4e-8 mm off its cylinder"* (PR #16504); *"Booleans: cuts no longer fail with 'related vertex is not an end of the edge use'"* (PR #16425); *"Extrude: a cut up to a face picked with To Object builds when the preview shows it"* (PR #16717).
  * **v0.41.0:** *"Booleans: a join that fills a hollow completely gives a solid (#14733), and tangent faces at a shared edge join at any size (#15229)"*; *"a box edge cutting a small round part no longer loses the cut (#15587)"*.
  * **v0.40.1:** *"A cut through a body's edge builds again when its sketch sits a few micrometres off that edge"* (#14849).

### 1.2 Fillet Short B-Spline Span Filtering & Forced `TangentChain: false`
* **Current implementation:**
  * `compileAndExecuteFillet` (`pkg/solidcraft/compiler.go`, lines 2183–2229): when multiple edges are selected, filters out isolated short (`< 15 mm`) edges to avoid blend termination failures on interior-knot-split degree-5 B-spline spans, and passes `TangentChain: false` to `solid.fillet`.
* **Changelog evidence:**
  * **v0.42.0:** *"solidcore: retire `fillet_trace_rejects_short_step` (WR-503)"* (PR #13982) — directly removes the short-step trace rejection.
  * **v0.41.0:** *"A fillet or chamfer that runs through a sharp corner a few degrees off tangent, common where a sketch line runs into an arc, is mitred instead of failing with status 1001 (#15370)"* and *"Fillet, chamfer, shell and edge selection now agree on one 2° angle for a tangent chain, so a near-smooth outline is rounded whole (#15445)"*.
  * **v0.42.0:** *"solidcore: a chain round whose rail lies wholly on the tangent face beyond rolls fully onto it"* (#15767 / PR #16149); *"an edge split by a cylinder's or sphere's seam is rounded across the seam, not refused"* (#13752 / PR #15637).
  * **v0.42.0:** *"Fillet: a second fillet on a body whose first fillet made freeform faces is no longer refused"* (PR #16299); *"root rounds where a lofted horn meets a wing build instead of failing with status 1001"* (PR #16426).
  * **v0.42.0-nightly:** *"solidcore: blends at a near-straight corner join only below the B-rep's intersection tolerance"* (#16627 / PR #16681); *"rounds joined at a crease of a spline loft's rim keep a certified definition or the fillet fails, never a face that cannot be read back"* (PR #17020).

### 1.3 Sketch Pre-conditioning (`SeparateIsolatedTangentCircles`, `SplitArcsAtCircles`, `BridgeOpenCollinearGaps`)
* **Current implementation (after v0.42.0 audit):**
  * `SeparateIsolatedTangentCircles`: **Removed** — empirical live tests on SolidCraft 0.42.0 (`TestLiveSketchPreconditioning/SeparateIsolatedTangentCircles` and `TestLiveModelIRSketchesWithoutPreconditioning` on `Socket` Step 0 & `bottom_covers` Step 85, including raw `solid.extrude`) passed without micron nudging.
  * `SplitArcsAtCircles`: **Removed** — empirical live tests on SolidCraft 0.42.0 (`TestLiveSketchPreconditioning/SplitArcsAtCircles` and `TestLiveModelIRSketchesWithoutPreconditioning` on `connectors` Step 71: 368 profiles, 22314.974 mm²) produced identical profiles without pre-splitting arcs.
  * `BridgeOpenCollinearGaps` (`pkg/cadir/precondition.go`): **Kept** — snaps open collinear line endpoints separated by `0.005 mm < gap < 0.25 mm`. Live test `TestLiveSketchPreconditioning/BridgeOpenCollinearGaps` on SolidCraft 0.42.0 confirmed a `0.10 mm` collinear gap still yields `0` profiles without it and closes to `1` profile (`200.0 mm²`) with it.
* **Changelog evidence:**
  * **v0.42.0-nightly:** *"solidcore: SolidCraft's planar region booleans run on solidcore-ops, made to match first (RFC 0003 step 2.10)"* (PR #15475); *"Sketch profiles: two rounded caps over one line both close a profile"* (PR #16566).
  * **v0.42.0:** *"Sketch profiles: two arcs meeting tangentially at one corner keep that corner, so a Join of the region builds"* (PR #16607).
  * **v0.41.0:** *"A sketch outline whose arcs leave a line tangentially closes into a profile again (#15560)"*; *"Extrude: adjacent profiles that touch at a tangent merge without a slit (#14106)"*; *"Profiles: a circle drawn twice is one hole of the ring around it (#13002)"*.
  * *Caveat:* **v0.40.0-rc.4** noted *"a 40 mm hole that touches a plate's edge at one point splits the plate into 2 bodies"*, so `SeparateIsolatedTangentCircles` should be re-tested before removal.

### 1.4 Multi-Profile Batch Bisection & `pending` Polling
* **Current implementation:**
  * `executeBatchExtrude` (`pkg/solidcraft/compiler.go`, lines 2503–2531): recursively bisects multi-profile extrudes to apply valid profiles in minimal batches while isolating kernel-failing profiles.
  * `CallTool` (`pkg/solidcraft/client.go`, lines 179–227): polls `pending` generously for long-running multi-profile booleans.
* **Changelog evidence:**
  * **v0.40.0:** *"Large flat extrusions, such as a plate full of holes, are about 10× faster, and joins of prisms about 1.5× faster"*.
  * **v0.41.0:** *"Extruding a flat face with many holes takes time that grows linearly with the hole count, not quadratically (#15232)"*.
  * **v0.42.0-nightly:** *"Booleans: cutting many holes in one cut no longer runs point solves whose answer nothing reads"* (#16704 / PR #16708) and *"a cut with a list of tools sets up its bookkeeping once, not once per tool"* (#16705 / PR #16709).
  * **v0.40.0-nightly:** *"mcp: `running` answers at once while a long `batch` runs"* (#14007 / PR #14083).

### 1.5 Coplanar Subdivided Face Collection
* **Current implementation:**
  * `collectCoplanarFaces` (`pkg/solidcraft/compiler.go`, lines 612–695): queries `body_topology` to collect all coplanar subdivided faces within the target face's bbox for face extrusions.
* **Changelog evidence:**
  * **v0.42.0:** *"solidcore: merge a sheet's coplanar faces, with old-to-new edge and face maps"* (PR #16040).
  * **v0.40.0-nightly:** *"Planar faces: a flat B-spline face (a loft side through three or more sections) is planar"* (#14346 / PR #14394).

---

## 2. Full Inventory of Remaining Workarounds (Unchanged by Changelog)

These workarounds have no evidence of being obsoleted by the v0.30.1–v0.42.0 changelog (most bridge Fusion 360 vs. SolidCraft semantic or convention differences rather than kernel bugs):

1. **Arc CCW normalization** — `NormalizeArc` (`pkg/cadir/precondition.go`, lines 8–40): swaps `Start`/`End` to match SolidCraft's CCW convention relative to `MidPoint`.
2. **Spline loose-end tying** — `compileAndExecuteSketch` (`pkg/solidcraft/compiler.go`, lines 582–584): appends `sketch.loose_ends.tie` for sketches with splines.
3. **Profile matching heuristics** — `compileAndExecuteExtrude` (`pkg/solidcraft/compiler.go`, lines 1147–1307) & `isNearestFusionProfile` (lines 2250–2264): area-ratio filtering, size-scaled centroid tolerance, deficit-hole matching, compound-profile bbox matching (`Area > 300 mm²`), `ProfileCentroids` fallback, and `< 0.05 mm²` sliver filtering.
4. **Swept-volume target fallback** — `sweptVolumeTargets` (`pkg/solidcraft/compiler.go`, lines 1608–1708): falls back to bbox overlap against the swept extrusion volume when IR body names do not resolve.
5. **Multi-body extrude splitting** — `compileMultiBodyExtrude` (`pkg/solidcraft/compiler.go`, lines 2266–2501): splits `join`/`cut` extrudes with multiple `ResultBodies` into per-target `solid.extrude` calls.
6. **Post-extrude participant merging & temporary-merge lookahead** — `mergeParticipantBodies` (`pkg/solidcraft/compiler.go`, lines 697–936) & `mergeIsTemporary` (lines 938–1019): explicitly joins `ParticipantBodies` consumed by Fusion, while skipping merges that a later timeline snapshot splits apart again.
7. **Move rotation + translation splitting** — `compileAndExecuteMove` (`pkg/solidcraft/compiler.go`, lines 1752–1807): splits a move with both rotation and translation into two sequential `solid.move` commands.
8. **Per-step geometric body reconciliation & collision-safe renaming** — `reconcileBodies` (`pkg/solidcraft/compiler.go`, lines 208–252), `MatchBodies` & `PlanRenames` (`pkg/cadir/snapshot.go`, lines 22–148), and `renameAutoProjectionSketch` (`pkg/solidcraft/compiler.go`, lines 127–163).
