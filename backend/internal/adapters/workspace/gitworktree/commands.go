package gitworktree

func checkRefFormatBranchArgs(repo, branch string) []string {
	return []string{"-C", repo, "check-ref-format", "--branch", branch}
}

func revParseVerifyArgs(repo, ref string) []string {
	return []string{"-C", repo, "rev-parse", "--verify", "--quiet", ref}
}

func worktreeAddBranchArgs(repo, path, branch string) []string {
	return []string{"-C", repo, "worktree", "add", path, branch}
}

func worktreeAddNewBranchArgs(repo, branch, path, baseRef string) []string {
	return []string{"-C", repo, "worktree", "add", "-b", branch, path, baseRef}
}

func worktreeRemoveForceArgs(repo, path string) []string {
	return []string{"-C", repo, "worktree", "remove", "--force", path}
}

func worktreePruneArgs(repo string) []string {
	return []string{"-C", repo, "worktree", "prune"}
}

func worktreeListPorcelainArgs(repo string) []string {
	return []string{"-C", repo, "worktree", "list", "--porcelain"}
}

func baseRefCandidates(branch, defaultBranch string) []string {
	return []string{"origin/" + branch, "origin/" + defaultBranch, branch}
}

func chooseWorktreeAddArgs(repo, path, branch, baseRef string, localBranchExists bool) []string {
	if localBranchExists {
		return worktreeAddBranchArgs(repo, path, branch)
	}
	return worktreeAddNewBranchArgs(repo, branch, path, baseRef)
}
