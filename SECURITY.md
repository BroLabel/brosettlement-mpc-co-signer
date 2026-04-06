# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| `main` | Yes |
| older releases | No |

We currently provide security fixes only for the latest code on `main`.

## Reporting a Vulnerability

Do not file public GitHub issues for security vulnerabilities.

MPC signing infrastructure is security-critical. A vulnerability here can result in loss of funds or exposure of key material, so we treat reports with high priority.

### How to report

Send an email to **security@brolabel.io** with:

- a short description of the issue
- steps to reproduce
- affected component or flow
- impact and severity assessment, if known
- your name or handle if you want credit

If you want to share encrypted details, mention that in the email and we can coordinate a secure follow-up channel.

## Response Timeline

| Stage | Target |
|-------|--------|
| Initial acknowledgement | 48 hours |
| Severity assessment | 5 business days |
| Fix or mitigation plan | 15 business days |
| Disclosure timing | Coordinated with the reporter |

We follow coordinated disclosure and ask for reasonable time to investigate and fix the issue before public disclosure.

## Scope

### In scope

- key generation and signing flow correctness
- unauthorized signing
- key share confidentiality
- share encryption and decryption
- key material remaining in memory longer than intended
- authentication or authorization bypass on co-signer interfaces
- vulnerabilities in cryptographic or signing dependencies
- Docker image attack surface

### Out of scope

- vulnerabilities in infrastructure outside this repository
- purely theoretical attacks with no practical exploit path
- denial of service issues unless they also expose key material
- documentation or test-only issues without security impact

## Threat Model

This project assumes:

- the signer side may be compromised
- the co-signer side may be compromised
- the network is untrusted
- simultaneous compromise of both trusted parties is outside the intended security boundary

Issues that break the noncustodial guarantee or allow unilateral signing are critical.

## Severity Classification

| Severity | Examples |
|----------|----------|
| Critical | Unilateral signing or private key reconstruction from one side |
| High | Key share leakage, auth bypass, failure to clear sensitive material |
| Medium | Insecure defaults, side-channel leakage with practical impact |
| Low | Minor weaknesses without a practical exploit path |

## Bug Bounty

We do not currently run a formal bug bounty program. We may offer public recognition or discretionary rewards for significant findings.

## Hall of Fame

Thank you to the researchers who disclose issues responsibly.
