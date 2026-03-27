# Git Branching and PR Workflow

## Branch Structure

Hover uses a simplified branching strategy:

```
main (production) ← Direct PRs from feature branches
```

### Branch Purposes

- **main**: Production branch, deployed to live environment
- **feature branches**: Individual development work (deleted after merge)
- **PR preview apps**: Isolated testing environments with dedicated databases

## Development Workflow

### 1. Start New Work

```bash
# Always branch from main for new features
git checkout main
git pull origin main
git checkout -b feature/descriptive-name

# For bug fixes
git checkout -b bug/issue-description

# For documentation
git checkout -b docs/what-you-are-documenting
```

### 2. Development Process

```bash
# Make changes
# Run tests locally
./run-tests.sh

# Commit — short, plain messages (5-6 words)
git add specific-file.go
git commit -m "Add cache warming endpoint"
```

### 3. Push Feature Branch

```bash
git push origin feature/your-feature
```

### 4. Create Pull Request

**Streamlined Workflow: Feature → Main**

1. **Create PR to main branch**:
   - PR automatically triggers preview app deployment
   - Preview app gets isolated Supabase database with migrations applied
   - Tests run automatically via GitHub Actions
   - Test your changes on preview URL: `hover-pr-[number].fly.dev`
   - Code review and approval
   - Merge and automatically delete feature branch

## Commit Message Convention

Short, plain English — 5-6 words maximum. No conventional commit prefixes, no AI
attribution.

```
Add user authentication flow
Fix API rate limiting bug
Update Supabase migration schema
Remove unused crawler config
```

## PR Guidelines

### Creating a PR

1. **Title**: Clear, descriptive summary
2. **Description**:
   - What changed and why
   - Related issue numbers
   - Testing performed
3. **Checklist**:
   - [ ] Tests pass locally
   - [ ] Documentation updated
   - [ ] No secrets in code

### PR Review Process

1. **Automated Checks**:
   - GitHub Actions tests must pass
   - No merge conflicts
   - Supabase preview successful

2. **Review Focus**:
   - Code quality and standards
   - Critical functionality works correctly
   - Documentation completeness
   - Security considerations

## Database Migrations

When your PR includes database changes:

1. **Create migration file**:

   ```bash
   supabase migration new your_migration_name
   ```

2. **Test locally**:

   ```bash
   supabase db reset
   ```

3. **Push with feature branch**:
   - Migrations auto-apply to PR preview branch
   - Review schema changes in isolated preview database

## Merge Strategy

- **Feature → main**: Squash and merge (clean history)

## Emergency Fixes

For critical production issues:

```bash
# Create hotfix from main
git checkout main
git checkout -b hotfix/critical-issue

# Fix and test
# Create PR directly to main
# Will get preview app for immediate testing
```

## Branch Cleanup Policy

**Mandatory**: All feature branches must be deleted after merging to keep the
repository clean.

### Automatic Cleanup

- GitHub can auto-delete branches after PR merge (recommended setting)
- Use "Squash and merge" for feature branches to maintain clean history

### Manual Cleanup

If not automated:

```bash
# Delete local feature branch
git branch -d feature/your-feature

# Delete remote feature branch
git push origin --delete feature/your-feature

# Prune stale remote references
git remote prune origin
```

### Branch Lifecycle

1. **Create**: Branch from main for new work
2. **Develop**: Make changes and commit
3. **PR**: Create pull request (usually to main)
4. **Review**: Code review and testing
5. **Merge**: Squash and merge to target branch
6. **Delete**: Immediately delete the feature branch

All feature branches are deleted after merge - no persistent staging branches
needed.

## Common Scenarios

### Updating Feature Branch

```bash
# If main has new changes
git checkout main
git pull origin main
git checkout feature/your-feature
git rebase main
```

### Multiple Developers

- Communicate about overlapping work
- Use draft PRs for work-in-progress
- Resolve conflicts early

### Long-Running Features

- Rebase regularly from main
- Consider feature flags for gradual rollout
- Break into smaller PRs when possible

## CI/CD Integration

The branching workflow integrates with:

1. **GitHub Actions**: Tests run on PR creation
2. **Supabase Preview Branches**: Isolated databases with migrations for each PR
3. **Fly.io Preview Apps**: Temporary apps for PR testing
4. **Fly.io Production**: Main branch auto-deploys to production

See [CI/CD Documentation](./testing/ci-cd.md) for details.
