package e2e

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	switch os.Getenv("GG_E2E_HELPER") {
	case "streams":
		fmt.Fprint(os.Stdout, "out")
		fmt.Fprint(os.Stderr, "err")
		os.Exit(7)
	case "cancel":
		time.Sleep(10 * time.Second)
		os.Exit(0)
	default:
		os.Exit(m.Run())
	}
}
