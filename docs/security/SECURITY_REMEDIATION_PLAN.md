# Security Remediation Plan

**Branch**: `security/cleanup-review` **Date**: 2026-01-04 **Scan Tool**:
`./scripts/security-check.sh`

---

## Executive Summary

| Category        | Issues | Critical | High | Medium | Low |
| --------------- | ------ | -------- | ---- | ------ | --- |
| Trivy (Secrets) | 2      | 0        | 0    | 2      | 0   |
| govulncheck     | 0      | -        | -    | -      | -   |
| ESLint Security | 29     | 0        | 0    | 1      | 28  |
| Gosec           | 0      | -        | -    | -      | -   |

**Overall Risk**: LOW - No critical vulnerabilities. Issues are primarily false
positives or low-risk patterns.

---

## Issue 1: Trivy - JWT Token in Python Scripts

**Severity**: MEDIUM (False Positive) **Files**:

- `scripts/auth/config.py:14`
- `scripts/auth/__pycache__/config.cpython-313.pyc`

**Description**: Trivy detects a JWT token pattern in the Supabase anon key.

**Analysis**: This is the Supabase **anon key** (equivalent to Stripe's `pk_*`
publishable key). It is designed to be public and is already embedded in the
frontend dashboard. This is NOT a secret key.

**Remediation Options**:

| Option                             | Effort | Recommendation                         |
| ---------------------------------- | ------ | -------------------------------------- |
| A. Add `.trivyignore` rule         | Low    | **Recommended**                        |
| B. Move to env-only (no fallback)  | Medium | Not recommended - breaks CLI usability |
| C. Accept as is with documentation | None   | Acceptable                             |

**Action**:

1. Create `.trivyignore` file to suppress this false positive
2. Delete untracked `__pycache__` files (already in `.gitignore`)
3. Add comment in `config.py` explaining this is a publishable key

---

## Issue 2: ESLint - Timing Attack Warnings (auth.js)

**Severity**: LOW (False Positive) **Files**: `web/static/js/auth.js` lines 620,
1035

**Description**: ESLint detects `password === confirm` comparisons as potential
timing attacks.

**Analysis**: Timing attacks apply when comparing **secret tokens or hashes**
server-side. These warnings are for **user-entered password confirmation**
during signup - the values are typed by the user and visible on screen. Not a
real vulnerability.

**Remediation Options**:

| Option                          | Effort | Recommendation             |
| ------------------------------- | ------ | -------------------------- |
| A. Add ESLint disable comments  | Low    | **Recommended**            |
| B. Use constant-time comparison | Medium | Overkill for this use case |
| C. Accept as is                 | None   | Acceptable                 |

**Action**: Add inline `// eslint-disable-next-line` comments with explanation.

---

## Issue 3: ESLint - Object Injection Warnings

**Severity**: LOW **Files**: Multiple JS files (24 warnings)

**Description**: ESLint flags `obj[key]` patterns where `key` is a variable.

**Analysis by File**:

### auth.js:459 - `titles[formType]`

- **Risk**: LOW - `formType` is controlled internally (signup/login/forgot)
- **Action**: Add allowlist validation

### gnh-data-binder.js:444-451 - Form data processing

- **Risk**: LOW - Keys come from HTML form field `name` attributes
- **Action**: No change needed - standard form processing pattern

### gnh-data-binder.js:924-937, 1078 - Data binding

- **Risk**: LOW - Internal template variable processing
- **Action**: No change needed

### gnh-metadata.js:112-125 - Metadata lookups

- **Risk**: LOW - Keys are hardcoded constants
- **Action**: No change needed

### job-page.js - Multiple locations

- **Risk**: LOW - Internal data structure traversal
- **Action**: No change needed

**Remediation**: Add ESLint configuration to reduce noise from known-safe
patterns.

---

## Issue 4: ESLint - Non-literal RegExp (bb-data-binder.js:586)

**Severity**: MEDIUM **File**: `web/static/js/gnh-data-binder.js:586`

**Description**: `new RegExp(rules.pattern)` uses a variable pattern, risking
ReDoS.

**Analysis**: The `rules.pattern` comes from validation rules defined in code,
not user input. However, if validation rules are ever loaded from external
sources, this could be exploited.

**Remediation**:

1. Add try-catch around RegExp construction
2. Add timeout/complexity limit for pattern matching
3. Document that patterns must be developer-controlled

**Action**: Wrap in try-catch and add validation.

---

## Implementation Plan

### Phase 1: Quick Wins (Low Effort)

- [ ] Create `.trivyignore` for anon key false positive
- [ ] Delete `scripts/auth/__pycache__/` directory
- [ ] Add ESLint disable comments for timing attack false positives
- [ ] Add comment to `config.py` documenting publishable key

### Phase 2: Code Improvements (Medium Effort)

- [ ] Add allowlist validation for `formType` in auth.js
- [ ] Wrap RegExp construction in try-catch in gnh-data-binder.js
- [ ] Update `.eslintrc` to reduce false positive noise

### Phase 3: Documentation

- [ ] Document security scan baseline in this file
- [ ] Add security section to CONTRIBUTING.md

---

## ESLint Configuration Update

Update `eslint.config.mjs` (ESLint v9 flat config format):

```javascript
import security from "eslint-plugin-security";

export default [
  {
    files: ["web/**/*.js"],
    plugins: {
      security,
    },
    rules: {
      // These rules flag false positives in this codebase
      // Object injection: All flagged uses are internal data processing, not user input
      "security/detect-object-injection": "off",
      // Timing attacks: Flagged on password confirmation UI, not secret comparison
      "security/detect-possible-timing-attacks": "off",
      // Non-literal regexp: Pattern comes from code, not user input; wrapped in try-catch
      "security/detect-non-literal-regexp": "warn",
    },
  },
];
```

---

## Trivyignore File

Create `.trivyignore`:

```text
# Supabase anon key is a publishable key (like Stripe pk_*), not a secret
# It's designed to be public and is already in the frontend
scripts/auth/config.py
```

---

## Acceptance Criteria

After remediation:

- [ ] `./scripts/security-check.sh` shows 0 actionable issues
- [ ] No new security warnings introduced
- [ ] All changes documented
- [ ] Tests pass
