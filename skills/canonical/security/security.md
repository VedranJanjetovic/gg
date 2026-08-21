# Security Review

Audit code for security vulnerabilities. Focus on exploitability, not theoretical risk.

## Checklist

### Input Handling
- [ ] All user input sanitized/validated at system boundary
- [ ] SQL queries use parameterized statements (no string concatenation)
- [ ] HTML output escaped to prevent XSS
- [ ] File paths validated to prevent traversal (no `../`)
- [ ] Deserialization of untrusted data avoided or sandboxed

### Authentication & Authorization
- [ ] Auth checks on every endpoint (not just the frontend)
- [ ] Session tokens are random, rotated, and expire
- [ ] Password hashing uses bcrypt/scrypt/argon2 (not MD5/SHA)
- [ ] No secrets in code, logs, or error messages
- [ ] RBAC/permissions checked server-side

### Data Protection
- [ ] Sensitive data encrypted at rest and in transit
- [ ] PII not logged or exposed in errors
- [ ] API responses don't leak internal structure
- [ ] CORS configured to specific origins (not `*`)

### Dependencies
- [ ] No known CVEs in dependencies
- [ ] Lock files committed
- [ ] No unnecessary dependencies

## Output Format

For each finding:
```
[CRITICAL/HIGH/MEDIUM/LOW] — [vulnerability type]
  Location: file:line
  Attack: [how to exploit]
  Fix: [what to do]
```
