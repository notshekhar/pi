package main

import (
	"fmt"
	"strconv"

	"github.com/notshekhar/pi/internal/core/catalog"
	"github.com/notshekhar/pi/internal/core/config"
)

// modelsCommand is loop's cmdModels: every catalog model, one per line, as
// "<id>\tctx:<window>\t$<in>/$<out>" — tab-separated so it pipes into cut.
func modelsCommand(*cliCall) int {
	config.ConfigureCatalog()
	for _, m := range catalog.All() {
		fmt.Printf("%s\tctx:%d\t$%s/$%s\n", m.ID, m.Context, jsNumber(m.Cost.Input), jsNumber(m.Cost.Output))
	}
	return 0
}

// jsNumber formats a float the way JavaScript prints a number: no trailing
// zeros, no exponent for ordinary prices (3, 0.15, 1.25).
func jsNumber(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
