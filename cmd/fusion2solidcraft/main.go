package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/jrussell/fusion2solidcraft/pkg/fusion"
	"github.com/jrussell/fusion2solidcraft/pkg/solidcraft"
)

func main() {
	fusionURL := flag.String("fusion-url", "http://127.0.0.1:27182/mcp", "Fusion 360 MCP URL")
	verify := flag.Bool("verify", true, "Verify intermediate B-Rep stats (volume, area, bbox)")
	savePath := flag.String("save", "", "Path to save .solidcraft file")
	jsonOut := flag.String("json-out", "", "Export CAD-IR JSON to this file")
	flag.Parse()

	fmt.Println("==================================================")
	fmt.Println("  Fusion 360 -> SolidCraft Parametric Translator  ")
	fmt.Println("==================================================")

	// 1. Connect to Fusion 360
	fmt.Printf("Connecting to Fusion 360 at %s...\n", *fusionURL)
	fusionClient := fusion.NewClient(*fusionURL)
	extractor := fusion.NewExtractor(fusionClient)

	// 2. Extract CAD-IR Model
	fmt.Println("Extracting active model timeline from Fusion 360...")
	model, err := extractor.ExtractModel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error extracting Fusion model: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Model: %s (%d timeline steps)\n", model.Name, len(model.Timeline))
	for _, step := range model.Timeline {
		fmt.Printf("  [%02d] %-20s (type: %s)\n", step.Index, step.Name, step.Type)
	}

	if *jsonOut != "" {
		b, _ := json.MarshalIndent(model, "", "  ")
		_ = os.WriteFile(*jsonOut, b, 0644)
		fmt.Printf("Saved CAD-IR JSON to %s\n", *jsonOut)
	}

	// 3. Connect to SolidCraft
	fmt.Println("\nConnecting to SolidCraft engine...")
	scClient, err := solidcraft.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to SolidCraft: %v\n", err)
		os.Exit(1)
	}
	defer scClient.Close()

	// 4. Compile and Execute Timeline
	fmt.Println("Compiling and replaying timeline into SolidCraft...")
	compiler := solidcraft.NewCompiler(scClient)
	results, err := compiler.CompileModel(model, *verify)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error during compilation: %v\n", err)
		os.Exit(1)
	}

	// 5. Digital Twin Verification Report
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

	// 6. Save model if requested
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
