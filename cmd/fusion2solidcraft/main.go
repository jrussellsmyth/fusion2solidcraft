package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/jrussell/fusion2solidcraft/pkg/cadir"
	"github.com/jrussell/fusion2solidcraft/pkg/fusion"
	"github.com/jrussell/fusion2solidcraft/pkg/solidcraft"
)

func main() {
	fusionURL := flag.String("fusion-url", "http://127.0.0.1:27182/mcp", "Fusion 360 MCP URL")
	inputPath := flag.String("input", "", "Load CAD-IR model from existing JSON file instead of extracting from Fusion")
	inspectOnly := flag.Bool("inspect", false, "Inspect & lint model timeline without streaming to SolidCraft")
	verify := flag.Bool("verify", true, "Verify intermediate B-Rep stats (volume, area, bbox)")
	untilStep := flag.Int("until-step", -1, "Pause execution after reaching this step index (inclusive)")
	findDivergence := flag.Bool("find-divergence", false, "Halt immediately at the first step where SolidCraft diverges from Fusion")
	savePath := flag.String("save", "", "Path to save .solidcraft file")
	jsonOut := flag.String("json-out", "", "Export CAD-IR JSON to this file")
	flag.Parse()

	fmt.Println("==================================================")
	fmt.Println("  Fusion 360 -> SolidCraft Parametric Translator  ")
	fmt.Println("==================================================")

	var model *cadir.Model

	if *inputPath != "" {
		fmt.Printf("Loading CAD-IR model from %s...\n", *inputPath)
		data, err := os.ReadFile(*inputPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading input JSON: %v\n", err)
			os.Exit(1)
		}
		var loadedModel cadir.Model
		if err := json.Unmarshal(data, &loadedModel); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing input JSON: %v\n", err)
			os.Exit(1)
		}
		model = &loadedModel
	} else {
		// 1. Connect to Fusion 360
		fmt.Printf("Connecting to Fusion 360 at %s...\n", *fusionURL)
		fusionClient := fusion.NewClient(*fusionURL)
		extractor := fusion.NewExtractor(fusionClient)

		// 2. Extract CAD-IR Model
		fmt.Println("Extracting active model timeline from Fusion 360...")
		extracted, err := extractor.ExtractModel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error extracting Fusion model: %v\n", err)
			os.Exit(1)
		}
		model = extracted
	}

	// 3. Analyze Feature Coverage
	typeCount := make(map[string]int)
	unknownCount := 0
	for _, step := range model.Timeline {
		typeCount[step.Type]++
		if step.Type == "unknown" {
			unknownCount++
		}
	}

	totalSteps := len(model.Timeline)
	knownSteps := totalSteps - unknownCount
	coveragePct := 0.0
	if totalSteps > 0 {
		coveragePct = float64(knownSteps) / float64(totalSteps) * 100.0
	}

	fmt.Printf("\nModel: %s (%d total unrolled timeline steps)\n", model.Name, totalSteps)
	fmt.Println("--- Feature Coverage Breakdown ---")
	for fType, count := range typeCount {
		fmt.Printf("  %-15s: %2d (%.1f%%)\n", fType, count, float64(count)/float64(totalSteps)*100.0)
	}
	fmt.Printf("\nOverall AST Coverage: %.1f%% (%d of %d steps mapped)\n", coveragePct, knownSteps, totalSteps)

	// 4. Save JSON if requested
	if *jsonOut != "" {
		b, err := json.MarshalIndent(model, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to marshal JSON: %v\n", err)
		} else {
			if err := os.WriteFile(*jsonOut, b, 0644); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to write file %s: %v\n", *jsonOut, err)
			} else {
				fmt.Printf("Saved full CAD-IR JSON to: %s\n", *jsonOut)
			}
		}
	}

	// If in inspect mode, exit now
	if *inspectOnly {
		fmt.Println("\nInspection complete (Inspect mode: skipped SolidCraft execution).")
		return
	}

	// 5. Connect to SolidCraft
	fmt.Println("\nConnecting to SolidCraft engine...")
	scClient, err := solidcraft.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to SolidCraft: %v\n", err)
		os.Exit(1)
	}
	defer scClient.Close()

	// 6. Compile and Execute Timeline
	fmt.Println("Compiling and replaying timeline into SolidCraft...")
	compiler := solidcraft.NewCompiler(scClient)
	doVerify := *verify || *findDivergence
	results, err := compiler.CompileModel(model, doVerify, *untilStep, *findDivergence)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error during compilation: %v\n", err)
		os.Exit(1)
	}

	// 7. Digital Twin Verification Report
	if *verify && len(results) > 0 {
		fmt.Println("\n--- Digital Twin Verification Report ---")
		fmt.Printf("%-5s | %-20s | %-8s | %-15s | %-15s\n", "Step", "Name", "Status", "Volume Diff %", "BBox Delta (mm)")
		fmt.Println("--------------------------------------------------------------------------------")
		allPassed := true
		for _, r := range results {
			status := "PASS"
			if !r.Passed {
				status = "WARN"
				allPassed = false
			}
			fmt.Printf("[%02d]  | %-20s | %-8s | %13.4f%% | %15.4f\n",
				r.StepIndex, r.StepName, status, r.VolumeDiffPct, r.BBoxMaxDelta)
		}
		if allPassed {
			fmt.Println("\nAll timeline checkpoints passed verification!")
		} else {
			fmt.Println("\nSome checkpoints deviated; check feature geometry details.")
		}
	}

	// 8. Save model if requested
	if *savePath != "" {
		fmt.Printf("\nSaving SolidCraft design to %s...\n", *savePath)
		if err := scClient.Save(*savePath); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving design: %v\n", err)
		} else {
			fmt.Println("Design saved successfully!")
		}
	}

	fmt.Println("\nTranslation complete! The model is active in your SolidCraft session.")
}
