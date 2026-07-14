package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/aviator-co/av/internal/git"
	"github.com/aviator-co/av/internal/meta"
	"github.com/aviator-co/av/internal/utils/colors"
	"github.com/aviator-co/av/internal/utils/stackutils"
	"github.com/spf13/cobra"
)

var flagTreeCurrent bool
var flagTreeJSON bool

var treeCmd = &cobra.Command{
	Use:   "tree",
	Short: "Show the tree of stacked branches",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		repo, err := getRepo(ctx)
		if err != nil {
			return err
		}

		db, err := getDB(ctx, repo)
		if err != nil {
			return err
		}

		status, err := repo.Status(ctx)
		if err != nil {
			return err
		}

		worktrees, _ := repo.WorktreeList(ctx)
		worktreesByBranch := make(map[string]string)
		for _, wt := range worktrees {
			if wt.Branch != "" {
				worktreesByBranch[wt.Branch] = filepath.Base(wt.Path)
			}
		}

		var ss []string
		currentBranch := status.CurrentBranch
		tx := db.ReadTx()
		var rootNodes []*stackutils.StackTreeNode
		if flagTreeCurrent {
			node, err := stackutils.BuildStackTreeCurrentStack(tx, currentBranch, true)
			if err != nil {
				return err
			}
			rootNodes = []*stackutils.StackTreeNode{node}
		} else {
			rootNodes = stackutils.BuildStackTreeAllBranches(tx, currentBranch, true)
		}
		if flagTreeJSON {
			nodes := make([]*treeJSONNode, 0, len(rootNodes))
			for _, node := range rootNodes {
				nodes = append(nodes, treeJSONFromNode(ctx, repo, tx, node, currentBranch))
			}
			out, err := json.MarshalIndent(nodes, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(out))
			return nil
		}
		for _, node := range rootNodes {
			ss = append(
				ss,
				stackutils.RenderTree(node, func(branchName string, isTrunk bool) string {
					return renderStackTreeBranchInfo(
						tx,
						stackTreeStackBranchInfoStyles,
						currentBranch,
						branchName,
						isTrunk,
						worktreesByBranch,
						computeBranchDrift(ctx, repo, tx, branchName, isTrunk).stats(),
					)
				}),
			)
		}
		var ret string
		if len(ss) != 0 {
			ret = lipgloss.NewStyle().MarginTop(1).MarginBottom(1).Render(
				lipgloss.JoinVertical(0, ss...),
			) + "\n"
		}
		_, _ = lipgloss.Print(ret)
		return nil
	},
}

func init() {
	treeCmd.Flags().BoolVar(&flagTreeCurrent, "current", false, "show only the current stack")
	treeCmd.Flags().BoolVar(&flagTreeJSON, "json", false, "output the tree as JSON")
}

type stackBranchInfoStyles struct {
	BranchName      lipgloss.Style
	HEAD            lipgloss.Style
	PullRequestLink lipgloss.Style
}

var stackTreeStackBranchInfoStyles = stackBranchInfoStyles{
	BranchName:      lipgloss.NewStyle().Bold(true).Foreground(colors.Green600),
	HEAD:            lipgloss.NewStyle().Bold(true).Foreground(colors.Cyan600),
	PullRequestLink: lipgloss.NewStyle(),
}

// branchDrift holds tree-health markers for a branch: a non-trunk parent
// whose tip the branch is no longer built on (silent fork; PRs may still
// show mergeable), and branches that carry no commits of their own.
type branchDrift struct {
	NeedsRestack bool
	NoCommits    bool
}

func computeBranchDrift(
	ctx context.Context,
	repo *git.Repo,
	tx meta.ReadTx,
	branchName string,
	isTrunk bool,
) branchDrift {
	var drift branchDrift
	if isTrunk {
		return drift
	}
	bi, ok := tx.Branch(branchName)
	if !ok || bi.Parent.Name == "" {
		return drift
	}
	parentTip, err := repo.RevParse(ctx, &git.RevParse{Rev: bi.Parent.Name})
	if err != nil {
		return drift
	}
	if !bi.Parent.Trunk {
		mergeBase, err := repo.MergeBase(ctx, parentTip, branchName)
		if err == nil && mergeBase != parentTip {
			drift.NeedsRestack = true
		}
	}
	if tip, err := repo.RevParse(ctx, &git.RevParse{Rev: branchName}); err == nil {
		base := bi.Parent.BranchingPointCommitHash
		if base == "" {
			base = parentTip
		}
		if tip == base || tip == parentTip {
			drift.NoCommits = true
		}
	}
	return drift
}

func (d branchDrift) stats() []string {
	var stats []string
	if d.NeedsRestack {
		stats = append(stats, lipgloss.NewStyle().Bold(true).Foreground(colors.Red600).
			Render("needs restack: parent moved"))
	}
	if d.NoCommits {
		stats = append(stats, colors.Faint("no commits"))
	}
	return stats
}

type treeJSONNode struct {
	Name   string `json:"name"`
	Trunk  bool   `json:"trunk,omitempty"`
	Head   string `json:"head,omitempty"`
	Parent string `json:"parent,omitempty"`
	// The recorded branching point: the parent commit this branch's own
	// commits start from. Empty when branching from trunk (av derives it
	// via merge-base in that case).
	BranchingPoint string          `json:"branchingPoint,omitempty"`
	PullRequest    string          `json:"pullRequest,omitempty"`
	Current        bool            `json:"current,omitempty"`
	NeedsRestack   bool            `json:"needsRestack,omitempty"`
	NoCommits      bool            `json:"noCommits,omitempty"`
	Children       []*treeJSONNode `json:"children,omitempty"`
}

func treeJSONFromNode(
	ctx context.Context,
	repo *git.Repo,
	tx meta.ReadTx,
	node *stackutils.StackTreeNode,
	currentBranch string,
) *treeJSONNode {
	branchName := node.Branch.BranchName
	bi, ok := tx.Branch(branchName)
	isTrunk := node.Branch.ParentBranchName == ""
	out := &treeJSONNode{
		Name:    branchName,
		Trunk:   isTrunk,
		Parent:  node.Branch.ParentBranchName,
		Current: branchName == currentBranch,
	}
	if head, err := repo.RevParse(ctx, &git.RevParse{Rev: branchName}); err == nil {
		out.Head = head
	}
	if ok {
		out.BranchingPoint = bi.Parent.BranchingPointCommitHash
	}
	if ok && bi.PullRequest != nil && bi.PullRequest.Permalink != "" {
		out.PullRequest = bi.PullRequest.Permalink
	}
	drift := computeBranchDrift(ctx, repo, tx, branchName, isTrunk)
	out.NeedsRestack = drift.NeedsRestack
	out.NoCommits = drift.NoCommits
	for _, child := range node.Children {
		out.Children = append(out.Children, treeJSONFromNode(ctx, repo, tx, child, currentBranch))
	}
	return out
}

func renderStackTreeBranchInfo(
	tx meta.ReadTx,
	styles stackBranchInfoStyles,
	currentBranchName string,
	branchName string,
	isTrunk bool,
	worktrees map[string]string,
	driftStats []string,
) string {
	bi, _ := tx.Branch(branchName)

	sb := strings.Builder{}
	sb.WriteString(styles.BranchName.Render(branchName))
	var stats []string
	if branchName == currentBranchName {
		stats = append(stats, styles.HEAD.Render("HEAD"))
	} else if wtName, ok := worktrees[branchName]; ok {
		stats = append(stats, colors.Faint("worktree: "+wtName))
	}
	stats = append(stats, driftStats...)
	if bi.ExcludeFromSyncAll {
		descendants := meta.SubsequentBranches(tx, branchName)
		if len(descendants) > 0 {
			stats = append(stats, colors.Faint("branch and children excluded from sync --all"))
		} else {
			stats = append(stats, colors.Faint("excluded from sync --all"))
		}
	}
	if len(stats) > 0 {
		sb.WriteString(" (")
		sb.WriteString(strings.Join(stats, ", "))
		sb.WriteString(")")
	}

	if !isTrunk {
		sb.WriteString("\n")
		if bi.PullRequest != nil && bi.PullRequest.Permalink != "" {
			sb.WriteString(styles.PullRequestLink.Render(bi.PullRequest.Permalink))
		} else {
			sb.WriteString(styles.PullRequestLink.Render("No pull request"))
		}
	}
	return sb.String()
}

type stackTreeBranchInfo struct {
	BranchName      string
	Deleted         bool
	NeedSync        bool
	PullRequestLink string
}

func getStackTreeBranchInfo(
	ctx context.Context,
	repo *git.Repo,
	tx meta.ReadTx,
	branchName string,
) *stackTreeBranchInfo {
	bi, _ := tx.Branch(branchName)
	branchInfo := stackTreeBranchInfo{
		BranchName: branchName,
	}
	if bi.PullRequest != nil && bi.PullRequest.Permalink != "" {
		branchInfo.PullRequestLink = bi.PullRequest.Permalink
	}
	if _, err := repo.RevParse(ctx, &git.RevParse{Rev: branchName}); err != nil {
		branchInfo.Deleted = true
	}

	parentHead, err := repo.RevParse(ctx, &git.RevParse{Rev: bi.Parent.Name})
	if err != nil {
		// The parent branch doesn't exist.
		branchInfo.NeedSync = true
	} else {
		mergeBase, err := repo.MergeBase(ctx, parentHead, branchName)
		if err != nil {
			// The merge base doesn't exist. This is odd. Mark the branch as needing
			// sync to see if we can fix this.
			branchInfo.NeedSync = true
		}
		if mergeBase != parentHead {
			// This branch is not on top of the parent branch. Need sync.
			branchInfo.NeedSync = true
		}
	}

	upstreamExists, err := repo.DoesRemoteBranchExist(ctx, branchName)
	if err != nil || !upstreamExists {
		// Not pushed.
		branchInfo.NeedSync = true
	}
	upstreamBranch := fmt.Sprintf("remotes/origin/%s", branchName)
	upstreamDiff, err := repo.Diff(ctx, &git.DiffOpts{
		Quiet:      true,
		Specifiers: []string{branchName, upstreamBranch},
	})
	if err != nil || !upstreamDiff.Empty {
		branchInfo.NeedSync = true
	}
	return &branchInfo
}
