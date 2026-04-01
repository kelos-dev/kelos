# CONTRIBUTING

In order to make this process as smooth as possible the following should be done in order to effectively contribute:

1. Branch off of main for all feature branches
2. Write code
3. Push to feature branch (this will be used for upstream contributions)
4. Check out prod branch
5. Checkout new branch called <feature-branch>-prod
6. Cherry-pick commit(s) from feature branch onto branch (squash to a single commit first, or use `git cherry-pick <first-commit>..<last-commit>` for multiple commits)
7. Push to origin
8. Open PR against prod branch in datagravity-ai/kelos repo
9. Merge

If changes are requested to the upstream branch simply continue to cherry-pick and push new PRs to prod branch

The key benefit here is minimizing the git merge conflicts that are needed while not removing the changes that we have applied to prod (as we are ahead of the upstream)
