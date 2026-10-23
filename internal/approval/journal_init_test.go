package approval

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func managedJournalPaths(t *testing.T) (primary, directory string) {
	t.Helper()
	root := privateDir(t)
	primary = filepath.Join(root, "primary")
	if err := os.Mkdir(primary, 0700); err != nil {
		t.Fatal(err)
	}
	return primary, filepath.Join(root, "approval")
}

func TestManagedJournalPreservesReceiptsAndExecutionAcrossRestart(t *testing.T) {
	primary, directory := managedJournalPaths(t)
	journal, err := OpenManagedJournal(directory, primary, true)
	if err != nil {
		t.Fatal(err)
	}
	a, binding := appliedExecutionFixture(t, journal)
	if err = journal.BindExecution(binding); err != nil {
		t.Fatal(err)
	}
	journal.Close()
	journal, err = OpenManagedJournal(directory, primary, true)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	state, err := journal.Load(a.OrgID, a.RequestID)
	if err != nil || state.Receipt == nil || !bytes.Equal(state.Receipt.Bytes, testReceipt(t, a).Bytes) || state.Execution == nil || *state.Execution != binding {
		t.Fatalf("saved decision or execution changed: %v", err)
	}
	binding.OutgoingID = "another-transfer"
	if err = journal.BindExecution(binding); !errors.Is(err, ErrConflict) {
		t.Fatalf("restart permitted replacement transfer: %v", err)
	}
	for _, path := range []string{filepath.Join(primary, journalIdentityName), filepath.Join(directory, journalIdentityName)} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("identity file not private: %v", err)
		}
	}
}

func TestManagedJournalAdoptsExistingExplicitEvidence(t *testing.T) {
	primary, directory := managedJournalPaths(t)
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	journal, err := OpenJournal(directory)
	if err != nil {
		t.Fatal(err)
	}
	a, binding := appliedExecutionFixture(t, journal)
	if err = journal.BindExecution(binding); err != nil {
		t.Fatal(err)
	}
	journal.Close()
	journal, err = OpenManagedJournal(directory, primary, false)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	state, err := journal.Load(a.OrgID, a.RequestID)
	if err != nil || state.Execution == nil || *state.Execution != binding {
		t.Fatalf("upgrade lost existing execution: %v", err)
	}
}

func TestManagedJournalPreservesExplicitDirectoryAlias(t *testing.T) {
	primary, directory := managedJournalPaths(t)
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	alias := directory + "-alias"
	if err := os.Symlink(directory, alias); err != nil {
		t.Fatal(err)
	}
	journal, err := OpenManagedJournal(alias, primary, false)
	if err != nil {
		t.Fatalf("existing explicit directory alias rejected: %v", err)
	}
	journal.Close()
	for _, path := range []string{directory, directory + string(filepath.Separator)} {
		journal, err = OpenManagedJournal(path, primary, false)
		if err != nil {
			t.Fatalf("equivalent path did not retain journal identity: %v", err)
		}
		journal.Close()
	}
}

func TestManagedJournalRejectsLostOrChangedIdentity(t *testing.T) {
	for _, change := range []string{"missing-marker", "missing-identity", "corrupt-marker", "corrupt-identity", "public-marker", "symlink-identity", "changed-path", "another-primary"} {
		t.Run(change, func(t *testing.T) {
			primary, directory := managedJournalPaths(t)
			journal, err := OpenManagedJournal(directory, primary, true)
			if err != nil {
				t.Fatal(err)
			}
			journal.Close()
			marker := filepath.Join(primary, journalIdentityName)
			identity := filepath.Join(directory, journalIdentityName)
			switch change {
			case "missing-marker":
				err = os.Remove(marker)
			case "missing-identity":
				err = os.Remove(identity)
			case "corrupt-marker":
				err = os.WriteFile(marker, []byte(`{}`), 0600)
			case "corrupt-identity":
				err = os.WriteFile(identity, []byte(`{}`), 0600)
			case "public-marker":
				err = os.Chmod(marker, 0644)
			case "symlink-identity":
				if err = os.Remove(identity); err == nil {
					err = os.Symlink(marker, identity)
				}
			case "changed-path":
				directory += "-replacement"
			case "another-primary":
				primary = filepath.Join(filepath.Dir(primary), "another-primary")
				err = os.Mkdir(primary, 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			if journal, err = OpenManagedJournal(directory, primary, true); err == nil {
				journal.Close()
				t.Fatal("missing or substituted history accepted")
			}
			if change == "changed-path" {
				if _, err = os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("path change created a replacement: %v", err)
				}
			}
		})
	}
}

func TestManagedJournalInitializationDurabilityFailures(t *testing.T) {
	for _, target := range []int{1, 2} {
		for _, stage := range []string{"write", "file-sync", "publish", "directory-sync"} {
			t.Run(fmt.Sprintf("artifact-%d/%s", target, stage), func(t *testing.T) {
				primary, directory := managedJournalPaths(t)
				ops := &nthFaultOperations{journalOperations: realJournalOperations{}, target: target, stage: stage}
				if journal, err := openManagedJournal(directory, primary, true, ops); err == nil {
					journal.Close()
					t.Fatal("initialization succeeded despite failed durable write")
				}
				journal, err := OpenManagedJournal(directory, primary, true)
				// No outer marker means no admission could have happened; a fully
				// published pair may reestablish durability. Partial pairs must stop.
				canResume := target == 1 && stage != "directory-sync" || target == 2 && stage == "directory-sync"
				if (err == nil) != canResume {
					t.Fatalf("unexpected restart result: %v", err)
				}
				if journal != nil {
					journal.Close()
				}
			})
		}
	}
}

func TestManagedJournalRejectsUnsafeStorageBeforeEnrollment(t *testing.T) {
	for _, unsafe := range []string{"parent", "primary", "journal", "overlap"} {
		t.Run(unsafe, func(t *testing.T) {
			primary, directory := managedJournalPaths(t)
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			path := directory
			switch unsafe {
			case "parent":
				path = filepath.Dir(primary)
			case "primary":
				path = primary
			case "overlap":
				directory = filepath.Join(primary, "nested")
			}
			if unsafe != "overlap" {
				if err := os.Chmod(path, 0770); err != nil {
					t.Fatal(err)
				}
			}
			if journal, err := OpenManagedJournal(directory, primary, true); err == nil {
				journal.Close()
				t.Fatal("unsafe storage admitted")
			}
			if _, err := os.Stat(filepath.Join(primary, journalIdentityName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unsafe storage was enrolled: %v", err)
			}
		})
	}
}

func TestManagedJournalInitializationProcessCrash(t *testing.T) {
	for _, stage := range []string{"write", "file-sync", "publish", "directory-sync"} {
		t.Run(stage, func(t *testing.T) {
			primary, directory := managedJournalPaths(t)
			cmd := exec.Command(os.Args[0], "-test.run=^TestManagedJournalInitializationCrashChild$")
			cmd.Env = append(os.Environ(), "APPROVAL_INIT_PRIMARY="+primary, "APPROVAL_INIT_DIRECTORY="+directory, "APPROVAL_INIT_CRASH="+stage)
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 79 {
				t.Fatalf("child did not terminate at boundary: %v %s", err, out)
			}
			journal, err := OpenManagedJournal(directory, primary, true)
			if (err == nil) != (stage == "write" || stage == "file-sync") {
				t.Fatalf("partial initialization recovered incorrectly: %v", err)
			}
			if journal != nil {
				journal.Close()
			}
		})
	}
}

func TestManagedJournalInitializationCrashChild(t *testing.T) {
	primary := os.Getenv("APPROVAL_INIT_PRIMARY")
	if primary == "" {
		return
	}
	ops := &crashOperations{journalOperations: realJournalOperations{}, stage: os.Getenv("APPROVAL_INIT_CRASH")}
	_, err := openManagedJournal(os.Getenv("APPROVAL_INIT_DIRECTORY"), primary, true, ops)
	t.Fatalf("crash not reached: %v", err)
}
