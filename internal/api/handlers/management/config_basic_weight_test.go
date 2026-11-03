package management

import "testing"

func TestNormalizeRoutingStrategyWeightedRoundRobin(t *testing.T) {
	for _, input := range []string{"weighted-round-robin", "weightedroundrobin", "wrr"} {
		got, ok := normalizeRoutingStrategy(input)
		if !ok || got != "weighted-round-robin" {
			t.Fatalf("normalizeRoutingStrategy(%q) = %q, %v; want weighted-round-robin, true", input, got, ok)
		}
	}
}

func TestNormalizeRoutingStrategyResetSoonest(t *testing.T) {
	for _, input := range []string{"reset-soonest", "resetsoonest", "rs", " Reset-Soonest "} {
		got, ok := normalizeRoutingStrategy(input)
		if !ok || got != "reset-soonest" {
			t.Fatalf("normalizeRoutingStrategy(%q) = %q, %v; want reset-soonest, true", input, got, ok)
		}
	}
}
