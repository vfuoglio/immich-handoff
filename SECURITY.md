# Security policy

## Reporting a problem

Please do **not** open a public issue for a security problem. Use GitHub's private reporting: the **Security** tab of this repository, then **Report a vulnerability**. Say what you found, how to reproduce it, the version (the page footer shows it) and what you think it allows. You do not need a fix.

You will get an answer within a week. A real problem is fixed in a patch release, and you are credited in the changelog unless you prefer not to be.

## Supported versions

The latest release (the `latest` image) gets the fixes. Older minor versions get one only if the problem is serious and the upgrade is hard.

## What counts

Anything that lets someone read or change photos, keys or addresses they should not reach; get around a sign-in, a link's limits or the cross-site header; make the app connect somewhere it should not; or learn a secret from a log or a page. How the app is meant to be trusted, and what it deliberately does not defend against, is in [Security and trust](docs/security.md).
