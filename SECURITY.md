# Security Policy

## Supported versions

Only the latest release receives security fixes. Run `norcube upgrade`
(or your package manager) to stay current.

## Reporting a vulnerability

Please do not open a public issue for security problems.

Use GitHub's private vulnerability reporting instead: go to the
[Security tab](https://github.com/norcubeplatform/cli/security) of this
repository and click "Report a vulnerability". Reports go directly and
privately to the maintainers.

What to include: the affected command or code path, a reproduction, and
the impact you believe it has (e.g. credential exposure, privilege
escalation). We'll acknowledge your report within a few days and keep
you posted as we work on a fix.

## Scope notes

- The CLI stores refresh tokens in the OS keyring and config in
  `~/.config/norcube/`. Anything that lets another local user or
  process read those secrets is in scope.
- Vulnerabilities in the Norcube platform itself (api.norcube.com,
  app.norcube.com) are also welcome through the same channel; they are
  triaged by the same team.
