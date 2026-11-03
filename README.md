# git-cleaner

A Go TUI for reviewing and deleting stale Git branches in the current repository.

## Features

- Configurable protected branches, defaulting to `main`, `master`, `develop`, `dev`, `development`, `staging`, and `release/*`.
- Shows local and remote branches merged into protected branches.
- Shows old branches by last commit age, default thresholds `30`, `60`, and `90` days.
- Shows abandoned branches:
  - local branches whose upstream is gone
  - remote-only branches with no matching local branch
- Shows unmerged/risky branches for review.
- Select individual branches, select all in a category, preview exact Git commands, and confirm before deletion.
- Deletes local and remote branches through plain Git only.
- Can ignore named remotes when fetching and reviewing remote branches.

## Usage

```sh
go run .
```

Useful flags:

```sh
go run . --dry-run
go run . --protected main,develop,release/*
go run . --old-days 30,60,90
go run . --ignore-remotes upstream,backup
go run . --config .git-cleaner.yml
go run . --init-config
go run . --no-fetch
```

Local config file:

```yaml
protected:
  - main
  - master
  - develop
  - dev
  - development
  - staging
  - release/*
old_days:
  - 30
  - 60
  - 90
ignore_remotes:
  - upstream
  - backup
```

Remote names are matched exactly. Ignored remotes are not fetched or shown for deletion; local branches are still included. The `--ignore-remotes` flag replaces the configured list.

## TUI Keys

- `up/down`, `j/k`: move
- `space`: select branch
- `a`: select all branches in current category
- `u`: unselect all branches in current category
- `p`: preview deletion commands
- `d`: delete selected branches
- `q`, `ctrl+c`: quit
