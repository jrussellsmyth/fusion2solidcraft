# SolidCraft Bug Report: `solid.combine` fails on 100% coincident / overlapping B-reps (`another entity is already related to the other one`)

**SolidCraft version:** `0.42.0` (`solidcraft-cli 0.42.0`)  
**Reproducer file:** `docs/repro-coincident-combine.sc`  
**Automated live test:** `TestLiveCoincidentCombineRepro` in `pkg/solidcraft/coincident_combine_repro_live_test.go`

---

## Summary

Calling `solid.combine` (`operation: "join"`) on two bodies that occupy the exact same geometry (or where one body is a 100% coincident duplicate of a sub-solid contained inside the other body) fails in SMLib with `another entity is already related to the other one` instead of unioning cleanly.

## Steps to Reproduce

### Option A — From the attached `.sc` file (`docs/repro-coincident-combine.sc`)
1. Open `docs/repro-coincident-combine.sc` in SolidCraft (it contains two identical stepped/pocketed socket bodies `Xiao_RP2040_Fifi_Left_McuCover` and `Xiao_RP2040_Fifi_Left_McuCover (2)` created by `solid.move` with `translate: [0, 0, 0]`, `copy: true`).
2. Run **Combine** (`solid.combine`):
   - `target`: `"Xiao_RP2040_Fifi_Left_McuCover"`
   - `tools`: `["Xiao_RP2040_Fifi_Left_McuCover (2)"]`
   - `operation`: `"join"`
   - `keep_tools`: `false`

### Option B — Via Go test
```bash
go test -v ./pkg/solidcraft -run TestLiveCoincidentCombineRepro
```

## Actual Result

`solid.combine` fails with:
```text
tool error: SMLib boolean: operation: solidcore: SmApiBoolean: boolean::topology_intersector::p1::relate: relating the operands' entities failed: the edge from (5.1720943808638955, -6.200000000000021, 0.9999999999999999) to (5.1720943808638955, -6.200000000000021, 1.5000000000000002) of the B-rep is related to the edge from (5.1720943808638955, -6.200000000000021, 0.9999999999999999) to (5.1720943808638955, -6.200000000000021, 1.5000000000000002) and was asked for the edge from (5.1720943808638955, -6.200000000000021, 0.9999999999999999) to (5.1720943808638955, -6.200000000000021, 1.5000000000000002): another entity is already related to the other one
```

## Expected Result

`solid.combine` (`operation: "join"`) should succeed and absorb the coincident/overlapping solid (matching Fusion 360's Combine Join behavior when two bodies share an identical overlapping region).
