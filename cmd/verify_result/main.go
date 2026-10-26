package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/jrussell/fusion2solidcraft/pkg/solidcraft"
)

func main() {
	client, err := solidcraft.Start()
	if err != nil {
		log.Fatalf("Failed to connect to SolidCraft: %v", err)
	}
	defer client.Close()

	// 1. Inspect design
	inspRaw, err := client.CallTool("inspect_design", map[string]interface{}{})
	if err != nil {
		log.Fatalf("inspect_design failed: %v", err)
	}
	var design struct {
		Bodies []struct {
			Name string `json:"name"`
		} `json:"bodies"`
		Sketches []struct {
			Name string `json:"name"`
		} `json:"sketches"`
	}
	_ = json.Unmarshal([]byte(inspRaw), &design)

	fmt.Printf("=== SolidCraft Active Design Inspection ===\n")
	fmt.Printf("Bodies Count: %d\n", len(design.Bodies))
	for i, b := range design.Bodies {
		fmt.Printf("  [%d] %s\n", i+1, b.Name)
	}
	fmt.Printf("Sketches Count: %d\n", len(design.Sketches))

	// 2. Measure
	stats, err := client.Measure()
	if err != nil {
		fmt.Printf("Measure failed: %v\n", err)
	} else {
		fmt.Printf("\n=== B-Rep Measurements ===\n")
		fmt.Printf("Total Volume: %.2f mm^3\n", stats.VolumeMM3)
		fmt.Printf("Surface Area: %.2f mm^2\n", stats.AreaMM2)
		fmt.Printf("Bounding Box: [%.2f, %.2f, %.2f] to [%.2f, %.2f, %.2f]\n",
			stats.BBoxMin.X, stats.BBoxMin.Y, stats.BBoxMin.Z,
			stats.BBoxMax.X, stats.BBoxMax.Y, stats.BBoxMax.Z)
	}

	// 3. Screenshot
	destPNG := "/Users/jrussell/.gemini/antigravity/brain/cd478591-3873-447d-a128-361c93b945a2/final_keyboard_solidcraft.png"
	_, err = client.CallTool("screenshot", map[string]interface{}{
		"view":   "iso",
		"width":  1280,
		"height": 800,
		"path":   destPNG,
	})
	if err != nil {
		fmt.Printf("Screenshot failed: %v\n", err)
	} else {
		fi, statErr := os.Stat(destPNG)
		if statErr == nil {
			fmt.Printf("Screenshot saved to: %s (%d bytes)\n", destPNG, fi.Size())
		} else {
			fmt.Printf("Screenshot tool succeeded, stat error: %v\n", statErr)
		}
	}

	// 4. Save
	savePath := "/Users/jrussell/code/fusion2solidcraft/.worktrees/init-pipeline/final_keyboard.solidcraft"
	if err := client.Save(savePath); err != nil {
		fmt.Printf("Save failed: %v\n", err)
	} else {
		fmt.Printf("Saved design to: %s\n", savePath)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
