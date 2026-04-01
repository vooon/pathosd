# AGENTS.md

## Scope
Instructions for contributors/agents working in this repository (`pathosd`), focused on GoBGP + checker integration.

## Critical Rules
- User directives are absolute: if the user says `DO NOT <action>`, do not perform that action without explicit permission.
- Never edit credential/config files (for example: `clouds.yml`, `.env`) unless the user explicitly asks.
- Never fabricate or overwrite credentials.
- If the user says something is already configured, trust that statement unless they ask you to verify.
- Preserve user data and configuration. If in doubt, ask before changing.
- Do not undo user choices because you think there is a better approach without discussing first.

## Source Of Truth
- Checker implementation and checker parameter schema are owned by the image repository.
- This repository should consume checker behavior, not redefine checker internals.

## Container Versioning
- Use approved SemVer image tags for long-lived defaults.
- Avoid floating tags like `latest` in committed defaults unless explicitly requested.

## Service/Health Behavior
- `gobgp` is expected to run as a Docker container under systemd control.
- Primary behavior goal: announce/withdraw VIP routes based on healthcheck status.
- Prefer bounded startup health probing to avoid endless restart loops when peers are unavailable.
- Keep reload-path health behavior separate from normal startup behavior.

## Review Checklist (Before MR)
- Compare final branch state against `master` (not intermediate commits).
- Check migration tasks for wildcard/path correctness (prefer explicit `find` + loop cleanup where needed).
- Verify docs match runtime behavior and containerized execution examples (use container CLI examples, not host-only binaries).
- Keep documented defaults and actual defaults aligned.
- CI pipeline must pass before merge.
- Local integration runs are optional and depend on user preference/environment.

## Editing Notes
- Keep links absolute unless explicitly requested otherwise.
- Prefer minimal, targeted patches; do not revert unrelated user changes.
- Do not modify sibling/external image-repo files from this repo unless the user explicitly approves it.
- Treat this repository and image-repository responsibilities as separate unless asked to bridge both.

## Commit Messages
- Use Conventional Commits for commit subject lines.
- Reference: `https://www.conventionalcommits.org/en/v1.0.0/#summary`
- Typical format: `<type>(<scope>): <description>`
