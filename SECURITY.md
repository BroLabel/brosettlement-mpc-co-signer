# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| latest (`main`) | ✅ |
| older releases | ❌ — please upgrade |

We only provide security fixes for the latest release. If you are running an older version, upgrade before reporting.

---

## Reporting a Vulnerability

**Please do not file public GitHub issues for security vulnerabilities.**

MPC signing infrastructure is security-critical. A vulnerability here can result in loss of funds or key material. We take all reports seriously and will respond quickly.

### How to report

Send an email to **security@brolabel.io** with:

- **Subject**: `[SECURITY] <short description>`
- A description of the vulnerability
- Steps to reproduce (proof-of-concept code or test case if possible)
- Affected component (`mpc-core` / `mpc-co-signer` / both)
- Your assessment of the severity and potential impact
- Your name / handle if you want credit (optional)

You may encrypt your report using our PGP key (see below).

### PGP Key

```
[publish your PGP fingerprint here once generated]
```

---

## Response Timeline

| Stage | Target |
|-------|--------|
| Initial acknowledgement | 48 hours |
| Severity assessment | 5 business days |
| Fix or mitigation plan | 15 business days |
| Coordinated disclosure | Agreed with reporter |

We follow **coordinated disclosure**: we ask that you give us reasonable time to fix the issue before publishing. We will credit you in the release notes unless you prefer to remain anonymous.

---

## Scope

### In scope

- Key generation (DKG) protocol — correctness and entropy
- Signing protocol — unauthorized signing, signature forgery
- Key share confidentiality — leakage of Share A, B, or C
- Share encryption / decryption (`AES-256-GCM` implementation)
- Memory safety — key material persistence beyond intended lifetime (e.g., Share B not zeroed on SIGTERM)
- Authentication / authorization bypass on the co-signer RPC/REST interface
- Dependency vulnerabilities in `tss-lib` or other cryptographic dependencies
- Docker image attack surface (`mpc-co-signer`)

### Out of scope

- Vulnerabilities in infrastructure you control (your HSM, your secrets manager, your network)
- Theoretical attacks with no practical exploit path
- DoS / resource exhaustion (unless it leads to key material exposure)
- Issues in test code or documentation

---

## Threat Model

This library assumes:

- The **server (MPC Signer)** may be compromised — the design should remain secure
- The **client (Co-Signer)** may be compromised — the design should remain secure
- Both parties being simultaneously compromised is outside scope (that is, by design, signing is possible)
- Network transport is **untrusted** — all communication must be authenticated and encrypted

Vulnerabilities that break the noncustodial guarantee (i.e., allow one party to sign without the other) are **critical severity**.

---

## Severity Classification

| Severity | Examples |
|----------|----------|
| **Critical** | Unilateral signing without co-signer participation; full key reconstruction from one share |
| **High** | Key share leakage; authentication bypass; Share B persists in memory after zeroing |
| **Medium** | Side-channel information leakage; insecure defaults in config |
| **Low** | Minor cryptographic weaknesses; non-exploitable logic errors |

---

## Bug Bounty

We do not currently operate a formal bug bounty program. We offer **public recognition** (Hall of Fame in this repo) and may offer discretionary rewards for critical findings. Contact us to discuss before reporting if this matters to you.

---

## Hall of Fame

Thank you to the security researchers who have responsibly disclosed vulnerabilities:

*(none yet — be the first)*
