package portfolio

import (
	"os"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/testguard"
)

func TestMain(m *testing.M) { os.Exit(testguard.Run(m)) }
