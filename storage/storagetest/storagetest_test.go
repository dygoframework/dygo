package storagetest_test

import (
	"testing"

	"github.com/dygoframework/dygo/storage/storagetest"
)

func TestRunOnLocal(t *testing.T) {
	storagetest.Run(t, storagetest.Local(t))
}
