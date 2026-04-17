# ghenga

A command-line tool for managing **stacked pull requests** with git.

A **tower** is a sequence of dependent branches where each one builds on the
previous. No more juggling rebases by hand when a branch below changes.
Ghenga lets you rebase the whole stack in one shot, push every branch to the
remote in order, and land branches one at a time from the bottom up.

## Install

```
go install github.com/cpoile/ghenga@latest
```

## Commands

- `ghenga init` — register the current repository in ghenga's config
- `ghenga new <tower>` — create a new tower and make it the current one
- `ghenga ls` — list towers and their branches (this is the default command)
- `ghenga current <tower>` — switch to a different tower
- `ghenga rename <new-name>` — rename the current tower
- `ghenga base <branch>` — set the current tower's base branch (e.g. `main`)
- `ghenga add <branch>` — add an existing branch to the current tower
- `ghenga branch-rm <branch>` — remove a branch from the current tower (the git branch itself is untouched)
- `ghenga tower-rm <tower>` — remove a tower
- `ghenga rebase` — rebase branches that have diverged from the branch below them
  - `ghenga rebase onto <ref>` — rebase the whole tower onto a new base
  - `ghenga rebase from <branch> <commit>` — partial rebase starting at `<commit>` on `<branch>`
  - `ghenga rebase continue` — resume after resolving conflicts
  - `ghenga rebase cancel` — abort an in-progress rebase
  - `ghenga rebase undo` — revert the last completed rebase
- `ghenga sync` — push every branch in the tower to the remote (force-with-lease where needed)
  - `ghenga sync undo` — undo the last sync
- `ghenga land` — land the bottom branch: pull its merge into the base, drop it from the tower, and rebase the rest
- `ghenga config` — print the path to the ghenga config file
- `ghenga completion` — emit shell completions

## How a tower relates to `main`

In plain git, a stack of pull requests is a chain of branches, each pointing
at the tip of the one below:

```
      o---o---o  main
               \
                o---o  auth-models
                     \
                      o---o  auth-handlers
                           \
                            o---o  auth-ui
```

The PR for `auth-models` targets `main`; `auth-handlers` targets
`auth-models`; `auth-ui` targets `auth-handlers`. That's the "stack."

ghenga records this as a tower:

```
  3. auth-ui          PR → auth-handlers
  2. auth-handlers    PR → auth-models
  1. auth-models      PR → main
Tower: auth   (base: main)
```

## A typical flow

Say you're building an auth feature in three reviewable pieces. Each branch
ends up with two commits.

**1. Initialize and create the tower.**

```
ghenga init
ghenga new auth
```

**2. Build the first branch and add it.**

```
git checkout -b auth-models
# ... work, commit, work, commit ...
ghenga add auth-models          # first add prompts you to pick a base (main)
```

**3. Stack the next branch on top.**

```
git checkout -b auth-handlers   # branches off auth-models' current tip
# ... two commits ...
ghenga add auth-handlers
```

**4. And the third.**

```
git checkout -b auth-ui
# ... two commits ...
ghenga add auth-ui
```

After those three `add`s, the repo looks like:

```
      o---o---o  main
               \
                o---o  auth-models
                     \
                      o---o  auth-handlers
                           \
                            o---o  auth-ui
```

**5. Push the stack and open PRs.**

```
ghenga sync
```

`sync` pushes every branch to the remote in order, force-pushing (with lease)
any that have been rewritten.

## Fixing a review comment on the bottom PR

A reviewer leaves feedback on the `auth-models` PR. You check it out and
commit a fix:

```
git checkout auth-models
# ... edit, test ...
git commit -am "Address review feedback"
```

`auth-models` now has a new commit, but `auth-handlers` and `auth-ui` still
point at the **old** tip. The stack is out of sync:

```
      o---o---o  main
               \
                o---o---*  auth-models         (* = the new review-fix commit)
                     \
                      o---o  auth-handlers     (still branched from the OLD tip)
                           \
                            o---o  auth-ui
```

Rewire the stack:

```
ghenga rebase
```

ghenga sees that `auth-handlers` has diverged from `auth-models`,
cherry-picks its commits onto the new `auth-models` tip, then does the same
for `auth-ui` onto the rebased `auth-handlers`:

```
      o---o---o  main
               \
                o---o---*  auth-models
                         \
                          o'--o'  auth-handlers    (rewritten)
                               \
                                o'--o'  auth-ui    (rewritten)
```

If a cherry-pick hits a conflict, ghenga pauses. Resolve the conflict,
`git add` the files, then run `ghenga rebase continue` to pick up where it
left off. (`ghenga rebase cancel` aborts; `ghenga rebase undo` reverts a
completed rebase.)

Now push the rewritten branches:

```
ghenga sync
```

## Landing the bottom PR

Once the bottom PR is approved and merged on the remote (and the remote
branch deleted — typically automatic after a squash-merge), run:

```
ghenga land
```

ghenga will:

1. Fetch the remote and fast-forward the local `main`.
2. Drop `auth-models` from the tower's branch list (the local git branch is left alone).
3. Rebase `auth-handlers` onto the new `main`, excluding the commits that already landed via the squash-merge.
4. Rebase `auth-ui` onto the rebased `auth-handlers`.

```
Before land:

      o---o---o---M  main              (M = the squash-merge, fetched from remote)
               \
                o---o---*  auth-models  (merged; removed from the tower)
                         \
                          o---o  auth-handlers
                               \
                                o---o  auth-ui

After land:

      o---o---o---M  main
                   \
                    o'--o'  auth-handlers    (rebased onto new main)
                         \
                          o'--o'  auth-ui
```

The tower is now a two-branch stack with `auth-handlers` as the new bottom
PR. Push the rewritten branches with `ghenga sync`, and repeat when that PR
lands.
