# Fusion to SolidCraft Translator

Reads the active design's timeline from **Fusion 360** over MCP, translates it into a
design-agnostic intermediate representation (CAD-IR), and replays it feature-by-feature in
**SolidCraft** over MCP — producing the same parametric model in SolidCraft and verifying
every intermediate step against Fusion's measured volume, area, and bounding box.

```
Fusion 360 (MCP) ──extract──▶ CAD-IR (JSON) ──compile+replay──▶ SolidCraft (MCP)
                                     │
                                     └── per-step checkpoints: volume / area / bbox
```

## Requirements

- Go 1.27+
- **Fusion 360** running with an MCP add-in exposing an HTTP endpoint
  (default `http://127.0.0.1:27182/mcp`) and the target design open.
- **SolidCraft** available as `solidcraft-cli` on `PATH` (the translator launches
  `solidcraft-cli mcp` as a subprocess; headless if no app window is open).

Build:

```bash
go build ./cmd/fusion2solidcraft
```

## How it works

1. **Extract** — an embedded Fusion Python script walks the timeline, rolls to each
   feature, and records sketches (curves, profiles), extrudes, fillets, combines, moves,
   mirrors, patterns, planes, and a per-step **body snapshot** (name, volume, area,
   centroid, bbox of every body) that captures Fusion's ground truth at that point in
   the history — including body renames, merges, and splits.
2. **Compile** — the timeline is translated into SolidCraft commands. Body identity is
   reconciled after every step: SolidCraft's bodies are geometrically matched to Fusion's
   snapshot and renamed collision-safely to Fusion's names, so references written at each
   timeline point resolve by construction. Extrude targets, participant merges (including
   merges a later split undoes), and mirror targets are all decided from this data — no
   feature names, no design-specific heuristics.
3. **Verify** — after each step with a checkpoint, SolidCraft's total volume/area/bbox is
   compared to Fusion's (2% tolerance) and reported per step.

## Common uses

### Full translation (extract from Fusion, replay in SolidCraft, verify)

```bash
go run ./cmd/fusion2solidcraft --save final_keyboard.solidcraft
```

Extracts the active Fusion design, replays all steps in SolidCraft, prints a per-step
verification report, and saves the result.

### Save / reuse the intermediate model

```bash
# Extract only and export the CAD-IR (no SolidCraft needed)
go run ./cmd/fusion2solidcraft --inspect --json-out model_ir.json

# Later: replay from the saved IR (Fusion not needed)
go run ./cmd/fusion2solidcraft --input model_ir.json --save out.solidcraft
```

`--inspect` also prints a feature-coverage breakdown (how many timeline steps mapped to
supported feature types).

### Stop at / start from a specific step

```bash
# Build only the first 20 steps
go run ./cmd/fusion2solidcraft --input model_ir.json --until-step 20

# Resume an existing SolidCraft session at step 40 (no reset)
go run ./cmd/fusion2solidcraft --input model_ir.json --start-step 40

# Aliases: -from-step / -end-step
```

### Interactive stepping (watch it build)

```bash
# Prompt before every feature: n/Enter = next, c = continue unattended, q = quit
go run ./cmd/fusion2solidcraft --input model_ir.json --step

# Run automatically up to step 50, then switch to interactive stepping
go run ./cmd/fusion2solidcraft --input model_ir.json --step-from 50
```

### Diagnosing divergences

```bash
# Halt at the first step where SolidCraft's geometry deviates from Fusion's
go run ./cmd/fusion2solidcraft --input model_ir.json --find-divergence

# Skip known-problem step(s) entirely (execution + checkpoint); accepts
# comma-separated indices and a-b ranges
go run ./cmd/fusion2solidcraft --input model_ir.json --skip-steps 86
go run ./cmd/fusion2solidcraft --input model_ir.json --skip-steps 40-45,86

# Window around a suspect step
go run ./cmd/fusion2solidcraft --input model_ir.json --start-step 80 --until-step 90
```

Skipped steps are not executed and their checkpoints are not compared; body
reconciliation is disabled for the rest of the run (the design intentionally diverges).
Each skip is logged.

### Disable verification

```bash
go run ./cmd/fusion2solidcraft --input model_ir.json --verify=false
```

## Flags

| Flag | Default | Purpose |
|------|---------|---------|
| `-fusion-url` | `http://127.0.0.1:27182/mcp` | Fusion 360 MCP endpoint |
| `-input` | — | Load CAD-IR from a JSON file instead of extracting from Fusion |
| `-inspect` | `false` | Extract/lint only; don't touch SolidCraft |
| `-verify` | `true` | Per-step volume/area/bbox checkpoints |
| `-start-step` / `-from-step` | `0` | First step to execute; `>0` resumes the existing SolidCraft session |
| `-until-step` / `-end-step` | `-1` | Last step to execute (inclusive) |
| `-step` | `false` | Prompt before every feature (`n`/`c`/`q`) |
| `-step-from` | `-1` | Run automatically to this index, then prompt |
| `-skip-steps` | — | Comma-separated indices/ranges (`51,52`, `40-45`) to skip |
| `-find-divergence` | `false` | Halt at the first checkpoint divergence |
| `-save` | — | Save the finished design to a `.solidcraft` file |
| `-json-out` | — | Export the CAD-IR model to JSON |

## Repository layout

- `cmd/fusion2solidcraft` — CLI entry point (extract → compile → replay → verify).
- `cmd/verify_result` — small debug utility for inspecting saved results.
- `pkg/fusion` — Fusion MCP client + timeline extractor (embedded Python script).
- `pkg/cadir` — the CAD-IR model schema and geometry helpers (body matching, rename
  planning, arc splitting, plane bases, checkpoint comparison).
- `pkg/solidcraft` — SolidCraft MCP client, command parameter types, and the compiler
  that replays a CAD-IR timeline step by step with per-step body reconciliation.

## Notes

- Units are millimeters throughout the IR (Fusion's centimeters are converted).
- The translator is design-agnostic: nothing in `pkg/` references feature or body names
  from any particular model (enforced by a test).
- SolidCraft boolean operations occasionally cannot replicate a Fusion feature (e.g.
  region detection on spline-heavy sketches); such steps surface as checkpoint
  divergences and can be bypassed with `--skip-steps` while investigating.
