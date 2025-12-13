# Git Conventions

## 1. Core Summary

This project uses a GitHub-centric workflow with feature branches and conventional commit messages. The main branch serves as the stable release branch, while development happens on feature branches. Pull requests are the primary mechanism for integrating changes, with automated merge commit messages following a standard format.

## 2. Source of Truth

- **Primary Branch Strategy**: `git branch -a` - Shows `main` as the primary branch with feature branches using `feature/` prefix
- **Commit Message Format**: `git log --oneline` - Reveals conventional commit patterns with type(scope): description format
- **PR Merge Pattern**: `git log --grep="Merge pull request"` - Shows standard GitHub PR merge commit format: "Merge pull request #<number> from <author>/<branch>"
- **Recent Example**: `git show --stat HEAD` - Demonstrates current commit structure and affected files

## 3. Observed Patterns

### Branch Types
- `main` - Primary/stable branch
- `feature/<name>` - Feature development branches
- Short topic branches (e.g., `agry`, `amp`, `think`)

### Commit Message Types
- `fix(scope):` - Bug fixes
- `feat(scope):` - New features
- `refactor(scope):` - Code refactoring
- `docs(scope):` - Documentation updates
- `Fixed: #<number>` - Issue resolution commits

### Merge Commits
- Standard GitHub format: "Merge pull request #<number> from <author>/<branch>"
- Used for all PR integrations