package main

import (
	"os"
	"testing"

	"github.com/kyungseo/acrelay/internal/testenv"
)

func TestMain(m *testing.M) {
	os.Exit(testenv.RunIsolatedMain(m))
}
