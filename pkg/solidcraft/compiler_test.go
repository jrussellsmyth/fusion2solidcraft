package solidcraft

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoDesignSpecificLiterals fails if any non-test Go file under pkg/
// mentions a design-specific name. The translator must be design-agnostic.
func TestNoDesignSpecificLiterals(t *testing.T) {
	banned := []string{
		"Extrude56", "Extrude57", "Extrude59", "Extrude60",
		"Corne36", "Xiao_", "McuCover", "MCULid",
		"usbc", "Socket", "connectors", "bottom_covers", "Sketch1",
	}
	walkErr := filepath.Walk("..", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			for _, b := range banned {
				if strings.Contains(line, b) {
					t.Errorf("%s:%d contains design-specific literal %q", path, i+1, b)
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk failed: %v", walkErr)
	}
}
