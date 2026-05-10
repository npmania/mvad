package lock

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAcquireReentrant(t *testing.T) {
	Path = filepath.Join(t.TempDir(), "lock")
	r1, err := AcquireRoot()
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	r2, err := AcquireRoot()
	if err != nil {
		t.Fatalf("nested acquire: %v", err)
	}
	r2()
	r1()
}

func TestAcquireCrossProcess(t *testing.T) {
	if os.Getenv("MVAD_LOCK_CHILD") == "1" {
		_, err := AcquireRoot()
		if err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	Path = filepath.Join(t.TempDir(), "lock")
	rel, err := AcquireRoot()
	if err != nil {
		t.Fatalf("parent acquire: %v", err)
	}
	defer rel()
	cmd := exec.Command(os.Args[0], "-test.run=TestAcquireCrossProcess")
	cmd.Env = append(os.Environ(), "MVAD_LOCK_CHILD=1", "MVAD_LOCK_PATH="+Path)
	err = cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("child: want exit 2 (ErrLocked), got %v", err)
	}
}

func TestMain(m *testing.M) {
	if p := os.Getenv("MVAD_LOCK_PATH"); p != "" {
		Path = p
	}
	os.Exit(m.Run())
}
