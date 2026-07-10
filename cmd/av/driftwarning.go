package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/aviator-co/av/internal/git"
	"github.com/aviator-co/av/internal/meta"
	"github.com/aviator-co/av/internal/utils/colors"
)

// warnLeftoverRestackDrift prints a warning when branches remain that are
// not built on their parent's current tip. A partial run (restack --current,
// reparent, an amend that only restacks part of the tree, or a conflict
// resolved with --abort) can leave a subtree behind on the parent's old
// commits; the branches still show mergeable PRs, so nothing else surfaces
// it. Called after mutation commands complete.
func warnLeftoverRestackDrift(ctx context.Context, repo *git.Repo, tx meta.ReadTx) {
	var stale []string
	for name, br := range tx.AllBranches() {
		if br.MergeCommit != "" || br.Parent.Trunk || br.Parent.Name == "" {
			continue
		}
		if excludedFromSyncAll(tx, name) {
			continue
		}
		if computeBranchDrift(ctx, repo, tx, name, false).NeedsRestack {
			stale = append(stale, fmt.Sprintf("%s (parent %s has moved)", name, br.Parent.Name))
		}
	}
	if len(stale) == 0 {
		return
	}
	sort.Strings(stale)
	fmt.Println(colors.Warning("Some branches are not restacked on their parent's latest commit:"))
	for _, line := range stale {
		fmt.Println("  " + line)
	}
	fmt.Println("Run 'av restack --all' to restack them.")
}

// excludedFromSyncAll reports whether the branch or any of its ancestors is
// marked as excluded from sync --all; those stacks are intentionally left
// alone, so leftover drift there is not worth warning about.
func excludedFromSyncAll(tx meta.ReadTx, name string) bool {
	seen := map[string]bool{}
	for name != "" && !seen[name] {
		seen[name] = true
		br, ok := tx.Branch(name)
		if !ok {
			return false
		}
		if br.ExcludeFromSyncAll {
			return true
		}
		if br.Parent.Trunk {
			return false
		}
		name = br.Parent.Name
	}
	return false
}
