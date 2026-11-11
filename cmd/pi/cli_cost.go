package main

import (
	"fmt"
	"os"

	"github.com/notshekhar/pi/internal/core/db"
)

// auditListLimit caps how many failing rows/sessions are printed; the count
// above the list always says how many there are in total.
const auditListLimit = 20

// costCommand is `pi cost audit`; anything else prints its usage.
func costCommand(c *cliCall) int {
	if len(c.args.positional) == 0 || c.args.positional[0] != "audit" {
		fmt.Printf("Usage: %s cost audit — reconcile the cost ledger\n", productName)
		return 0
	}
	h, err := db.Open()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	audit, err := auditLedger(h)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	printLedgerAudit(audit)
	if len(audit.priceViolations) > 0 || len(audit.sessionMismatches) > 0 {
		return 1
	}
	return 0
}

func printLedgerAudit(a ledgerAudit) {
	fmt.Printf("Ledger rows: %d\n", a.rows)
	if len(a.priceViolations) == 0 {
		fmt.Println("Every priced row recomputes to its recorded usd (qty × snapshot price).")
	} else {
		fmt.Printf("%d row(s) fail the price reconciliation:\n", len(a.priceViolations))
		for _, v := range a.priceViolations[:min(auditListLimit, len(a.priceViolations))] {
			fmt.Printf("  row %d (%s): recorded $%.6f, recomputed $%.6f\n", v.id, v.model, v.usd, v.computed)
		}
	}
	if len(a.sessionMismatches) == 0 {
		fmt.Println("Ledger token sums match the transcripts for every session.")
		return
	}
	fmt.Printf("%d session(s) disagree with their transcripts:\n", len(a.sessionMismatches))
	for _, m := range a.sessionMismatches[:min(auditListLimit, len(a.sessionMismatches))] {
		fmt.Printf("  %s: ledger in/out %d/%d, entries %d/%d\n", m.sessionPub, m.ledgerInput, m.ledgerOutput, m.entriesInput, m.entriesOutput)
	}
}
